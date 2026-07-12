// Package ingest parses one Claude Code JSONL transcript (plus its sidechain
// companions) into the store, following docs/jsonl-format.md exclusively.
package ingest

import (
	"bufio"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/cost" // metrics hook (wave C)
)

// Stats counts rows created by one ingest run (idempotent re-runs report zeros).
type Stats struct {
	Projects     int
	Sessions     int
	Turns        int
	Events       int
	FileChanges  int
	SkippedLines int
}

const (
	maxLineBytes    = 64 << 20 // transcripts embed whole file contents; lines get big
	payloadStrLimit = 2048     // truncation limit for long strings inside payloads
	titleLimit      = 120
)

// File ingests one main transcript .jsonl and any sidechain transcripts under
// its companion dir (<file-without-ext>/subagents/agent-*.jsonl, §1/§7).
func File(db *sql.DB, path string) (Stats, error) {
	var stats Stats

	absPath, err := filepath.Abs(path)
	if err != nil {
		return stats, err
	}
	recs, consumed, err := readRecords(absPath, &stats)
	if err != nil {
		return stats, err
	}
	if len(recs) == 0 {
		return stats, fmt.Errorf("%s: no parseable records", absPath)
	}

	fi, err := os.Stat(absPath)
	if err != nil {
		return stats, err
	}

	tx, err := db.Begin()
	if err != nil {
		return stats, err
	}
	defer tx.Rollback()

	ing := &ingester{tx: tx, stats: &stats, thresholds: DefaultThresholds()}
	if err := ing.upsertProjectAndSession(recs, fi.ModTime(), false); err != nil {
		return stats, err
	}
	if err := ing.processRecords(recs, absPath, false, "", 0); err != nil {
		return stats, err
	}
	if err := ing.recordOffset(absPath, consumed); err != nil {
		return stats, err
	}

	// Sidechain companions: <dir>/<name-without-.jsonl>/subagents/agent-*.jsonl.
	companion := strings.TrimSuffix(absPath, filepath.Ext(absPath))
	sidechains, _ := filepath.Glob(filepath.Join(companion, "subagents", "agent-*.jsonl"))
	sort.Strings(sidechains)
	for _, sc := range sidechains {
		if err := ing.ingestSidechain(sc); err != nil {
			return stats, err
		}
	}

	if err := tx.Commit(); err != nil {
		return stats, err
	}
	return stats, nil
}

// readRecords parses all lines of a .jsonl file; malformed lines are warned and skipped.
func readRecords(path string, stats *Stats) ([]record, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()

	var recs []record
	var consumed int64
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), maxLineBytes)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Bytes()
		consumed += int64(len(line)) + 1
		trimmed := strings.TrimSpace(string(line))
		if trimmed == "" {
			continue
		}
		var r record
		if err := json.Unmarshal([]byte(trimmed), &r); err != nil {
			log.Printf("warn: %s:%d: skipping malformed line: %v", path, lineNo, err)
			stats.SkippedLines++
			continue
		}
		r.raw = []byte(trimmed)
		recs = append(recs, r)
	}
	if err := sc.Err(); err != nil {
		return nil, 0, fmt.Errorf("scan %s: %w", path, err)
	}
	return recs, consumed, nil
}

// ingester carries per-run state across main + sidechain files.
type ingester struct {
	tx         *sql.Tx
	stats      *Stats
	thresholds Thresholds

	projectID int64
	sessionID int64

	// live-pipeline bookkeeping (consumed by the event bus after commit).
	sessionCreated bool
	newEventIDs    []int64

	// pending tool_use calls awaiting their tool_result (keyed by toolu_… id).
	pending map[string]*pendingTool
	// subagent_start event ids keyed by the Agent tool_use id (meta.json join key).
	subagentStarts map[string]int64
}

type pendingTool struct {
	eventID   int64
	name      string
	ts        time.Time
	input     map[string]any
	isAgent   bool
	isCreated bool // row was created this run (vs found via dedup_key)
}

// ── session / project bookkeeping ────────────────────────────────────────────

// upsertProjectAndSession registers/updates the project and session rows for a
// batch of records. mtime is the file's mtime fallback for activity when no
// record carries a timestamp. In sidechain mode title/model are never taken
// from the batch (the sidechain opener repeats the Agent prompt, §7).
func (in *ingester) upsertProjectAndSession(recs []record, mtime time.Time, sidechain bool) error {
	var (
		sessionUUID, cwd, branch, model, firstTS, lastTS string
		title, firstPrompt                               string
	)
	for i := range recs {
		r := &recs[i]
		if sessionUUID == "" && r.SessionID != "" {
			sessionUUID = r.SessionID
		}
		if r.Timestamp != "" {
			if firstTS == "" {
				firstTS = r.Timestamp
			}
			lastTS = r.Timestamp
		}
		if r.UUID != "" { // envelope record
			if cwd == "" && r.CWD != "" {
				cwd = r.CWD
			}
			if branch == "" && r.GitBranch != "" {
				branch = r.GitBranch
			}
		}
		if sidechain {
			continue
		}
		switch r.Type {
		case "ai-title":
			if r.AITitle != "" {
				title = r.AITitle // latest wins (checkpoint snapshot semantics)
			}
		case "assistant":
			if model == "" {
				var m apiMessage
				if json.Unmarshal(r.Message, &m) == nil && m.Model != "" {
					model = m.Model
				}
			}
		case "user":
			if firstPrompt == "" && !r.IsMeta && !r.IsCompactSummary {
				var m apiMessage
				if json.Unmarshal(r.Message, &m) == nil {
					var s string
					if json.Unmarshal(m.Content, &s) == nil && s != "" {
						firstPrompt = truncate(s, titleLimit)
					}
				}
			}
		}
	}
	if sessionUUID == "" {
		return fmt.Errorf("no sessionId found in transcript")
	}
	// title holds an ai-title here (authoritative, may overwrite on update);
	// firstPrompt is only an initial fallback — a tail batch's "first" prompt
	// is NOT the session's first prompt, so it never overwrites (see below).
	insertTitle := title
	if insertTitle == "" {
		insertTitle = firstPrompt
	}

	// Status heuristic (C5): only active | idle | completed in MVP, purely
	// time-based so the ingest path and the status ticker agree.
	lastActivity := parseTS(lastTS)
	if lastActivity.IsZero() {
		lastActivity = mtime
	}
	status := StatusFor(lastActivity, time.Now(), in.thresholds)

	// Resolve the session first: mid-file tail batches may carry no cwd, so an
	// existing session must never depend on batch-derived project fields.
	err := in.tx.QueryRow(
		`SELECT id, project_id FROM sessions WHERE session_uuid = ?`,
		sessionUUID).Scan(&in.sessionID, &in.projectID)
	switch {
	case err == sql.ErrNoRows:
		// Project keyed by cwd path; slug derived as '/' → '-' (§1).
		if cwd == "" {
			cwd = "(unknown)"
		}
		slug := strings.ReplaceAll(cwd, "/", "-")
		perr := in.tx.QueryRow(`SELECT id FROM projects WHERE path = ?`, cwd).Scan(&in.projectID)
		switch {
		case perr == sql.ErrNoRows:
			res, err := in.tx.Exec(
				`INSERT INTO projects (path, slug, first_seen, last_activity) VALUES (?, ?, ?, ?)`,
				cwd, slug, firstTS, lastTS)
			if err != nil {
				return fmt.Errorf("insert project: %w", err)
			}
			in.projectID, _ = res.LastInsertId()
			in.stats.Projects++
		case perr != nil:
			return perr
		}

		res, err := in.tx.Exec(
			`INSERT INTO sessions (project_id, session_uuid, model, git_branch, cwd, status,
			                       started_at, ended_at, title, source)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'jsonl')`,
			in.projectID, sessionUUID, nullStr(model), nullStr(branch), cwd, status,
			firstTS, nullStr(lastTS), nullStr(insertTitle))
		if err != nil {
			return fmt.Errorf("insert session: %w", err)
		}
		in.sessionID, _ = res.LastInsertId()
		in.stats.Sessions++
		in.sessionCreated = true
	case err != nil:
		return err
	default:
		if _, err := in.tx.Exec(
			`UPDATE sessions SET model = COALESCE(?, model), status = ?,
			                     ended_at = CASE WHEN ? = '' THEN ended_at
			                                     ELSE MAX(COALESCE(ended_at,''), ?) END,
			                     title = COALESCE(?, COALESCE(title, ?)) WHERE id = ?`,
			nullStr(model), status, lastTS, lastTS,
			nullStr(title), nullStr(firstPrompt), in.sessionID); err != nil {
			return err
		}
	}
	if lastTS != "" {
		if _, err := in.tx.Exec(
			`UPDATE projects SET last_activity = MAX(COALESCE(last_activity,''), ?) WHERE id = ?`,
			lastTS, in.projectID); err != nil {
			return err
		}
	}
	return nil
}

// ── record processing ────────────────────────────────────────────────────────

// processRecords walks records in file order. In sidechain mode no turns are
// created and every produced event is parented to parentEventID (§7 mapping);
// dedup keys are scoped by scope (sidechain agentId — uuid space restarts per file, C3).
func (in *ingester) processRecords(recs []record, path string, sidechain bool, scope string, parentEventID int64) error {
	if in.pending == nil {
		in.pending = map[string]*pendingTool{}
		in.subagentStarts = map[string]int64{}
	}
	seq, err := in.nextSeq()
	if err != nil {
		return err
	}
	var curTurnID int64
	var curMsgID string

	for i := range recs {
		r := &recs[i]
		dedup := in.dedupKey(r, path, scope)

		switch r.Type {
		case "user":
			var m apiMessage
			if err := json.Unmarshal(r.Message, &m); err != nil {
				log.Printf("warn: %s: bad user message (%v), skipping", path, err)
				in.stats.SkippedLines++
				continue
			}
			var promptText string
			if json.Unmarshal(m.Content, &promptText) == nil {
				// (a) real prompt — string content (§4a).
				if r.IsMeta || r.IsCompactSummary {
					continue // injected skill bodies / compaction summaries
				}
				if sidechain {
					continue // the sidechain opener repeats the Agent prompt — no new event
				}
				turnID, created, err := in.upsertTurn(seq, "user", "", "", r.Timestamp, nil)
				if err != nil {
					return err
				}
				if created {
					seq++
				}
				curTurnID, curMsgID = turnID, ""
				payload := map[string]any{"content": truncate(promptText, payloadStrLimit)}
				if r.PromptSource != "" {
					payload["promptSource"] = r.PromptSource
				}
				if _, _, err := in.insertEvent(eventRow{
					turnID: turnID, ts: r.Timestamp, typ: "user_prompt",
					parentEventID: parentEventID, payload: payload, dedup: dedup,
				}); err != nil {
					return err
				}
				continue
			}
			// (b) tool_result carrier — array content (§4b).
			var blocks []contentBlock
			if err := json.Unmarshal(m.Content, &blocks); err != nil {
				log.Printf("warn: %s: unrecognized user content shape, skipping", path)
				in.stats.SkippedLines++
				continue
			}
			for _, b := range blocks {
				if b.Type != "tool_result" {
					continue
				}
				if err := in.closeToolCall(r, b, dedup, parentEventID); err != nil {
					return err
				}
			}

		case "assistant":
			var m apiMessage
			if err := json.Unmarshal(r.Message, &m); err != nil {
				log.Printf("warn: %s: bad assistant message (%v), skipping", path, err)
				in.stats.SkippedLines++
				continue
			}
			if !sidechain {
				if m.ID != curMsgID {
					// New API message → new assistant turn; usage counted ONCE (C1).
					turnID, created, err := in.upsertTurn(seq, "assistant", m.ID, m.Model, r.Timestamp, m.Usage)
					if err != nil {
						return err
					}
					if created {
						seq++
						// metrics hook (wave C): price the turn from its usage +
						// per-message model — the single cost integration point.
						if u := m.Usage; u != nil {
							if c := cost.EnrichTurn(cost.Turn{
								Model:            m.Model,
								TokensIn:         &u.InputTokens,
								TokensOut:        &u.OutputTokens,
								TokensCacheRead:  &u.CacheReadInputTokens,
								TokensCacheWrite: &u.CacheCreationInputTokens,
							}); c != nil {
								if _, err := in.tx.Exec(
									`UPDATE turns SET cost_usd = ? WHERE id = ?`, *c, turnID); err != nil {
									return err
								}
							}
						}
					}
					curTurnID, curMsgID = turnID, m.ID
				} else if curTurnID != 0 {
					// Later split line of the same message → extend the turn.
					if _, err := in.tx.Exec(
						`UPDATE turns SET ended_at = ? WHERE id = ?`, r.Timestamp, curTurnID); err != nil {
						return err
					}
				}
			}
			var blocks []contentBlock
			if err := json.Unmarshal(m.Content, &blocks); err != nil {
				continue
			}
			for _, b := range blocks {
				if b.Type != "tool_use" {
					continue // thinking / text blocks are turn content, not events
				}
				if err := in.openToolCall(r, b, dedup, curTurnID, parentEventID, sidechain); err != nil {
					return err
				}
			}

		case "system":
			switch r.Subtype {
			case "api_error":
				payload := map[string]any{}
				if len(r.Error) > 0 {
					payload["error"] = json.RawMessage(r.Error)
				}
				if _, _, err := in.insertEvent(eventRow{
					turnID: curTurnID, ts: r.Timestamp, typ: "error", status: "error",
					parentEventID: parentEventID, payload: payload, dedup: dedup,
				}); err != nil {
					return err
				}
			case "turn_duration":
				// Turn-boundary marker: refine the current turn's end (§10).
				if curTurnID != 0 {
					if _, err := in.tx.Exec(
						`UPDATE turns SET ended_at = COALESCE(ended_at, ?) WHERE id = ?`,
						r.Timestamp, curTurnID); err != nil {
						return err
					}
				}
			case "compact_boundary":
				// Kept as payload-only event; design adds no dedicated type for it.
				if _, _, err := in.insertEvent(eventRow{
					ts: r.Timestamp, typ: "unknown", parentEventID: parentEventID,
					payload: map[string]any{"raw": json.RawMessage(r.raw)}, dedup: dedup,
				}); err != nil {
					return err
				}
			}
			// Other system subtypes: not ingested in MVP (design §2 events comment).

		case "attachment", "last-prompt", "mode", "permission-mode", "ai-title",
			"file-history-snapshot", "queue-operation", "pr-link", "bridge-session", "agent-name":
			// Known non-action records — ignored in MVP (mapping §11).

		default:
			// Unknown record type → events row with type='unknown', payload = raw JSON.
			ts := r.Timestamp
			if _, _, err := in.insertEvent(eventRow{
				ts: ts, typ: "unknown", parentEventID: parentEventID,
				payload: map[string]any{"raw": json.RawMessage(r.raw)}, dedup: dedup,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// openToolCall creates the event for one tool_use block (§5): tool_call,
// or subagent_start for Agent (C6), or skill_use for Skill (§9).
func (in *ingester) openToolCall(r *record, b contentBlock, dedup string, turnID, parentEventID int64, sidechain bool) error {
	input := map[string]any{}
	if len(b.Input) > 0 {
		if err := json.Unmarshal(b.Input, &input); err != nil {
			input = map[string]any{}
		}
	}
	truncateStrings(input)
	input["tool_use_id"] = b.ID

	typ := "tool_call"
	switch b.Name {
	case "Agent":
		typ = "subagent_start"
	case "Skill":
		typ = "skill_use"
	}
	if sidechain {
		turnID = 0
	}
	id, created, err := in.insertEvent(eventRow{
		turnID: turnID, ts: r.Timestamp, typ: typ, toolName: b.Name,
		parentEventID: parentEventID, payload: input, dedup: dedup + "#" + b.ID,
	})
	if err != nil {
		return err
	}
	in.pending[b.ID] = &pendingTool{
		eventID: id, name: b.Name, ts: parseTS(r.Timestamp),
		input: input, isAgent: b.Name == "Agent", isCreated: created,
	}
	if b.Name == "Agent" {
		in.subagentStarts[b.ID] = id
	}
	return nil
}

// closeToolCall handles one tool_result block on a user line (§4b): sets
// status/duration on the pending event, emits subagent_stop for Agent (§7),
// and extracts file_changes from Edit/Write results (§8).
func (in *ingester) closeToolCall(r *record, b contentBlock, dedup string, parentEventID int64) error {
	p, ok := in.pending[b.ToolUseID]
	if !ok {
		// Incremental tail: the tool_use may have been ingested in an earlier
		// batch — recover the still-open event from the DB (nil if unknown or
		// already closed; closed events need no further work).
		p = in.recoverPending(b.ToolUseID)
		if p == nil {
			return nil
		}
	}
	delete(in.pending, b.ToolUseID)

	status := "ok"
	if b.IsError {
		status = "error"
	}
	durationMs := parseTS(r.Timestamp).Sub(p.ts).Milliseconds()

	if p.isAgent {
		// Agent completion → subagent_stop, parented to subagent_start (§7).
		var ar agentResult
		if len(r.ToolUseResult) > 0 {
			_ = json.Unmarshal(r.ToolUseResult, &ar)
		}
		// Background (run_in_background) launch: the tool_result arrives
		// immediately ("Async agent launched") while the sidechain keeps
		// running — neither an error nor a real duration. The sidechain
		// ingest refines duration_ms later (reconcileAsyncSubagent).
		async := ar.IsAsync || ar.Status == "async_launched"
		if ar.Status != "" && ar.Status != "completed" && !async {
			status = "error"
		}
		if ar.TotalDurationMs > 0 {
			durationMs = ar.TotalDurationMs
		}
		payload := map[string]any{
			"agentId": ar.AgentID, "agentType": ar.AgentType, "status": ar.Status,
			"totalTokens": ar.TotalTokens, "tool_use_id": b.ToolUseID,
		}
		if len(ar.ToolStats) > 0 {
			payload["toolStats"] = json.RawMessage(ar.ToolStats)
		}
		if _, _, err := in.insertEvent(eventRow{
			ts: r.Timestamp, typ: "subagent_stop", toolName: "Agent",
			parentEventID: p.eventID, status: status, durationMs: durationMs,
			payload: payload, dedup: dedup + "#" + b.ToolUseID,
		}); err != nil {
			return err
		}
		// Also close the start event's status for convenience.
		if _, err := in.tx.Exec(
			`UPDATE events SET status = ?, duration_ms = ? WHERE id = ?`,
			status, durationMs, p.eventID); err != nil {
			return err
		}
		// Heal the tail race: sidechain events ingested before this Agent
		// call existed carry a NULL parent — adopt them now that the result
		// reveals the sidechain agentId, then derive the real duration of a
		// background agent from whatever sidechain rows are already stored.
		if err := in.adoptOrphanSidechainEvents(ar.AgentID, p.eventID); err != nil {
			return err
		}
		if async {
			return in.reconcileAsyncSubagent(p.eventID, "")
		}
		return nil
	}

	// Regular tool: merge structured result into payload (minus originalFile, §11).
	payload := map[string]any{"input": p.input}
	if len(r.ToolUseResult) > 0 {
		var res any
		if json.Unmarshal(r.ToolUseResult, &res) == nil {
			if m, ok := res.(map[string]any); ok {
				delete(m, "originalFile")
				truncateStrings(m)
				payload["result"] = m
			} else if s, ok := res.(string); ok {
				payload["result"] = truncate(s, payloadStrLimit)
			}
		}
	}
	payloadJSON, _ := json.Marshal(payload)
	if _, err := in.tx.Exec(
		`UPDATE events SET status = ?, duration_ms = ?, payload = ? WHERE id = ?`,
		status, durationMs, string(payloadJSON), p.eventID); err != nil {
		return err
	}

	// Edit / Write results carry structuredPatch → file_changes (§8).
	if (p.name == "Edit" || p.name == "Write") && len(r.ToolUseResult) > 0 && !b.IsError {
		if err := in.insertFileChange(p, r.ToolUseResult); err != nil {
			return err
		}
	}
	return nil
}

// recoverPending rebuilds a pendingTool from an events row created by an
// earlier tail batch. Matches on the tool_use_id kept in the payload: open
// tool_call/skill_use events store it top-level; closed ones nest the input
// under "input" (so a plain tool_call that is already closed is not matched —
// nothing left to do for it). subagent_start keeps it top-level for the
// sidechain join; re-closing it is idempotent (dedup absorbs the stop event).
func (in *ingester) recoverPending(toolUseID string) *pendingTool {
	var (
		id       int64
		toolName sql.NullString
		ts       string
		typ      string
		payload  sql.NullString
	)
	err := in.tx.QueryRow(
		`SELECT id, tool_name, ts, type, payload FROM events
		 WHERE session_id = ? AND type IN ('tool_call','subagent_start','skill_use')
		   AND json_extract(payload, '$.tool_use_id') = ?`,
		in.sessionID, toolUseID).Scan(&id, &toolName, &ts, &typ, &payload)
	if err != nil {
		return nil // sql.ErrNoRows or transient error → treat as unknown tool_use
	}
	input := map[string]any{}
	if payload.Valid {
		_ = json.Unmarshal([]byte(payload.String), &input)
	}
	return &pendingTool{
		eventID: id, name: toolName.String, ts: parseTS(ts),
		input: input, isAgent: typ == "subagent_start",
	}
}

func (in *ingester) insertFileChange(p *pendingTool, rawResult json.RawMessage) error {
	var fc fileChangeResult
	if err := json.Unmarshal(rawResult, &fc); err != nil || fc.FilePath == "" {
		return nil // string results (errors) or unexpected shapes — nothing to record
	}
	changeType := "edit"
	if p.name == "Write" && fc.Type == "create" {
		changeType = "create"
	}
	additions, deletions := 0, 0
	var diff strings.Builder
	for _, h := range fc.StructuredPatch {
		fmt.Fprintf(&diff, "@@ -%d,%d +%d,%d @@\n", h.OldStart, h.OldLines, h.NewStart, h.NewLines)
		for _, l := range h.Lines {
			diff.WriteString(l)
			diff.WriteByte('\n')
			if strings.HasPrefix(l, "+") {
				additions++
			} else if strings.HasPrefix(l, "-") {
				deletions++
			}
		}
	}
	// Idempotency: one file_change per event.
	var exists int
	err := in.tx.QueryRow(`SELECT 1 FROM file_changes WHERE event_id = ?`, p.eventID).Scan(&exists)
	if err == nil {
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}
	if _, err := in.tx.Exec(
		`INSERT INTO file_changes (event_id, session_id, file_path, change_type, additions, deletions, diff)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		p.eventID, in.sessionID, fc.FilePath, changeType, additions, deletions, diff.String()); err != nil {
		return err
	}
	in.stats.FileChanges++
	return nil
}

// ── sidechains ───────────────────────────────────────────────────────────────

func (in *ingester) ingestSidechain(path string) error {
	metaPath := strings.TrimSuffix(path, ".jsonl") + ".meta.json"
	var meta sidechainMeta
	if raw, err := os.ReadFile(metaPath); err == nil {
		_ = json.Unmarshal(raw, &meta)
	}
	parentID := in.subagentStarts[meta.ToolUseID]
	if parentID == 0 && meta.ToolUseID != "" {
		// Standalone re-ingest: recover the parent via the stored tool_use_id.
		_ = in.tx.QueryRow(
			`SELECT id FROM events WHERE type = 'subagent_start' AND session_id = ?
			   AND json_extract(payload, '$.tool_use_id') = ?`,
			in.sessionID, meta.ToolUseID).Scan(&parentID)
	}

	recs, consumed, err := readRecords(path, in.stats)
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		return nil
	}
	// Sidechain uuid space restarts per file (C3) → scope dedup keys by agentId.
	scope := recs[0].AgentID
	if scope == "" {
		scope = filepath.Base(path)
	}
	if err := in.processRecords(recs, path, true, scope, parentID); err != nil {
		return err
	}
	if parentID != 0 {
		if err := in.adoptOrphanSidechainEvents(scope, parentID); err != nil {
			return err
		}
		if err := in.reconcileAsyncSubagent(parentID, lastRecordTS(recs)); err != nil {
			return err
		}
	}
	return in.recordOffset(path, consumed)
}

// adoptOrphanSidechainEvents backfills parent_event_id for sidechain events
// that were ingested before their parent subagent_start row existed (live-tail
// race: a sidechain batch can be flushed and picked up before the main
// transcript's Agent tool_use line). Matching is by dedup-key scope — the
// sidechain agentId prefix. Idempotent: already-parented rows are untouched.
func (in *ingester) adoptOrphanSidechainEvents(scope string, parentID int64) error {
	if scope == "" || parentID == 0 {
		return nil
	}
	_, err := in.tx.Exec(
		`UPDATE events SET parent_event_id = ?
		 WHERE session_id = ? AND parent_event_id IS NULL AND turn_id IS NULL
		   AND substr(dedup_key, 1, ?) = ?`,
		parentID, in.sessionID, len(scope)+1, scope+":")
	return err
}

// reconcileAsyncSubagent fixes the duration of background (run_in_background)
// Agent calls. Their tool_result arrives ~immediately with status
// "async_launched" and no totalDurationMs, so the subagent_start/stop rows are
// closed with the launch roundtrip (~0.1s) while the sidechain runs for
// minutes. Once sidechain records are ingested, the real duration is the span
// subagent_start.ts → last sidechain record timestamp (lastTS; when empty the
// latest stored child event ts is used instead). Monotonic and idempotent:
// the duration only ever grows towards the sidechain's true end, so live tail
// batches refine it and re-ingest converges to the same value.
func (in *ingester) reconcileAsyncSubagent(parentID int64, lastTS string) error {
	if lastTS == "" {
		var maxTS sql.NullString
		if err := in.tx.QueryRow(
			`SELECT MAX(ts) FROM events WHERE parent_event_id = ? AND type != 'subagent_stop'`,
			parentID).Scan(&maxTS); err != nil {
			return err
		}
		if !maxTS.Valid {
			return nil // no sidechain rows yet — a later sidechain batch refines it
		}
		lastTS = maxTS.String
	}
	var stopID int64
	var startTS string
	err := in.tx.QueryRow(
		`SELECT stop.id, start.ts FROM events stop
		 JOIN events start ON start.id = stop.parent_event_id
		 WHERE stop.parent_event_id = ? AND stop.type = 'subagent_stop'
		   AND json_extract(stop.payload, '$.status') = 'async_launched'`,
		parentID).Scan(&stopID, &startTS)
	if err == sql.ErrNoRows {
		return nil // foreground agent (or stop not ingested yet) — nothing to fix
	}
	if err != nil {
		return err
	}
	d := parseTS(lastTS).Sub(parseTS(startTS)).Milliseconds()
	if d <= 0 {
		return nil
	}
	_, err = in.tx.Exec(
		`UPDATE events SET duration_ms = ?
		 WHERE id IN (?, ?) AND (duration_ms IS NULL OR duration_ms < ?)`,
		d, parentID, stopID, d)
	return err
}

// ── row helpers ──────────────────────────────────────────────────────────────

func (in *ingester) nextSeq() (int, error) {
	var seq sql.NullInt64
	if err := in.tx.QueryRow(
		`SELECT MAX(seq) FROM turns WHERE session_id = ?`, in.sessionID).Scan(&seq); err != nil {
		return 0, err
	}
	if seq.Valid {
		return int(seq.Int64) + 1, nil
	}
	return 0, nil
}

// upsertTurn inserts a turn; on (session_id, seq) conflict the existing row is
// reused (re-ingest). model is the per-message API model (empty → NULL, e.g.
// user turns). Returns (id, createdNow).
func (in *ingester) upsertTurn(seq int, role, messageID, model, ts string, u *usage) (int64, bool, error) {
	// Re-ingest: match an existing turn by identity first.
	var id int64
	var err error
	if messageID != "" {
		err = in.tx.QueryRow(
			`SELECT id FROM turns WHERE session_id = ? AND message_id = ?`,
			in.sessionID, messageID).Scan(&id)
	} else {
		err = in.tx.QueryRow(
			`SELECT id FROM turns WHERE session_id = ? AND role = 'user' AND started_at = ?`,
			in.sessionID, ts).Scan(&id)
	}
	if err == nil {
		return id, false, nil
	}
	if err != sql.ErrNoRows {
		return 0, false, err
	}

	var tin, tout, tcr, tcw any
	if u != nil {
		tin, tout, tcr, tcw = u.InputTokens, u.OutputTokens, u.CacheReadInputTokens, u.CacheCreationInputTokens
	}
	res, err := in.tx.Exec(
		`INSERT INTO turns (session_id, seq, role, message_id, model, started_at, ended_at,
		                    tokens_in, tokens_out, tokens_cache_read, tokens_cache_write)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.sessionID, seq, role, nullStr(messageID), nullStr(model), ts, ts, tin, tout, tcr, tcw)
	if err != nil {
		return 0, false, fmt.Errorf("insert turn seq=%d: %w", seq, err)
	}
	id, _ = res.LastInsertId()
	in.stats.Turns++
	return id, true, nil
}

type eventRow struct {
	turnID        int64
	ts            string
	typ           string
	toolName      string
	parentEventID int64
	status        string
	durationMs    int64
	payload       map[string]any
	dedup         string
}

// insertEvent inserts an event with dedup_key uniqueness; on conflict the
// existing row id is returned. Returns (id, createdNow).
func (in *ingester) insertEvent(e eventRow) (int64, bool, error) {
	var payloadJSON any
	if e.payload != nil {
		b, err := json.Marshal(e.payload)
		if err != nil {
			return 0, false, err
		}
		payloadJSON = string(b)
	}
	res, err := in.tx.Exec(
		`INSERT INTO events (session_id, turn_id, ts, type, tool_name, parent_event_id,
		                     status, duration_ms, payload, dedup_key)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(dedup_key) DO NOTHING`,
		in.sessionID, nullID(e.turnID), e.ts, e.typ, nullStr(e.toolName), nullID(e.parentEventID),
		nullStr(e.status), nullInt(e.durationMs), payloadJSON, e.dedup)
	if err != nil {
		return 0, false, fmt.Errorf("insert event %s: %w", e.typ, err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		id, _ := res.LastInsertId()
		in.stats.Events++
		in.newEventIDs = append(in.newEventIDs, id)
		return id, true, nil
	}
	var id int64
	if err := in.tx.QueryRow(`SELECT id FROM events WHERE dedup_key = ?`, e.dedup).Scan(&id); err != nil {
		return 0, false, err
	}
	return id, false, nil
}

func (in *ingester) recordOffset(path string, offset int64) error {
	var inode any
	if fi, err := os.Stat(path); err == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			inode = int64(st.Ino)
		}
	}
	_, err := in.tx.Exec(
		`INSERT INTO file_offsets (file_path, byte_offset, inode) VALUES (?, ?, ?)
		 ON CONFLICT(file_path) DO UPDATE SET byte_offset = excluded.byte_offset, inode = excluded.inode`,
		path, offset, inode)
	return err
}

// dedupKey: record uuid (globally unique per file family, C3), scoped by the
// sidechain agentId because sidechain uuid spaces restart per file; uuid-less
// lines fall back to SHA-256(file path + raw line).
func (in *ingester) dedupKey(r *record, path, scope string) string {
	if r.UUID != "" {
		if scope != "" {
			return scope + ":" + r.UUID
		}
		return r.UUID
	}
	sum := sha256.Sum256(append([]byte(path+"\n"), r.raw...))
	return hex.EncodeToString(sum[:])
}

// ── small utilities ──────────────────────────────────────────────────────────

// lastRecordTS returns the timestamp of the last record that carries one.
func lastRecordTS(recs []record) string {
	for i := len(recs) - 1; i >= 0; i-- {
		if recs[i].Timestamp != "" {
			return recs[i].Timestamp
		}
	}
	return ""
}

func parseTS(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…[truncated]"
}

// truncateStrings caps long top-level string values (prompts, file contents).
func truncateStrings(m map[string]any) {
	for k, v := range m {
		if s, ok := v.(string); ok && len(s) > payloadStrLimit {
			m[k] = truncate(s, payloadStrLimit)
		}
	}
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func nullInt(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}
