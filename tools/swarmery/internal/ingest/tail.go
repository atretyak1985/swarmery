package ingest

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// TailResult reports what one incremental tail pass did to a single file.
type TailResult struct {
	Path           string
	SessionID      int64 // sessions.id (0 if nothing was ingested)
	SessionCreated bool
	Lines          int     // parseable records processed this pass
	SkippedLines   int     // malformed lines skipped this pass
	NewEventIDs    []int64 // events created this pass, in insert order
	// Existing events whose duration was refined this pass (async subagent
	// reconcile) — re-published so live clients replace their stale copies.
	UpdatedEventIDs []int64
	StartOffset     int64  // byte offset the pass started reading from
	NextOffset      int64  // byte offset persisted after the pass
	Reset           bool   // offset was reset to 0 (recreated/truncated file)
	LastTS          string // timestamp of the last ingested record (lag metric)
}

// Q11 (docs/jsonl-format.md): a live watch experiment confirmed transcripts
// are strictly append-only — sizes only grow, the byte prefix never changes,
// inodes are stable. Tail-follow from persisted byte offsets is therefore
// safe. The inode and size-shrink resets below are defensive guards for file
// recreation (e.g. deletion + re-run), not an observed steady-state behavior.

// TailFile incrementally ingests one transcript from its persisted byte
// offset. Crash-safety contract: the offset row is updated in the SAME
// transaction as the ingested rows, so a crash between read and commit
// re-reads the batch and the dedup_key scheme absorbs the replay.
//
// Unlike File it never touches sidechain companions (they are independent
// tail targets) and an empty/partial-line-only file is a successful no-op.
func TailFile(db *sql.DB, path string, th Thresholds) (TailResult, error) {
	var res TailResult

	absPath, err := filepath.Abs(path)
	if err != nil {
		return res, err
	}
	res.Path = absPath

	fi, err := os.Stat(absPath)
	if err != nil {
		return res, err
	}
	curIno := inodeOf(fi)

	// Resolve the starting offset from file_offsets.
	var start int64
	var storedIno sql.NullInt64
	err = db.QueryRow(
		`SELECT byte_offset, inode FROM file_offsets WHERE file_path = ?`,
		absPath).Scan(&start, &storedIno)
	switch {
	case err == sql.ErrNoRows:
		start = 0
	case err != nil:
		return res, err
	default:
		// inode changed → file recreated; size below offset → truncated/rewritten.
		// Either way: reset to 0 and re-read; dedup prevents duplicates (Q11).
		if (storedIno.Valid && curIno != 0 && storedIno.Int64 != curIno) || fi.Size() < start {
			start = 0
			res.Reset = true
		}
	}
	res.StartOffset, res.NextOffset = start, start
	if fi.Size() == start {
		return res, nil // nothing new
	}

	var stats Stats
	recs, consumed, err := readRecordsFrom(absPath, start, &stats)
	if err != nil {
		return res, err
	}
	res.SkippedLines = stats.SkippedLines
	if consumed == 0 {
		return res, nil // only a partial (not yet newline-terminated) line — retry later
	}

	tx, err := db.Begin()
	if err != nil {
		return res, err
	}
	defer tx.Rollback()

	ing := &ingester{tx: tx, stats: &stats, thresholds: th}
	if len(recs) > 0 {
		sidechain, scope, parentEventID, agentType := sidechainContext(tx, absPath, recs)
		if err := ing.upsertProjectAndSession(recs, fi.ModTime(), sidechain); err != nil {
			return res, err
		}
		agentName := ""
		if sidechain {
			agentName = ing.agentNameFor(parentEventID, agentType)
		}
		if err := ing.processRecords(recs, absPath, sidechain, scope, parentEventID, agentName); err != nil {
			return res, err
		}
		if sidechain && parentEventID != 0 {
			// Earlier batches of this sidechain may have been ingested before
			// the parent subagent_start existed — adopt those orphans, then
			// refine the duration of background (async) agents from the
			// batch's last record timestamp.
			if err := ing.adoptOrphanSidechainEvents(scope, parentEventID); err != nil {
				return res, err
			}
			if err := ing.reconcileAsyncSubagent(parentEventID, lastRecordTS(recs)); err != nil {
				return res, err
			}
		}
	}
	// Advance the offset even for batches of only-malformed lines: they will
	// never parse and must not be re-read every pass.
	if err := ing.recordOffset(absPath, start+consumed); err != nil {
		return res, err
	}
	if err := tx.Commit(); err != nil {
		return res, err
	}

	res.SessionID = ing.sessionID
	res.SessionCreated = ing.sessionCreated
	res.Lines = len(recs)
	res.NewEventIDs = ing.newEventIDs
	res.UpdatedEventIDs = ing.updatedEventIDs
	res.NextOffset = start + consumed
	for i := len(recs) - 1; i >= 0; i-- {
		if recs[i].Timestamp != "" {
			res.LastTS = recs[i].Timestamp
			break
		}
	}
	return res, nil
}

// readRecordsFrom reads complete ('\n'-terminated) lines starting at byte
// offset. A trailing partial line is NOT consumed — the writer is mid-append;
// the next pass picks it up once terminated. Malformed complete lines are
// warned, counted, and consumed.
func readRecordsFrom(path string, offset int64, stats *Stats) ([]record, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return nil, 0, err
		}
	}

	var recs []record
	var consumed int64
	br := bufio.NewReaderSize(f, 1<<20)
	lineNo := 0
	for {
		line, err := br.ReadBytes('\n')
		if err == io.EOF {
			// len(line) > 0 ⇒ partial trailing line — leave it unconsumed.
			break
		}
		if err != nil {
			return recs, consumed, fmt.Errorf("read %s: %w", path, err)
		}
		lineNo++
		consumed += int64(len(line))
		trimmed := strings.TrimSpace(string(line))
		if trimmed == "" {
			continue
		}
		var r record
		if err := json.Unmarshal([]byte(trimmed), &r); err != nil {
			log.Printf("warn: %s(+%d):%d: skipping malformed line: %v", path, offset, lineNo, err)
			stats.SkippedLines++
			continue
		}
		r.raw = []byte(trimmed)
		recs = append(recs, r)
	}
	return recs, consumed, nil
}

// sidechainContext detects a subagents/agent-*.jsonl transcript and resolves
// its dedup scope + parent subagent_start event (via meta.json toolUseId, §7).
func sidechainContext(tx *sql.Tx, path string, recs []record) (sidechain bool, scope string, parentEventID int64, agentType string) {
	base := filepath.Base(path)
	if filepath.Base(filepath.Dir(path)) != "subagents" || !strings.HasPrefix(base, "agent-") {
		return false, "", 0, ""
	}
	scope = recs[0].AgentID
	if scope == "" {
		scope = base
	}
	metaPath := strings.TrimSuffix(path, ".jsonl") + ".meta.json"
	var meta sidechainMeta
	if raw, err := os.ReadFile(metaPath); err == nil {
		_ = json.Unmarshal(raw, &meta)
	}
	// meta.AgentType is available whenever the sidechain file exists, so it
	// backstops phase-2 agent attribution during the live-tail race where the
	// parent subagent_start row does not exist yet.
	agentType = meta.AgentType
	if meta.ToolUseID != "" && recs[0].SessionID != "" {
		_ = tx.QueryRow(
			`SELECT e.id FROM events e JOIN sessions s ON s.id = e.session_id
			 WHERE s.session_uuid = ? AND e.type = 'subagent_start'
			   AND json_extract(e.payload, '$.tool_use_id') = ?`,
			recs[0].SessionID, meta.ToolUseID).Scan(&parentEventID)
	}
	return true, scope, parentEventID, agentType
}

// inodeOf extracts the inode number (0 when unavailable).
func inodeOf(fi os.FileInfo) int64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int64(st.Ino)
	}
	return 0
}

// lagFrom returns the delay between a record timestamp and now — used for
// observed-lag logging in the pipeline.
func lagFrom(ts string, now time.Time) time.Duration {
	t := parseTS(ts)
	if t.IsZero() {
		return 0
	}
	return now.Sub(t)
}
