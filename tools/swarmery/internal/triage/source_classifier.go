package triage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	dec "github.com/atretyak1985/swarmery/tools/swarmery/internal/decide"
)

// kindClassifier is the inbox kind of the local classifier's open questions.
const kindClassifier = "classifier"

// Excerpt bounds for Prepare: the opening user request and each closing turn.
const (
	classifierOpeningBytes = 2000
	classifierTurnBytes    = 1500
	classifierLastTurns    = 4
)

// classifierInstruction is what the judge is asked for every session; one line
// per question follows it.
const classifierInstruction = "For each question pick the answer that describes what actually happened in this session. " +
	"The local model's guess is shown for reference and is often wrong. " +
	"Answer skip for a question the evidence cannot settle — a wrong answer is worse than none."

// ClassifierSource offers the label queue (decide.LabelQueue) to a triage run:
// one Item per session, one Part per open question about it. Applying a value
// writes the question's ground truth with source agent; Undo clears exactly
// that label, never an operator's or an observed one.
type ClassifierSource struct {
	DB         *sql.DB
	SystemPath string           // projects.path of the System project; its sessions are left out
	Now        func() time.Time // nil ⇒ time.Now
}

var (
	_ Source   = (*ClassifierSource)(nil)
	_ Preparer = (*ClassifierSource)(nil)
)

// Kind is "classifier".
func (s *ClassifierSource) Kind() string { return kindClassifier }

func (s *ClassifierSource) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Collect lists the waiting sessions, oldest first: one LabelQueue query plus
// a query for the sessions' projects. No evidence is built here. A decision
// with no session, or whose session has no row yet (not ingested), is skipped —
// there is no transcript to judge it from. A non-zero limit caps the number of
// sessions.
func (s *ClassifierSource) Collect(ctx context.Context, sc Scope, limit int) ([]Item, error) {
	queue, err := dec.LabelQueue(s.DB, dec.QueueOptions{Limit: -1, ProjectID: sc.ProjectID, SystemPath: s.SystemPath})
	if err != nil {
		return nil, err
	}
	bySession := map[string]*Item{}
	var keys []string
	// LabelQueue is newest first; walk it backwards so parts read in asking order.
	for i := len(queue) - 1; i >= 0; i-- {
		q := queue[i]
		if q.SessionUUID == "" {
			continue
		}
		it, ok := bySession[q.SessionUUID]
		if !ok {
			it = &Item{Kind: kindClassifier, Key: q.SessionUUID, Title: sessionTitle(q), WaitingSince: q.CreatedAt}
			bySession[q.SessionUUID] = it
			keys = append(keys, q.SessionUUID)
		}
		if q.CreatedAt < it.WaitingSince {
			it.WaitingSince = q.CreatedAt
		}
		it.Parts = append(it.Parts, Part{Ref: strconv.FormatInt(q.ID, 10), Label: q.QuestionID,
			Allowed: slices.Clone(q.Options)})
	}
	// A session not ingested yet has no row: its Prepare could only fail, so it
	// waits for a later run. Dropped before the limit, so the limit counts
	// sessions that can be judged.
	projects, err := s.sessionProjects(ctx, keys)
	if err != nil {
		return nil, err
	}
	items := make([]Item, 0, len(keys))
	for _, k := range keys {
		pid, ok := projects[k]
		if !ok {
			continue
		}
		it := *bySession[k]
		it.ProjectID = pid
		items = append(items, it)
	}
	slices.SortStableFunc(items, func(a, b Item) int {
		if c := strings.Compare(a.WaitingSince, b.WaitingSince); c != 0 {
			return c
		}
		return strings.Compare(a.Key, b.Key)
	})
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

// sessionTitle is the session's title, or the head of its uuid when it has none.
func sessionTitle(q dec.QueueItem) string {
	if q.SessionTitle != "" {
		return q.SessionTitle
	}
	if len(q.SessionUUID) > 8 {
		return q.SessionUUID[:8]
	}
	return q.SessionUUID
}

// sessionBatch bounds the bind arguments of one sessionProjects query.
const sessionBatch = 500

// sessionProjects resolves project_id for every session that has a row, a
// batch of uuids per query. A session without a row is absent from the map; a
// session without a project maps to 0.
func (s *ClassifierSource) sessionProjects(ctx context.Context, uuids []string) (map[string]int64, error) {
	out := map[string]int64{}
	for chunk := range slices.Chunk(uuids, sessionBatch) {
		args := make([]any, len(chunk))
		for i, u := range chunk {
			args[i] = u
		}
		if err := s.scanSessionProjects(ctx, args, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *ClassifierSource) scanSessionProjects(ctx context.Context, args []any, out map[string]int64) error {
	rows, err := s.DB.QueryContext(ctx, `SELECT session_uuid, COALESCE(project_id, 0) FROM sessions
		WHERE session_uuid IN (`+placeholders(len(args))+`)`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var uuid string
		var pid int64
		if err := rows.Scan(&uuid, &pid); err != nil {
			return err
		}
		out[uuid] = pid
	}
	return rows.Err()
}

// Prepare fills the item's Instruction (the fixed question text plus one line
// per open question with the local model's answer) and Evidence (the D2 digest
// plus a main-thread transcript excerpt) for an item the run will judge.
func (s *ClassifierSource) Prepare(ctx context.Context, it *Item) error {
	answers, err := s.localAnswers(ctx, it.Parts)
	if err != nil {
		return err
	}
	var ins strings.Builder
	ins.WriteString(classifierInstruction)
	ins.WriteString("\n\nQuestions:\n")
	for _, p := range it.Parts {
		fmt.Fprintf(&ins, "- %s %s: local model answered %q; allowed: %s\n",
			p.Ref, p.Label, answers[p.Ref], strings.Join(p.Allowed, ", "))
	}

	digest, err := dec.SessionEvidence(s.DB, it.Key)
	if err != nil {
		return fmt.Errorf("session %s evidence: %w", it.Key, err)
	}
	excerpt, err := s.transcriptExcerpt(ctx, it.Key)
	if err != nil {
		return fmt.Errorf("session %s transcript: %w", it.Key, err)
	}
	it.Instruction = ins.String()
	it.Evidence = digest + excerpt
	return nil
}

// localAnswers reads the local model's answer for every part's decision in
// one query, keyed by Part.Ref.
func (s *ClassifierSource) localAnswers(ctx context.Context, parts []Part) (map[string]string, error) {
	out := map[string]string{}
	args := make([]any, 0, len(parts))
	for _, p := range parts {
		id, err := decisionID(p.Ref)
		if err != nil {
			return nil, err
		}
		args = append(args, id)
	}
	if len(args) == 0 {
		return out, nil
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id, answer FROM decisions WHERE id IN (`+placeholders(len(args))+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var answer string
		if err := rows.Scan(&id, &answer); err != nil {
			return nil, err
		}
		out[strconv.FormatInt(id, 10)] = answer
	}
	return out, rows.Err()
}

// transcriptExcerpt is the session's main thread in brief: the first user turn
// (cut to classifierOpeningBytes) and the last classifierLastTurns turns (each
// cut to classifierTurnBytes). Turns without text are skipped; a session with
// no such turn gives "".
func (s *ClassifierSource) transcriptExcerpt(ctx context.Context, uuid string) (string, error) {
	const mainThread = `FROM turns t JOIN sessions s ON s.id = t.session_id
		WHERE s.session_uuid = ? AND t.agent_name IS NULL AND COALESCE(t.text, '') <> ''`
	var opening string
	err := s.DB.QueryRowContext(ctx, `SELECT t.text `+mainThread+` AND t.role = 'user' ORDER BY t.seq LIMIT 1`, uuid).
		Scan(&opening)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT t.role, t.text `+mainThread+` ORDER BY t.seq DESC LIMIT ?`,
		uuid, classifierLastTurns)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var last []string
	for rows.Next() {
		var role, text string
		if err := rows.Scan(&role, &text); err != nil {
			return "", err
		}
		last = append(last, role+": "+cutBytes(text, classifierTurnBytes))
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if opening == "" && len(last) == 0 {
		return "", nil
	}
	slices.Reverse(last)
	var b strings.Builder
	b.WriteString("\n\ntranscript excerpt (main thread)\n")
	if opening != "" {
		b.WriteString("opening request:\n")
		b.WriteString(cutBytes(opening, classifierOpeningBytes))
		b.WriteString("\n")
	}
	if len(last) > 0 {
		b.WriteString("last turns:\n")
		b.WriteString(strings.Join(last, "\n"))
		b.WriteString("\n")
	}
	return b.String(), nil
}

// Apply writes value as the decision's ground truth with source agent. An
// operator label written meanwhile wins: decide.ErrAlreadyLabelled comes back.
func (s *ClassifierSource) Apply(_ context.Context, _ Item, p Part, value, _ string, _ json.RawMessage) (Applied, error) {
	id, err := decisionID(p.Ref)
	if err != nil {
		return Applied{}, err
	}
	if err := dec.ValidateTruth(s.DB, id, value); err != nil {
		return Applied{}, err
	}
	if err := dec.RecordGroundTruth(s.DB, id, value, dec.TruthAgent, s.now()); err != nil {
		return Applied{}, err
	}
	return Applied{Prior: json.RawMessage(`{}`)}, nil
}

// Undo clears the agent label, so the question returns to the queue. It is
// idempotent: a label already cleared, or no longer the agent's, is a no-op.
func (s *ClassifierSource) Undo(_ context.Context, v Verdict) error {
	id, err := decisionID(v.Ref)
	if err != nil {
		return err
	}
	if err := dec.ClearAgentTruth(s.DB, id); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}

// Open reports whether the decision still has no ground truth; an unknown
// decision is not open.
func (s *ClassifierSource) Open(ctx context.Context, ref string) (bool, error) {
	id, err := decisionID(ref)
	if err != nil {
		return false, err
	}
	var open bool
	switch err := s.DB.QueryRowContext(ctx, `SELECT ground_truth IS NULL FROM decisions WHERE id = ?`, id).Scan(&open); {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, err
	}
	return open, nil
}

// decisionID parses a classifier part ref (a decision id in decimal).
func decisionID(ref string) (int64, error) {
	id, err := strconv.ParseInt(ref, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("classifier: bad decision ref %q", ref)
	}
	return id, nil
}

// placeholders is "?,?,…" for n bind arguments.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// cutBytes keeps at most n bytes of text, cut back to a rune boundary.
func cutBytes(text string, n int) string {
	if len(text) <= n {
		return text
	}
	i := n
	for i > 0 && !utf8.RuneStart(text[i]) {
		i--
	}
	return text[:i]
}
