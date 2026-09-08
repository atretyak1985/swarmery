// Package ingest parses one Claude Code JSONL transcript (plus its sidechain
// companions) into the store, following docs/jsonl-format.md exclusively.
package ingest

import (
	"bufio"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
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

// UnknownProjectPath is the placeholder projects.path used when a session's
// cwd is not (yet) known: a hook POST that beats the JSONL tail, or a tail
// batch that carries timestamps but no cwd. Sessions attached to it are
// stubs — the ingest upsert and HealStubSessions re-attribute them as soon as
// the real cwd is learned. A batch with no timestamp AT ALL never gets this
// far: it mints nothing (see ErrNoSessionEvidence).
const UnknownProjectPath = "(unknown)"

// ErrNoSessionEvidence is returned by upsertProjectAndSession when a batch
// would have to MINT a session row but carries no record timestamp at all —
// i.e. nothing that says a session ever ran. Claude Code writes such files:
// a transcript whose only line is {"type":"ai-title",…}, no envelope, no
// cwd, no clock. Minting a row from one produced a session with started_at
// ” on the '(unknown)' project, permanently stuck in whatever status the
// mtime heuristic guessed at insert time.
//
// Callers treat it as "skip this batch and do NOT advance the offset": the
// file is re-read next scan, so if real records ever arrive the session is
// created from the full transcript with its title intact. A batch that only
// UPDATES an existing session is unaffected — the guard is on creation.
var ErrNoSessionEvidence = errors.New("ingest: batch carries no session evidence")

// File ingests one main transcript .jsonl and any sidechain transcripts under
// its companion dir (<file-without-ext>/subagents/agent-*.jsonl, §1/§7).
// Callers with no projects-root context (the single-file `swarmery ingest`
// subcommand) get an empty origin root — see ingester.originRoot.
func File(db *sql.DB, path string) (Stats, error) { return fileFrom(db, path, "") }

// fileFrom is File with the projects root the transcript was discovered under,
// so a multi-root scan (RebuildText) keeps the account dimension attached.
func fileFrom(db *sql.DB, path, originRoot string) (Stats, error) {
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

	ing := &ingester{tx: tx, stats: &stats, thresholds: DefaultThresholds(), originRoot: originRoot}
	if err := ing.upsertProjectAndSession(recs, fi.ModTime(), false); err != nil {
		if errors.Is(err, ErrNoSessionEvidence) {
			// Nothing to attach records to, and no offset recorded — the file
			// is re-read next scan and ingested in full once it has content.
			return stats, nil
		}
		return stats, err
	}
	if err := ing.processRecords(recs, absPath, false, "", 0, ""); err != nil {
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

	// originRoot is the projects root this transcript was discovered under:
	// ~/.claude/projects, ~/.claude-<account>/projects, … One machine can run
	// several Claude Code subscriptions side by side (CLAUDE_CONFIG_DIR) and
	// the config dir is the ONLY thing that tells their sessions apart — the
	// transcripts themselves carry no account marker. AccountFor turns it into
	// the sessions.account key stamped by upsertProjectAndSession; "" when the
	// caller has no root context (single-file `swarmery ingest`, tests), which
	// leaves the column at its '' default.
	originRoot string

	projectID int64
	sessionID int64
	// sessionUUID is the transcript's sessionId — the stable, cross-restart
	// identity capture_key is built from (sessions.id is local to this
	// database). Set by upsertProjectAndSession; "" when no record carried one.
	sessionUUID string

	// live-pipeline bookkeeping (consumed by the event bus after commit).
	sessionCreated bool
	newEventIDs    []int64
	// Existing event rows whose duration was refined this run (async subagent
	// reconcile). They were already broadcast once with the stale launch
	// roundtrip (~0.1s) — the pipeline re-publishes them so live clients can
	// replace their copies.
	updatedEventIDs []int64

	// agentName tags turns created in the CURRENT processRecords call: the
	// subagent whose sidechain is being processed (analytics phase 2), or ""
	// for the main orchestrator transcript. Set at the top of processRecords.
	agentName string

	// pending tool_use calls awaiting their tool_result (keyed by toolu_… id).
	pending map[string]*pendingTool
	// subagent_start event ids keyed by the Agent tool_use id (meta.json join key).
	subagentStarts map[string]int64

	// turn prose assembly (Chat tab): text blocks per turn in file/line order,
	// batch-local — flushed to turns.text by flushTurnTexts (extend rule).
	turnTexts     map[int64][]string
	turnTextOrder []int64

	// board capture (see capture.go). skipCapture memoizes the per-session
	// "this session never produces cards" verdict so a batch of 500 lines costs
	// at most ONE skip query, not 500. capturedTaskIDs collects the board rows
	// this batch really CHANGED — cards newly inserted, plus cards a completed
	// todo moved to in_review (lifecycle signal 1) — published as task_updated
	// by the pipeline AFTER the transaction commits, so a rolled-back batch
	// emits nothing. Both kinds are the same frame to a client (the row
	// changed, re-read it), which is why they share one list; a replay
	// contributes neither.
	skipCapture     *bool
	capturedTaskIDs []int64
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
	in.sessionUUID = sessionUUID
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

	// Subscription the transcript belongs to (migration 0047), derived from the
	// config dir of the projects root it was found under. "" when this caller
	// has no root context — the column then keeps its '' default and a later
	// tail that DOES know the root fills it in.
	account := AccountFor(in.originRoot)

	// Resolve the session first: mid-file tail batches may carry no cwd, so an
	// existing session must never depend on batch-derived project fields.
	err := in.tx.QueryRow(
		`SELECT id, project_id FROM sessions WHERE session_uuid = ?`,
		sessionUUID).Scan(&in.sessionID, &in.projectID)
	switch {
	case err == sql.ErrNoRows:
		// No timestamp anywhere in the batch means there is no evidence a
		// session ran — refuse to mint one (see ErrNoSessionEvidence). Note
		// this is the CREATE path only: an existing session is still updated
		// by timestamp-less batches below, so a title-only tail of a real
		// session keeps working.
		if firstTS == "" && lastTS == "" {
			return ErrNoSessionEvidence
		}
		// Project keyed by cwd path; slug derived as '/' → '-' (§1); display
		// name is the path base — filled only while NULL so a future rename
		// UI always wins over the derived default.
		if cwd == "" {
			cwd = UnknownProjectPath
		}
		projectID, created, perr := UpsertProject(in.tx, cwd, firstTS, lastTS)
		if perr != nil {
			return perr
		}
		in.projectID = projectID
		if created {
			in.stats.Projects++
		}

		res, err := in.tx.Exec(
			`INSERT INTO sessions (project_id, session_uuid, model, git_branch, cwd, status,
			                       started_at, ended_at, title, source, account)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'jsonl', ?)`,
			in.projectID, sessionUUID, nullStr(model), nullStr(branch), cwd, status,
			firstTS, nullStr(lastTS), nullStr(insertTitle), account)
		if err != nil {
			return fmt.Errorf("insert session: %w", err)
		}
		in.sessionID, _ = res.LastInsertId()
		in.stats.Sessions++
		in.sessionCreated = true
		// A SessionStart hook POST can beat this tail to the punch: apply any
		// terminal identity it parked for this uuid (internal/api
		// hookSessionStart, migration 0068) now that the row finally exists,
		// in the same transaction as the INSERT above.
		if term, ok := popPendingTerminal(sessionUUID); ok {
			if _, err := in.tx.Exec(
				`UPDATE sessions SET term_program = ?, term_focus_url = ?, term_bundle_id = ?, term_tty = ?
				 WHERE id = ?`,
				nullStr(term.Program), nullStr(term.FocusURL), nullStr(term.BundleID), nullStr(term.TTY),
				in.sessionID); err != nil {
				return fmt.Errorf("apply parked terminal identity: %w", err)
			}
		}
	case err != nil:
		return err
	default:
		// Stub heal: a session row minted before its cwd was known (a hook POST
		// that beat the JSONL tail, or a batch with timestamps but no envelope)
		// sits on the '(unknown)' project with empty cwd/started_at. As soon as
		// a batch knows better, backfill — never overwrite good values.
		if cwd != "" {
			var curCwd sql.NullString
			var curPath string
			if err := in.tx.QueryRow(
				`SELECT s.cwd, p.path FROM sessions s
				 JOIN projects p ON p.id = s.project_id WHERE s.id = ?`,
				in.sessionID).Scan(&curCwd, &curPath); err != nil {
				return err
			}
			if curPath == UnknownProjectPath {
				projectID, created, perr := UpsertProject(in.tx, cwd, firstTS, lastTS)
				if perr != nil {
					return perr
				}
				if created {
					in.stats.Projects++
				}
				if _, err := in.tx.Exec(
					`UPDATE sessions SET project_id = ? WHERE id = ?`,
					projectID, in.sessionID); err != nil {
					return err
				}
				in.projectID = projectID
				// The re-point may have orphaned the '(unknown)' placeholder —
				// drop it now rather than leaving a ghost projects row until
				// the next daemon restart (HealStubSessions).
				if err := DropUnknownProjectIfOrphaned(in.tx); err != nil {
					return err
				}
			}
			if !curCwd.Valid || curCwd.String == "" || curCwd.String == UnknownProjectPath {
				if _, err := in.tx.Exec(
					`UPDATE sessions SET cwd = ? WHERE id = ?`, cwd, in.sessionID); err != nil {
					return err
				}
			}
		}
		// Phase 2 (approvals) interplay:
		//   - a session first created by the hooks channel (source='hook') that
		//     now shows up in a JSONL transcript is promoted to source='both';
		//   - status='waiting_approval' is owned exclusively by the approvals
		//     layer (entry AND exit) — the heuristic never overwrites it here;
		//     the approvals resolution/sweeper restores the heuristic status.
		//   - status='killed' is terminal (operator killed the session via
		//     prockill.go, which documents "procwatch/ingest never revert a
		//     'killed' row"). A killed process flushes final JSONL lines after
		//     death; tailing them must NOT resurrect it to a live status.
		// started_at / git_branch only backfill stub rows (empty / NULL) —
		// a mid-file tail batch's first timestamp is NOT the session start, so
		// good values are never overwritten. account backfills the same way
		// (migration 0047): a re-tail through a caller with no root context
		// must never blank an account already stamped, and a session belongs
		// to exactly one subscription for its whole life, so the first root
		// that knew wins.
		if _, err := in.tx.Exec(
			`UPDATE sessions SET model = COALESCE(?, model),
			                     git_branch = COALESCE(git_branch, ?),
			                     started_at = CASE WHEN (started_at IS NULL OR started_at = '') AND ? != ''
			                                       THEN ? ELSE started_at END,
			                     status = CASE WHEN status IN ('waiting_approval','killed')
			                                   THEN status ELSE ? END,
			                     source = CASE WHEN source = 'hook' THEN 'both' ELSE source END,
			                     ended_at = CASE WHEN ? = '' THEN ended_at
			                                     ELSE MAX(COALESCE(ended_at,''), ?) END,
			                     account = CASE WHEN account = '' THEN ? ELSE account END,
			                     title = COALESCE(?, COALESCE(title, ?)) WHERE id = ?`,
			nullStr(model), nullStr(branch), firstTS, firstTS, status, lastTS, lastTS,
			account, nullStr(title), nullStr(firstPrompt), in.sessionID); err != nil {
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
func (in *ingester) processRecords(recs []record, path string, sidechain bool, scope string, parentEventID int64, agentName string) error {
	// Tag turns created in this call with their agent (phase 2). Main
	// transcript passes "" (NULL agent_name = orchestrator).
	in.agentName = agentName
	if in.pending == nil {
		in.pending = map[string]*pendingTool{}
		in.subagentStarts = map[string]int64{}
	}
	if in.turnTexts == nil {
		in.turnTexts = map[int64][]string{}
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
			isPrompt := json.Unmarshal(m.Content, &promptText) == nil
			var blocks []contentBlock
			if !isPrompt {
				// Array content: tool_result carrier (§4b) — or a prompt shipped
				// as text blocks (e.g. prompts with attachments).
				if err := json.Unmarshal(m.Content, &blocks); err != nil {
					log.Printf("warn: %s: unrecognized user content shape, skipping", path)
					in.stats.SkippedLines++
					continue
				}
				carrier := false
				for _, b := range blocks {
					if b.Type != "tool_result" {
						continue
					}
					carrier = true
					if err := in.closeToolCall(r, b, dedup, parentEventID); err != nil {
						return err
					}
				}
				if carrier {
					continue
				}
				var texts []string
				for _, b := range blocks {
					if b.Type == "text" && b.Text != "" {
						texts = append(texts, b.Text)
					}
				}
				if len(texts) == 0 {
					continue // nothing actionable on this user line
				}
				promptText = strings.Join(texts, "\n\n")
			}
			// Real prompt — string content (§4a) or text blocks.
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
			in.addTurnText(turnID, promptText)
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

		case "assistant":
			var m apiMessage
			if err := json.Unmarshal(r.Message, &m); err != nil {
				log.Printf("warn: %s: bad assistant message (%v), skipping", path, err)
				in.stats.SkippedLines++
				continue
			}
			// Assistant turns are recorded for BOTH the main transcript and
			// subagent sidechains (phase 2): in.agentName (set by
			// processRecords) tags sidechain turns; usage is priced the same
			// way regardless. Only turn PROSE (addTurnText below) stays
			// main-only.
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
			var blocks []contentBlock
			if err := json.Unmarshal(m.Content, &blocks); err != nil {
				continue
			}
			for _, b := range blocks {
				if b.Type == "text" && !sidechain {
					// Turn prose (Chat tab): text blocks only — thinking and
					// tool_use are never part of turns.text.
					in.addTurnText(curTurnID, b.Text)
				}
				if b.Type != "tool_use" {
					continue // thinking / text blocks are turn content, not events
				}
				if err := in.openToolCall(r, b, dedup, curTurnID, parentEventID, sidechain); err != nil {
					return err
				}
				// Board capture hook A (capture.go). Placed on the raw block,
				// not on openToolCall's decoded input: that map has been through
				// truncateStrings, and a clipped todo would hash to a different
				// capture_key than the same todo read whole. Returns after one
				// string compare for every tool that is not TodoWrite.
				in.captureTodos(b, sidechain, r.UUID)
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
	return in.flushTurnTexts()
}

// addTurnText buffers one prose block for a turn (batch-local, line order).
func (in *ingester) addTurnText(turnID int64, text string) {
	if turnID == 0 || text == "" {
		return
	}
	if _, ok := in.turnTexts[turnID]; !ok {
		in.turnTextOrder = append(in.turnTextOrder, turnID)
	}
	in.turnTexts[turnID] = append(in.turnTexts[turnID], text)
}

// flushTurnTexts applies this batch's assembled prose to turns.text with the
// EXTEND rule (chosen over rebuild so the tail path needs no full re-read):
//
//   - stored is NULL/empty            → set to the batch text;
//   - batch starts with stored        → replace with the batch text (a full
//     re-read from byte 0: File(), an offset reset, or --rebuild-text always
//     re-assembles the complete join, of which any previous state is a
//     string prefix — so re-ingest converges and is idempotent);
//   - otherwise                       → stored + "\n\n" + batch (incremental
//     tail: the batch carries only NEW trailing lines of the turn).
//
// Text is stored verbatim — never truncated. Crash safety mirrors events:
// the update commits in the same transaction as the file offset, so a tail
// batch can never be applied twice.
func (in *ingester) flushTurnTexts() error {
	for _, id := range in.turnTextOrder {
		batch := strings.Join(in.turnTexts[id], "\n\n")
		if batch == "" {
			continue
		}
		var stored sql.NullString
		if err := in.tx.QueryRow(`SELECT text FROM turns WHERE id = ?`, id).Scan(&stored); err != nil {
			return fmt.Errorf("read turn %d text: %w", id, err)
		}
		next := batch
		if stored.Valid && stored.String != "" && !strings.HasPrefix(batch, stored.String) {
			next = stored.String + "\n\n" + batch
		}
		if stored.Valid && stored.String == next {
			continue
		}
		if _, err := in.tx.Exec(`UPDATE turns SET text = ? WHERE id = ?`, next, id); err != nil {
			return fmt.Errorf("update turn %d text: %w", id, err)
		}
	}
	in.turnTexts = map[int64][]string{}
	in.turnTextOrder = in.turnTextOrder[:0]
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
	// Skill attribution (§9): tool calls issued while a skill is active carry
	// attributionSkill on the assistant line — stored in the input map so it
	// survives closeToolCall's payload rebuild ({input, result}). This is the
	// skill→tool / skill→subagent edge of the session call tree. Attribution,
	// not strict causality: there is no "skill ended" marker, so the tag can
	// outlive the skill's actual work.
	if r.AttributionSkill != "" {
		input["attributionSkill"] = r.AttributionSkill
	}

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
		// ingest refines duration_ms later (reconcileSubagentDuration).
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
		// Reconcile whenever the result could not date the run: the async marker,
		// or a result that reported no duration at all. Without the second
		// condition a call whose toolUseResult was empty keeps the launch
		// roundtrip forever, and every per-agent duration statistic built on it
		// describes the spawn instead of the work.
		if async || ar.TotalDurationMs <= 0 {
			return in.reconcileSubagentDuration(p.eventID, "")
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

	// Bash test-runner calls also emit a test_run event with parsed counts
	// (the only source of the "Quality" aggregate). Best-effort — counts are 0
	// when the runner's summary line can't be parsed; the run's ok/error status
	// mirrors the Bash exit.
	if p.name == "Bash" {
		if cmd, _ := p.input["command"].(string); isTestCommand(cmd) {
			passed, failed, skipped, parsed := parseTestCounts(testResultText(r.ToolUseResult))
			if _, _, err := in.insertEvent(eventRow{
				ts: r.Timestamp, typ: "test_run", toolName: "Bash",
				parentEventID: p.eventID, status: status, durationMs: durationMs,
				payload: map[string]any{
					"framework": testFramework(cmd),
					"passed":    passed, "failed": failed, "skipped": skipped,
					"parsed": parsed, "command": truncate(cmd, 200),
				},
				dedup: dedup + "#testrun#" + b.ToolUseID,
			}); err != nil {
				return err
			}
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

// agentNameFor resolves the agent that owns a sidechain's turns (phase 2): the
// parent subagent_start's subagent_type — the SAME value analytics folds run
// counts by, so $ and counts share a key — falling back to the meta.json
// agentType. Returns "" when neither is available; the turns are still
// recorded, just left unattributed.
func (in *ingester) agentNameFor(parentEventID int64, fallback string) string {
	if parentEventID != 0 {
		var s sql.NullString
		if err := in.tx.QueryRow(
			`SELECT json_extract(payload, '$.subagent_type') FROM events WHERE id = ?`,
			parentEventID).Scan(&s); err == nil && s.Valid && s.String != "" {
			return s.String
		}
	}
	return fallback
}

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
	if err := in.processRecords(recs, path, true, scope, parentID, in.agentNameFor(parentID, meta.AgentType)); err != nil {
		return err
	}
	if parentID != 0 {
		if err := in.adoptOrphanSidechainEvents(scope, parentID); err != nil {
			return err
		}
		if err := in.reconcileSubagentDuration(parentID, lastRecordTS(recs)); err != nil {
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

// reconcileSubagentDuration fixes the duration of an Agent call whose tool_result
// could not tell us how long it took.
//
// TWO cases land here, and they look identical from the row's side:
//
//  1. A background (run_in_background) launch. Its tool_result arrives
//     ~immediately with status "async_launched" and no totalDurationMs, so the
//     start/stop rows are closed with the launch roundtrip (~0.1s) while the
//     sidechain runs for minutes.
//  2. A call whose toolUseResult carried NO usable fields at all — no status, no
//     agentId, no totalDurationMs. The same launch roundtrip is kept, for the
//     same wrong reason, and nothing ever corrected it.
//
// Case 2 is not hypothetical and it is not harmless: it is why one retrospective
// read "verification-agent p95 47s" and "test-writer p95 1s at $0.00" and
// concluded the fleet's verifiers were dying in seconds. Six of seven
// verification runs in that window had 8–24 parented sidechain events spanning
// two to three MINUTES, and each was recorded as ~1.8 seconds. The agent was
// working; the measurement was not.
//
// The real duration is the span subagent_start.ts → last sidechain record
// timestamp (lastTS; when empty the latest stored child event ts is used
// instead). Monotonic and idempotent: the duration only ever GROWS towards the
// sidechain's true end, so live-tail batches refine it, re-ingest converges to
// the same value, and a genuine short run can never be inflated.
func (in *ingester) reconcileSubagentDuration(parentID int64, lastTS string) error {
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
	// Matches BOTH cases in the doc comment: the explicit async marker, and a
	// stop row whose status is absent or empty — the "the result told us nothing"
	// shape. A stop row carrying a real status ("completed") is left alone: there
	// the tool_result did report, and its number is the authoritative one.
	err := in.tx.QueryRow(
		`SELECT stop.id, start.ts FROM events stop
		 JOIN events start ON start.id = stop.parent_event_id
		 WHERE stop.parent_event_id = ? AND stop.type = 'subagent_stop'
		   AND COALESCE(json_extract(stop.payload, '$.status'), '') IN ('async_launched', '')`,
		parentID).Scan(&stopID, &startTS)
	if err == sql.ErrNoRows {
		return nil // the result reported a real status — nothing to fix
	}
	if err != nil {
		return err
	}
	d := parseTS(lastTS).Sub(parseTS(startTS)).Milliseconds()
	if d <= 0 {
		return nil
	}
	res, err := in.tx.Exec(
		`UPDATE events SET duration_ms = ?
		 WHERE id IN (?, ?) AND (duration_ms IS NULL OR duration_ms < ?)`,
		d, parentID, stopID, d)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		// Both rows were already pushed over WS with the launch roundtrip
		// duration — mark them for re-broadcast so open dashboards don't keep
		// showing "· 0.1s" agent pills until a reload.
		in.updatedEventIDs = append(in.updatedEventIDs, parentID, stopID)
	}
	return nil
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
		                    tokens_in, tokens_out, tokens_cache_read, tokens_cache_write, agent_name)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.sessionID, seq, role, nullStr(messageID), nullStr(model), ts, ts, tin, tout, tcr, tcw, nullStr(in.agentName))
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

// ── project registry (shared with the approvals hook path) ──────────────────

// projectNameFor derives the default display name of a project from its cwd
// path: the last path element ("/home/dev/swarmery" → "swarmery").
func projectNameFor(path string) string {
	return filepath.Base(path)
}

// SlugForPath derives the project slug from its cwd path: '/' → '-' (§1).
func SlugForPath(path string) string {
	return strings.ReplaceAll(path, "/", "-")
}

// dbtx is the common surface of *sql.DB and *sql.Tx the project upsert needs.
type dbtx interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
	Query(query string, args ...any) (*sql.Rows, error)
}

// worktreeRootOverride pins the dispatcher worktree base in tests; empty
// means <home>/.swarmery/worktrees (mirrors internal/worktree.DefaultRoot,
// which this package must not import — worktree imports store).
var worktreeRootOverride string

func worktreeBase() string {
	if worktreeRootOverride != "" {
		return worktreeRootOverride
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".swarmery", "worktrees")
}

// systemBaseOverride pins the System project dir in tests; empty means
// <home>/.swarmery. Daemon-spawned headless claude runs set this as their
// cwd, and legacy cwd "/" (runs that inherited the launchd cwd before that)
// canonicalizes here too, so system telemetry lives on one deliberate
// "System" project row instead of a mystery "/" project.
var systemBaseOverride string

func systemBase() string {
	if systemBaseOverride != "" {
		return systemBaseOverride
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".swarmery")
}

// SystemDir exposes the System project dir to other packages (the API layer
// flags that projects row isSystem so the dashboard can demote it). Empty
// only when the home dir is unresolvable.
func SystemDir() string { return systemBase() }

// onboardRootsOverride holds the daemon's onboarding roots (SWARMERY_ONBOARD_ROOTS,
// cleaned) so the ancestor rule can refuse them as attribution targets. An
// onboarding root is a PARENT directory holding many unrelated repos, not a
// project — but one session that once ran at that exact cwd is enough to mint
// a projects row for it, and from then on "deepest registered ancestor wins"
// folds every sibling repo under it into that one catch-all row (and the heal
// then deletes their real rows). Empty means the daemon was started without
// onboarding roots, which is the pre-existing behaviour.
var onboardRootsOverride []string

// SetOnboardRoots publishes the onboarding allow-list to the attribution rules;
// call once at startup, BEFORE the ingest pipeline starts (its first Backfill
// runs HealProjectAttribution, which must already see the roots). Mirrors
// api.AttachOnboard, which fences the onboarding endpoint with the same list.
func SetOnboardRoots(roots []string) {
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		if r = strings.TrimSpace(r); r != "" {
			out = append(out, filepath.Clean(r))
		}
	}
	onboardRootsOverride = out
}

// CanonicalProjectPath maps a raw session cwd to the path of the registered
// project it belongs to, so satellite cwds never mint phantom project rows:
//   - a dispatcher worktree <root>/<parentSlug>/<task> resolves to the
//     project whose slug is the first segment under the worktree root;
//   - a subdirectory of a registered project resolves to that project
//     (deepest registered ancestor wins), skipping archived rows and
//     configured onboarding roots — see the ancestor query below.
//
// Unknown paths return unchanged — attribution never invents projects
// (except cwd "/", which maps to the System dir; UpsertProject mints that
// row on first sight).
func CanonicalProjectPath(q dbtx, path string) string {
	if path == "/" {
		if s := systemBase(); s != "" {
			return s
		}
	}
	if root := worktreeBase(); root != "" {
		if rel, ok := strings.CutPrefix(path, root+"/"); ok {
			seg, _, _ := strings.Cut(rel, "/")
			var parent string
			if err := q.QueryRow(`SELECT path FROM projects WHERE slug = ?`, seg).Scan(&parent); err == nil && parent != "" {
				return parent
			}
		}
	}
	// Deepest registered ancestor wins, with two rows barred from ever being
	// the target:
	//   - archived = 1: archiving a row is the operator saying "stop using
	//     this project"; without the filter an archived trap row keeps
	//     swallowing new cwds and archiving cannot defuse it;
	//   - an onboarding root: a parent dir of many repos, never a project in
	//     the attribution sense (see onboardRootsOverride).
	// Both bans are ancestor-rule-only — UpsertProject's exact-path lookup
	// still resolves an archived project or an onboarding root that really
	// did host a session, so no existing row loses its own sessions.
	query := `SELECT path FROM projects WHERE ? LIKE path || '/%' AND archived = 0`
	args := []any{path}
	if len(onboardRootsOverride) > 0 {
		query += ` AND path NOT IN (?` + strings.Repeat(`, ?`, len(onboardRootsOverride)-1) + `)`
		for _, r := range onboardRootsOverride {
			args = append(args, r)
		}
	}
	query += ` ORDER BY LENGTH(path) DESC LIMIT 1`
	var ancestor string
	if err := q.QueryRow(query, args...).Scan(&ancestor); err == nil && ancestor != "" {
		return ancestor
	}
	return path
}

// UpsertProject resolves or creates the projects row for a cwd path with the
// canonical derivation (slug '/'→'-', display name = path base, name filled
// only while NULL so a future rename UI always wins). It is THE single place
// project rows are minted — the JSONL ingester, the approvals hook path, and
// the stub heal all share it so every channel attributes identically.
func UpsertProject(q dbtx, path, firstSeen, lastActivity string) (id int64, created bool, err error) {
	err = q.QueryRow(`SELECT id FROM projects WHERE path = ?`, path).Scan(&id)
	switch {
	case err == sql.ErrNoRows:
		// Not registered under this exact path — attribute satellite cwds
		// (dispatcher worktrees, in-repo subdirectories, cwd "/") to their
		// canonical project instead of minting a phantom row.
		if canon := CanonicalProjectPath(q, path); canon != path {
			if err := q.QueryRow(`SELECT id FROM projects WHERE path = ?`, canon).Scan(&id); err == nil {
				return id, false, nil
			}
			// Only the System dir can canonicalize to an unregistered path
			// (worktree/subdir rules select from projects) — mint it.
			path = canon
		}
		name := projectNameFor(path)
		if path == systemBase() {
			name = "System"
		}
		res, ierr := q.Exec(
			`INSERT INTO projects (path, slug, name, first_seen, last_activity) VALUES (?, ?, ?, ?, ?)`,
			path, SlugForPath(path), name, firstSeen, lastActivity)
		if ierr != nil {
			return 0, false, fmt.Errorf("insert project: %w", ierr)
		}
		id, _ = res.LastInsertId()
		return id, true, nil
	case err != nil:
		return 0, false, err
	}
	// Pre-existing row (older ingests left name NULL) — heal in place.
	if _, err := q.Exec(
		`UPDATE projects SET name = ? WHERE id = ? AND name IS NULL`,
		projectNameFor(path), id); err != nil {
		return 0, false, err
	}
	return id, false, nil
}

// HealProjectNames backfills projects.name = base(path) for rows where the
// name is still NULL (rows written before names existed). Non-NULL names are
// NEVER overwritten — a user rename must always win over the derived default.
// Called from every Backfill pass, so existing databases heal on the first
// daemon restart (or `swarmery backfill`) after upgrading.
func HealProjectNames(db *sql.DB) (int, error) {
	rows, err := db.Query(`SELECT id, path FROM projects WHERE name IS NULL`)
	if err != nil {
		return 0, err
	}
	type row struct {
		id   int64
		path string
	}
	var todo []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.path); err != nil {
			rows.Close()
			return 0, err
		}
		todo = append(todo, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, r := range todo {
		if _, err := db.Exec(
			`UPDATE projects SET name = ? WHERE id = ? AND name IS NULL`,
			projectNameFor(r.path), r.id); err != nil {
			return 0, err
		}
	}
	return len(todo), nil
}

// HealProjectAttribution merges phantom project rows (satellite cwds minted
// before canonicalization existed — dispatcher worktrees, in-repo
// subdirectories) into the project their path canonicalizes to: every
// project_id reference in every table is re-pointed, then the phantom row is
// dropped. Two markers make a row a real project that is never folded away:
// a linked workspaces row (real projects carry workspaces, satellites don't)
// and pinned = 1 (the operator's explicit "never merge this row away" — the
// escape hatch when a rule would otherwise swallow a row the operator knows
// is its own project). Returns the number of phantoms merged. Called from
// every Backfill pass, so existing databases heal on the first daemon restart
// after upgrading.
func HealProjectAttribution(db *sql.DB) (int, error) {
	rows, err := db.Query(`SELECT id, path, pinned FROM projects`)
	if err != nil {
		return 0, err
	}
	type row struct {
		id     int64
		path   string
		pinned int
	}
	var projs []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.path, &r.pinned); err != nil {
			rows.Close()
			return 0, err
		}
		projs = append(projs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	tables, err := projectRefTables(db)
	if err != nil {
		return 0, err
	}

	// Upgrade the System row's derived default name (a pre-existing row
	// minted from the raw path reads ".swarmery"); a user-set name is never
	// touched — only the exact derived default is replaced.
	if s := systemBase(); s != "" {
		if _, err := db.Exec(
			`UPDATE projects SET name = 'System' WHERE path = ? AND name = ?`,
			s, projectNameFor(s)); err != nil {
			return 0, err
		}
	}

	moved := 0
	for _, p := range projs {
		canon := CanonicalProjectPath(db, p.path)
		if canon == p.path {
			continue
		}
		// Resolve-or-mint the canonical target (mint only happens for the
		// System dir — see UpsertProject).
		target, _, err := UpsertProject(db, canon, "", "")
		if err != nil || target == p.id {
			continue
		}
		if p.pinned != 0 {
			continue // pinned → the operator declared it a real project
		}
		var linked int
		if err := db.QueryRow(`SELECT COUNT(*) FROM workspaces WHERE project_id = ?`, p.id).Scan(&linked); err == nil && linked > 0 {
			continue // workspace-linked → a real project, not a phantom
		}
		tx, err := db.Begin()
		if err != nil {
			return moved, err
		}
		ok := true
		for _, tbl := range tables {
			// OR IGNORE + sweep: rows that would collide with the target's
			// (e.g. a PRIMARY KEY on project_id) are dropped with the phantom.
			if _, err := tx.Exec(`UPDATE OR IGNORE `+tbl+` SET project_id = ? WHERE project_id = ?`, target, p.id); err != nil {
				ok = false
				break
			}
			if _, err := tx.Exec(`DELETE FROM `+tbl+` WHERE project_id = ?`, p.id); err != nil {
				ok = false
				break
			}
		}
		if ok {
			if _, err := tx.Exec(`DELETE FROM projects WHERE id = ?`, p.id); err != nil {
				ok = false
			}
		}
		if !ok {
			tx.Rollback()
			continue
		}
		if err := tx.Commit(); err != nil {
			return moved, err
		}
		log.Printf("heal: merged phantom project %q into %q", p.path, canon)
		moved++
	}
	return moved, nil
}

// projectRefTables lists user tables carrying a project_id column (the
// projects table itself excluded).
func projectRefTables(db *sql.DB) ([]string, error) {
	rows, err := db.Query(
		`SELECT name FROM sqlite_master
		 WHERE type = 'table' AND name != 'projects' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, err
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		names = append(names, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []string
	for _, n := range names {
		cols, err := db.Query(`SELECT name FROM pragma_table_info(?)`, n)
		if err != nil {
			return nil, err
		}
		for cols.Next() {
			var c string
			if err := cols.Scan(&c); err != nil {
				cols.Close()
				return nil, err
			}
			if c == "project_id" {
				out = append(out, n)
				break
			}
		}
		cols.Close()
		if err := cols.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
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
