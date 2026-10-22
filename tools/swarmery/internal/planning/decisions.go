package planning

// Decision ids (verify-not-trust phase 2): at PROCEED every interview question —
// answered or skipped — becomes a phase_decisions row named D-<seq>, written
// BEFORE the planner resumes, and its `## Decisions` line travels in the PROCEED
// message so the README states exactly what the store holds. The operator can
// flip a question's cost-to-change from the dashboard at any time (SetTurnCost).

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

var (
	// ErrTurnNotFound: no planning_turns row for (session, seq) (404).
	ErrTurnNotFound = errors.New("planning turn not found")
	// ErrBadCost: the requested cost is outside {cheap, expensive} (400).
	ErrBadCost = errors.New("cost must be cheap or expensive")
)

// turnDecision derives one turn's decision row and README line. answered is
// false for a turn PROCEED dismissed or one the operator only refined.
func turnDecision(sessionID int64, seq int, q PlanningQuestion, ans *wizardAnswer, now string) (store.Decision, string) {
	cost := q.CostToChange
	if cost != CostExpensive {
		cost = CostCheap
	}
	d := store.Decision{
		PlanningSessionID: &sessionID,
		DecisionID:        store.DecisionID(seq),
		Seq:               seq,
		Source:            store.DecisionSourceInterview,
		QuestionID:        q.ID,
		Question:          q.Question,
		Cost:              cost,
		CostReason:        q.CostReason,
		OptionIDs:         []string{},
		CreatedAt:         now,
	}
	labelFor := make(map[string]string, len(q.Options))
	recommended := ""
	for _, o := range q.Options {
		labelFor[o.ID] = o.Label
		if o.Recommended && !o.IsOther && recommended == "" {
			recommended = o.Label
		}
	}
	if ans != nil && ans.Kind == "answer" {
		d.Status = store.DecisionDecided
		d.DecidedAt = &now
		d.OptionIDs = append(d.OptionIDs, ans.SelectedOptionIDs...)
		var parts []string
		for _, id := range ans.SelectedOptionIDs {
			if l := labelFor[id]; l != "" && !isOtherOption(q, id) {
				parts = append(parts, l)
			}
		}
		if strings.TrimSpace(ans.OtherText) != "" {
			parts = append(parts, strings.TrimSpace(ans.OtherText))
		}
		d.Answer = strings.Join(parts, "; ")
	} else {
		d.Status = store.UndecidedStatus(cost)
		if d.Status == store.DecisionRecommended {
			d.Answer = strings.TrimSpace(recommended + " " + wsingest.RecommendedSuffix)
		} else {
			d.Answer = wsingest.UndecidedAnswer
		}
	}
	return d, wsingest.FormatDecisionLine(lineOf(d))
}

func isOtherOption(q PlanningQuestion, id string) bool {
	for _, o := range q.Options {
		if o.ID == id {
			return o.IsOther
		}
	}
	return false
}

func lineOf(d store.Decision) wsingest.DecisionLine {
	return wsingest.DecisionLine{Seq: d.Seq, Cost: d.Cost, Status: d.Status,
		Question: d.Question, Answer: d.Answer, Reason: d.CostReason}
}

// recordDecisions persists one phase_decisions row per planning turn of the
// wizard (in seq order) and returns their README lines. Best-effort per row: a
// failed write is logged and the line still travels, because the README the
// planner writes is what the scan will index anyway.
func (s *Service) recordDecisions(sessionID int64) []string {
	rows, err := s.DB.Query(
		`SELECT seq, question, answer FROM planning_turns WHERE planning_session_id = ? ORDER BY seq`, sessionID)
	if err != nil {
		log.Printf("error: planning: read turns for decisions session=%d: %v", sessionID, err)
		return nil
	}
	type turn struct {
		seq int
		q   PlanningQuestion
		ans *wizardAnswer
	}
	var turns []turn
	for rows.Next() {
		var (
			seq    int
			qJSON  string
			ansRaw sql.NullString
		)
		if err := rows.Scan(&seq, &qJSON, &ansRaw); err != nil {
			log.Printf("error: planning: scan turn session=%d: %v", sessionID, err)
			continue
		}
		var t turn
		t.seq = seq
		if json.Unmarshal([]byte(qJSON), &t.q) != nil {
			continue
		}
		if ansRaw.Valid {
			var a wizardAnswer
			if json.Unmarshal([]byte(ansRaw.String), &a) == nil {
				t.ans = &a
			}
		}
		turns = append(turns, t)
	}
	rows.Close()

	now := s.ts()
	lines := make([]string, 0, len(turns))
	for _, t := range turns {
		d, line := turnDecision(sessionID, t.seq, t.q, t.ans, now)
		if err := store.UpsertSessionDecision(s.DB, d); err != nil {
			log.Printf("error: planning: record %s session=%d: %v", d.DecisionID, sessionID, err)
		}
		lines = append(lines, line)
	}
	return lines
}

// SetTurnCost flips one interview question's cost-to-change: on the stored turn
// (and the current question when it is that turn), on its phase_decisions row
// once PROCEED has recorded one, and on its README line once the plan exists. An
// empty reason keeps the planner's. Returns the updated question.
func (s *Service) SetTurnCost(sessionID int64, seq int, cost, reason string) (*PlanningQuestion, error) {
	cost = strings.ToLower(strings.TrimSpace(cost))
	if cost != CostCheap && cost != CostExpensive {
		return nil, ErrBadCost
	}
	reason = strings.TrimSpace(reason)

	var (
		qJSON     string
		projectID int64
		current   sql.NullString
	)
	err := s.DB.QueryRow(`
		SELECT t.question, p.project_id, p.current_question
		  FROM planning_turns t JOIN planning_sessions p ON p.id = t.planning_session_id
		 WHERE t.planning_session_id = ? AND t.seq = ?`, sessionID, seq).Scan(&qJSON, &projectID, &current)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTurnNotFound
	}
	if err != nil {
		return nil, err
	}
	var q PlanningQuestion
	if err := json.Unmarshal([]byte(qJSON), &q); err != nil {
		return nil, err
	}
	q.CostToChange = cost
	if reason != "" {
		q.CostReason = reason
	}
	q.Lints = dropCostLints(q.Lints)
	b, err := json.Marshal(q)
	if err != nil {
		return nil, err
	}
	if _, err := s.DB.Exec(`UPDATE planning_turns SET question = ? WHERE planning_session_id = ? AND seq = ?`,
		string(b), sessionID, seq); err != nil {
		return nil, err
	}
	if current.Valid {
		var cq PlanningQuestion
		if json.Unmarshal([]byte(current.String), &cq) == nil && cq.ID == q.ID {
			if _, err := s.DB.Exec(`UPDATE planning_sessions SET current_question = ? WHERE id = ?`,
				string(b), sessionID); err != nil {
				return nil, err
			}
		}
	}

	d, err := store.SetSessionDecisionCost(s.DB, sessionID, store.DecisionID(seq), cost, reason)
	if err != nil {
		return nil, err
	}
	if d != nil && d.Status != store.DecisionDecided {
		// A skipped decision's answer text follows its status: the recommended
		// option when cheap, "undecided" when expensive.
		nd, _ := turnDecision(sessionID, seq, q, nil, s.ts())
		if _, err := s.DB.Exec(`UPDATE phase_decisions SET answer = ? WHERE id = ?`, nd.Answer, d.ID); err != nil {
			return nil, err
		}
		d.Answer = nd.Answer
	}
	if d != nil && d.WorkspaceTaskID != nil {
		// The plan exists: the README is the scan's source of truth, so the flip
		// must land in it too or the next scan would put the old cost back.
		if err := s.rewriteReadmeDecision(*d.WorkspaceTaskID, *d); err != nil {
			log.Printf("warn: planning: README decision %s task=%d: %v", d.DecisionID, *d.WorkspaceTaskID, err)
		}
	}
	s.notify(projectID)
	return &q, nil
}

func dropCostLints(lints []string) []string {
	var out []string
	for _, l := range lints {
		if l != LintQuestionCostMissing && l != LintQuestionCostInvalid {
			out = append(out, l)
		}
	}
	return out
}

// rewriteReadmeDecision replaces the D-n line of the plan README's
// `## Decisions` section with the row's current state, appending the line (and
// the section, when missing) otherwise.
func (s *Service) rewriteReadmeDecision(taskID int64, d store.Decision) error {
	var planDir string
	if err := s.DB.QueryRow(`SELECT path FROM task_artifacts WHERE task_id = ? AND kind = 'plan'`, taskID).
		Scan(&planDir); err != nil {
		return err
	}
	path := filepath.Join(planDir, "README.md")
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	line := wsingest.FormatDecisionLine(lineOf(d))
	text := string(b)
	re := regexp.MustCompile(`(?m)^[ \t]*[-*][ \t]+\*\*` + regexp.QuoteMeta(d.DecisionID) + `\*\*.*$`)
	switch {
	case re.MatchString(text):
		text = re.ReplaceAllLiteralString(text, line)
	default:
		head := regexp.MustCompile(`(?mi)^##[ \t]+Decisions[ \t]*$`)
		if loc := head.FindStringIndex(text); loc != nil {
			// Append at the end of the section: before the next `## ` or EOF.
			rest := text[loc[1]:]
			end := len(text)
			if n := regexp.MustCompile(`(?m)^##[ \t]`).FindStringIndex(rest); n != nil {
				end = loc[1] + n[0]
			}
			body := strings.TrimRight(text[:end], "\n")
			text = body + "\n" + line + "\n\n" + strings.TrimLeft(text[end:], "\n")
		} else {
			text = strings.TrimRight(text, "\n") + "\n\n## Decisions\n\n" + line + "\n"
		}
	}
	return os.WriteFile(path, []byte(text), 0o644)
}
