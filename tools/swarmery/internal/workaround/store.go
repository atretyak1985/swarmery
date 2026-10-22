package workaround

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// SessionCap is how many hits per session keep their text. Beyond it a hit is
// stored as its key only (empty sentence), so the count stays exact.
const SessionCap = 50

// Execer is what the writers go through: a *sql.DB, or the *sql.Tx a caller
// already holds — the store runs one connection, so the ingest tail must write
// its hits through its own transaction (the findings.Execer pattern).
type Execer interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
}

// Link is where one chain's hits belong.
type Link struct {
	// Linked: the session serves a phase or a plan, so its prose is scanned.
	Linked bool
	// PhaseID is the epic_phases row the chain worked on; 0 when the session is
	// linked but this chain is not attributable to one phase.
	PhaseID int64
}

// phaseLabel reads the phase number out of a subagent's dispatch description —
// run-plan dispatches executors as "Phase 4 return to agent", "Phase 5a …".
var phaseLabel = regexp.MustCompile(`(?i)\bphase\s+(\d+)`)

// IsReviewer reports whether a subagent reviews rather than builds. A reviewer
// quoting "bypass" from the doc is not the agent changing course, so its prose
// is never scanned.
func IsReviewer(agentName string) bool {
	return strings.Contains(strings.ToLower(agentName), "reviewer")
}

// ResolveLink decides whether a chain of sessionUUID is phase-linked and to
// which phase:
//
//  1. a daemon phase run — epic_phases.run_session_uuid is the session — owns
//     every chain of the session;
//  2. otherwise a task_sessions link makes the session linked; a subagent
//     whose description names "Phase N" is attributed to phase N of the linked
//     plan, and a board card a phase activated maps to that phase;
//  3. otherwise the session is not linked and nothing is scanned.
//
// agentLabel is the subagent's dispatch description ("" for the main chain).
func ResolveLink(q Execer, sessionUUID string, sessionID int64, agentLabel string) (Link, error) {
	var phaseID int64
	err := q.QueryRow(`SELECT id FROM epic_phases WHERE run_session_uuid = ? ORDER BY id DESC LIMIT 1`,
		sessionUUID).Scan(&phaseID)
	if err == nil {
		return Link{Linked: true, PhaseID: phaseID}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Link{}, err
	}
	if sessionID == 0 {
		return Link{}, nil
	}
	var linked int
	if err := q.QueryRow(`SELECT COUNT(*) FROM task_sessions WHERE session_id = ?`, sessionID).Scan(&linked); err != nil {
		return Link{}, err
	}
	if linked == 0 {
		return Link{}, nil
	}
	l := Link{Linked: true}
	if m := phaseLabel.FindStringSubmatch(agentLabel); m != nil {
		seq, _ := strconv.Atoi(m[1])
		err := q.QueryRow(`
			SELECT ep.id FROM task_sessions ts
			  JOIN epic_phases ep ON ep.workspace_task_id = ts.task_id
			 WHERE ts.session_id = ? AND ep.seq = ?
			 ORDER BY ts.link_source = 'explicit' DESC, ts.confidence DESC, ep.id DESC LIMIT 1`,
			sessionID, seq).Scan(&l.PhaseID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return Link{}, err
		}
		if l.PhaseID != 0 {
			return l, nil
		}
	}
	err = q.QueryRow(`
		SELECT ep.id FROM task_sessions ts
		  JOIN epic_phases ep ON ep.activated_board_task_id = ts.task_id
		 WHERE ts.session_id = ? ORDER BY ep.id DESC LIMIT 1`, sessionID).Scan(&l.PhaseID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Link{}, err
	}
	return l, nil
}

// Row is one hit as stored.
type Row struct {
	SessionUUID string
	TurnID      int64
	PhaseID     int64 // 0 → NULL
	AgentName   string
	Hit
}

// Insert stores hits idempotently: a (turn, word, sentence) already stored is
// skipped. Past SessionCap text-bearing rows for the session, a hit keeps its
// key only. Returns how many rows were new.
func Insert(db Execer, rows []Row, now time.Time) (int, error) {
	added := 0
	for _, r := range rows {
		hash := sentenceHash(r.Sentence)
		var exists int
		if err := db.QueryRow(`SELECT COUNT(*) FROM workaround_hits WHERE turn_id = ? AND word = ? AND sentence_hash = ?`,
			r.TurnID, r.Word, hash).Scan(&exists); err != nil {
			return added, err
		}
		if exists > 0 {
			continue
		}
		var kept int
		if err := db.QueryRow(`SELECT COUNT(*) FROM workaround_hits WHERE session_uuid = ? AND sentence <> ''`,
			r.SessionUUID).Scan(&kept); err != nil {
			return added, err
		}
		sentence, before, after := r.Sentence, r.Before, r.After
		if kept >= SessionCap {
			sentence, before, after = "", "", ""
		}
		res, err := db.Exec(`
			INSERT OR IGNORE INTO workaround_hits
			  (session_uuid, turn_id, phase_id, agent_name, word, sentence, sentence_hash,
			   context_before, context_after, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.SessionUUID, r.TurnID, nullID(r.PhaseID), nullStr(r.AgentName), r.Word,
			sentence, hash, before, after, now.UTC().Format(time.RFC3339))
		if err != nil {
			return added, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			added++
		}
	}
	return added, nil
}

// Stored is a hit as the API and the reviewer read it.
type Stored struct {
	ID          int64  `json:"id"`
	SessionUUID string `json:"sessionUuid"`
	TurnSeq     int    `json:"turnSeq"`
	TurnAt      string `json:"turnAt"`
	AgentName   string `json:"agentName"`
	Word        string `json:"word"`
	Sentence    string `json:"sentence"`
	Before      string `json:"before"`
	After       string `json:"after"`
}

// PhaseHits is a phase's text-bearing hits in transcript order, and the total
// including the key-only ones past the cap.
type PhaseHits struct {
	Hits  []Stored `json:"hits"`
	Total int      `json:"total"`
}

// Querier is a read handle (*sql.DB or *sql.Tx).
type Querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

const selectStored = `
	SELECT h.id, h.session_uuid, COALESCE(t.seq, 0), COALESCE(t.started_at, ''), COALESCE(h.agent_name, ''),
	       h.word, h.sentence, h.context_before, h.context_after, h.phase_id
	  FROM workaround_hits h LEFT JOIN turns t ON t.id = h.turn_id`

// ForPhase returns one phase's hits; sessionUUID narrows them to one run ("" =
// every session that worked on the phase).
func ForPhase(db Querier, phaseID int64, sessionUUID string) (PhaseHits, error) {
	q := selectStored + ` WHERE h.phase_id = ?`
	args := []any{phaseID}
	if sessionUUID != "" {
		q += ` AND h.session_uuid = ?`
		args = append(args, sessionUUID)
	}
	byPhase, err := collect(db, q+` ORDER BY t.started_at, t.seq, h.id`, args...)
	if err != nil {
		return PhaseHits{Hits: []Stored{}}, err
	}
	if out, ok := byPhase[phaseID]; ok {
		return out, nil
	}
	return PhaseHits{Hits: []Stored{}}, nil
}

// ForTask returns every phase's hits of one plan in one query, keyed by phase
// id — the epic payload's batch read.
func ForTask(db Querier, taskID int64) (map[int64]PhaseHits, error) {
	return collect(db, selectStored+`
		 JOIN epic_phases ep ON ep.id = h.phase_id
		WHERE ep.workspace_task_id = ?
		ORDER BY t.started_at, t.seq, h.id`, taskID)
}

func collect(db Querier, q string, args ...any) (map[int64]PhaseHits, error) {
	rows, err := db.Query(q, args...)
	if err != nil {
		return map[int64]PhaseHits{}, err
	}
	defer rows.Close()
	out := map[int64]PhaseHits{}
	for rows.Next() {
		var (
			s       Stored
			phaseID sql.NullInt64
		)
		if err := rows.Scan(&s.ID, &s.SessionUUID, &s.TurnSeq, &s.TurnAt, &s.AgentName,
			&s.Word, &s.Sentence, &s.Before, &s.After, &phaseID); err != nil {
			return map[int64]PhaseHits{}, err
		}
		p := out[phaseID.Int64]
		p.Total++
		if s.Sentence != "" {
			p.Hits = append(p.Hits, s)
		}
		if p.Hits == nil {
			p.Hits = []Stored{}
		}
		out[phaseID.Int64] = p
	}
	return out, rows.Err()
}

// sentenceHash keys a hit's sentence: short, and identical for a replay.
func sentenceHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
