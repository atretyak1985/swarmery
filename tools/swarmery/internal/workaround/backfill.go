package workaround

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// HandbackTool is the tool a subagent delivers its final report through; its
// input.message is the subagent's final message — assistant prose, scanned.
const HandbackTool = "SubagentHandback"

// HandbackReport is a SubagentHandback tool_use's report, "" for any other tool.
func HandbackReport(name string, input json.RawMessage) string {
	if name != HandbackTool || len(input) == 0 {
		return ""
	}
	var in struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(input, &in) != nil {
		return ""
	}
	return in.Message
}

// ScanSession scans one ingested session after the fact — what the tail hook
// would have stored had the session been phase-linked when its lines arrived:
// the main chain's assistant prose from turns.text, and every subagent
// transcript next to the main one on disk (sidechain prose is never stored, so
// it is read from the JSONL; reviewer subagents are skipped). Idempotent: the
// (turn, word, sentence) key absorbs a re-run. Returns the rows added.
func ScanSession(db *sql.DB, m *Matcher, sessionUUID string, now time.Time) (int, error) {
	if m.Empty() {
		return 0, nil
	}
	var sessionID int64
	err := db.QueryRow(`SELECT id FROM sessions WHERE session_uuid = ?`, sessionUUID).Scan(&sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var rows []Row

	main, err := ResolveLink(db, sessionUUID, sessionID, "")
	if err != nil {
		return 0, err
	}
	if main.Linked {
		r, err := scanMainChain(db, m, sessionUUID, sessionID, main.PhaseID)
		if err != nil {
			return 0, err
		}
		rows = append(rows, r...)
	}
	r, err := scanSidechains(db, m, sessionUUID, sessionID)
	if err != nil {
		return 0, err
	}
	rows = append(rows, r...)
	if len(rows) == 0 {
		return 0, nil
	}

	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	added, err := Insert(tx, rows, now)
	if err != nil {
		return 0, err
	}
	return added, tx.Commit()
}

func scanMainChain(db *sql.DB, m *Matcher, sessionUUID string, sessionID, phaseID int64) ([]Row, error) {
	q, err := db.Query(`SELECT id, seq, text FROM turns
		WHERE session_id = ? AND role = 'assistant' AND agent_name IS NULL AND COALESCE(text, '') <> ''
		ORDER BY seq`, sessionID)
	if err != nil {
		return nil, err
	}
	defer q.Close()
	var rows []Row
	for q.Next() {
		var (
			turnID int64
			seq    int
			text   string
		)
		if err := q.Scan(&turnID, &seq, &text); err != nil {
			return nil, err
		}
		for _, h := range m.Scan(text) {
			h.TurnSeq = seq
			rows = append(rows, Row{SessionUUID: sessionUUID, TurnID: turnID, PhaseID: phaseID, Hit: h})
		}
	}
	return rows, q.Err()
}

// sidechainMeta is agent-<id>.meta.json.
type sidechainMeta struct {
	AgentType   string `json:"agentType"`
	Description string `json:"description"`
}

// transcriptRecord is the slice of a JSONL line the scan needs.
type transcriptRecord struct {
	Type    string `json:"type"`
	Message struct {
		ID      string          `json:"id"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type block struct {
	Type  string          `json:"type"`
	Name  string          `json:"name"`
	Text  string          `json:"text"`
	Input json.RawMessage `json:"input"`
}

// scanSidechains reads <main-without-.jsonl>/subagents/agent-*.jsonl. The main
// transcript's path is the one the tail recorded in file_offsets.
func scanSidechains(db *sql.DB, m *Matcher, sessionUUID string, sessionID int64) ([]Row, error) {
	var mainPath string
	err := db.QueryRow(`SELECT file_path FROM file_offsets WHERE file_path LIKE ? ORDER BY file_path LIMIT 1`,
		"%/"+sessionUUID+".jsonl").Scan(&mainPath)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	files, _ := filepath.Glob(filepath.Join(strings.TrimSuffix(mainPath, ".jsonl"), "subagents", "agent-*.jsonl"))
	sort.Strings(files)
	var rows []Row
	for _, f := range files {
		var meta sidechainMeta
		if raw, err := os.ReadFile(strings.TrimSuffix(f, ".jsonl") + ".meta.json"); err == nil {
			_ = json.Unmarshal(raw, &meta)
		}
		if IsReviewer(meta.AgentType) {
			continue
		}
		link, err := ResolveLink(db, sessionUUID, sessionID, meta.Description)
		if err != nil {
			return nil, err
		}
		if !link.Linked {
			continue
		}
		r, err := scanSidechainFile(db, m, f, sessionUUID, sessionID, link.PhaseID)
		if err != nil {
			return nil, err
		}
		rows = append(rows, r...)
	}
	return rows, nil
}

func scanSidechainFile(db *sql.DB, m *Matcher, path, sessionUUID string, sessionID, phaseID int64) ([]Row, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil // a transcript removed since it was ingested: nothing to read
	}
	defer f.Close()
	type turn struct {
		id    int64
		seq   int
		agent string
	}
	turns := map[string]*turn{}
	turnOf := func(msgID string) (*turn, error) {
		if t, ok := turns[msgID]; ok {
			return t, nil
		}
		var t turn
		var agent sql.NullString
		err := db.QueryRow(`SELECT id, seq, agent_name FROM turns WHERE session_id = ? AND message_id = ? LIMIT 1`,
			sessionID, msgID).Scan(&t.id, &t.seq, &agent)
		if errors.Is(err, sql.ErrNoRows) {
			turns[msgID] = nil // not ingested (yet): no turn to anchor a hit on
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		t.agent = agent.String
		turns[msgID] = &t
		return &t, nil
	}

	var rows []Row
	br := bufio.NewReaderSize(f, 1<<20)
	for {
		line, rerr := br.ReadBytes('\n')
		if len(line) > 0 {
			var rec transcriptRecord
			if json.Unmarshal(line, &rec) == nil && rec.Type == "assistant" && rec.Message.ID != "" {
				var blocks []block
				if json.Unmarshal(rec.Message.Content, &blocks) == nil {
					for _, b := range blocks {
						text := ""
						switch {
						case b.Type == "text":
							text = b.Text
						case b.Type == "tool_use":
							text = HandbackReport(b.Name, b.Input)
						}
						if text == "" {
							continue
						}
						hits := m.Scan(text)
						if len(hits) == 0 {
							continue
						}
						t, err := turnOf(rec.Message.ID)
						if err != nil {
							return nil, err
						}
						if t == nil {
							continue
						}
						for _, h := range hits {
							h.TurnSeq = t.seq
							rows = append(rows, Row{SessionUUID: sessionUUID, TurnID: t.id, PhaseID: phaseID, AgentName: t.agent, Hit: h})
						}
					}
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, rerr
		}
	}
	return rows, nil
}

// PhaseCount is one phase's hits after a backfill.
type PhaseCount struct {
	Plan    string // tasks.external_id of the plan
	Seq     int
	Name    string
	PhaseID int64
	Hits    int // text-bearing
	Total   int // including key-only past the cap
}

// BackfillResult is what a backfill did.
type BackfillResult struct {
	Sessions int // phase-linked sessions scanned
	Added    int // rows new this run
	Phases   []PhaseCount
}

// Backfill scans every phase-linked session that started at or after since —
// a daemon phase run, or a session task_sessions links to a plan — and
// reports the per-phase counts of those sessions' hits. plan, when set, keeps
// only that plan's phases in the report (the scan is the same). Idempotent.
func Backfill(db *sql.DB, m *Matcher, since time.Time, plan string, now time.Time) (BackfillResult, error) {
	var res BackfillResult
	cutoff := since.UTC().Format(time.RFC3339)
	q, err := db.Query(`
		SELECT s.session_uuid FROM sessions s
		 WHERE s.started_at >= ?
		   AND (EXISTS (SELECT 1 FROM epic_phases ep WHERE ep.run_session_uuid = s.session_uuid)
		        OR EXISTS (SELECT 1 FROM task_sessions ts WHERE ts.session_id = s.id))
		 ORDER BY s.started_at, s.id`, cutoff)
	if err != nil {
		return res, err
	}
	var uuids []string
	for q.Next() {
		var u string
		if err := q.Scan(&u); err != nil {
			q.Close()
			return res, err
		}
		uuids = append(uuids, u)
	}
	q.Close()
	if err := q.Err(); err != nil {
		return res, err
	}
	for _, u := range uuids {
		n, err := ScanSession(db, m, u, now)
		if err != nil {
			return res, err
		}
		res.Sessions++
		res.Added += n
	}

	query := `
		SELECT COALESCE(t.external_id, ''), ep.seq, ep.name, ep.id,
		       SUM(CASE WHEN h.sentence <> '' THEN 1 ELSE 0 END), COUNT(*)
		  FROM workaround_hits h
		  JOIN epic_phases ep ON ep.id = h.phase_id
		  JOIN tasks t ON t.id = ep.workspace_task_id
		  JOIN sessions s ON s.session_uuid = h.session_uuid
		 WHERE s.started_at >= ?`
	args := []any{cutoff}
	if plan != "" {
		query += ` AND t.external_id = ?`
		args = append(args, plan)
	}
	rows, err := db.Query(query+` GROUP BY ep.id ORDER BY t.external_id, ep.seq`, args...)
	if err != nil {
		return res, err
	}
	defer rows.Close()
	for rows.Next() {
		var p PhaseCount
		if err := rows.Scan(&p.Plan, &p.Seq, &p.Name, &p.PhaseID, &p.Hits, &p.Total); err != nil {
			return res, err
		}
		res.Phases = append(res.Phases, p)
	}
	return res, rows.Err()
}
