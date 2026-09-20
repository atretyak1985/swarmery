package improve

// Skill proposals (agent-memory phase 5): the procedural half of the retro
// improvement loop.
//
// An agent proposal starts from "this agent keeps failing" and rewrites the
// agent's own prompt. A SKILL proposal starts from R11 — one lesson identity
// (retro_lessons.norm_title) learned in >= 3 DISTINCT tasks — and edits the
// SKILL.md that should have carried the lesson so the next run reads it instead
// of re-learning it. Same human gate, same guardrails, same apply/PR pipeline;
// only the evidence and the prompt differ, and this file holds both.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

// ErrSkillNotFound — the lesson named a skill that ships in no pack at
// origin/main. Routing turns this into a needs_target row rather than a failure:
// the evidence is real, only the file is unknown.
var ErrSkillNotFound = errors.New("skill not found in the apply repo")

// ErrNoLessonIdentity — routing was asked for an empty norm_title. '' is the
// "not folded yet" marker in retro_lessons, never an identity, so grouping by it
// would gather every unrelated lesson in the database into one bogus target.
var ErrNoLessonIdentity = errors.New("recommendation has no lesson identity (norm_title)")

// skillLessonLimit caps the lesson rows quoted in a skill bundle. R11 needs >= 3
// tasks to fire; twenty rows is far past the point where another repetition
// tells the model anything new.
const skillLessonLimit = 20

// SkillRouteReq parameterizes one R11 → proposal routing.
type SkillRouteReq struct {
	// NormTitle is the recommendation's target: the lesson's cross-task
	// identity (retro_lessons.norm_title, migration 0070).
	NormTitle string
	// RecommendationID links the proposal back to the accepted R11 row.
	RecommendationID *int64
}

// RouteSkill is the phase-5 entry point: turn an accepted R11 recommendation
// into a proposal against a SKILL.md.
//
// TWO PATHS, both of which end in a row the operator can see — never in silence:
//
//   - RESOLVED. The action R11 recorded for the lesson (see lessonAction) names a skill
//     (`… skills/<name> …`) that ships at plugins/<pack>/skills/<name>/SKILL.md
//     in origin/main. The proposal is generated against that file exactly like an
//     agent proposal: placeholder row first (so the one-open-per-target index
//     closes the TOCTOU window before the minutes-long model run), then evidence,
//     model, diff, status 'proposed'.
//
//   - NEEDS TARGET. The action names no skill, or names one no pack ships. The
//     evidence is still real — a lesson learned three times is a fact — so the
//     row is created with an empty target_path and status 'needs_target', which
//     the Retro page surfaces for an operator to point at a skill. Guessing a
//     target here would be the loop inventing the one thing it has no evidence
//     for.
//
// Pre-flight failures (no identity, open proposal, DB errors) return a typed
// error. Pipeline failures land on the row as status='failed', like Generate.
func (s *Service) RouteSkill(ctx context.Context, req SkillRouteReq) (int64, error) {
	norm := strings.TrimSpace(req.NormTitle)
	if norm == "" {
		return 0, ErrNoLessonIdentity
	}
	if open, err := s.OpenLessonProposalID(norm); err != nil {
		return 0, err
	} else if open != 0 {
		return 0, fmt.Errorf("%w (proposal %d)", ErrOpenProposal, open)
	}

	action, err := s.lessonAction(req.RecommendationID, norm)
	if err != nil {
		return 0, err
	}
	relPath, content, rerr := s.resolveSkillFromAction(action)
	if rerr != nil {
		if errors.Is(rerr, ErrSkillNotFound) {
			logSkillRoute(norm, action, "needs_target: "+rerr.Error())
			return s.insertNeedsTarget(req, norm, rerr.Error())
		}
		return 0, rerr
	}
	logSkillRoute(norm, action, "target "+relPath)

	ev, err := s.buildSkillEvidence(norm, relPath, content)
	if err != nil {
		return 0, err
	}
	// A second open proposal for the SAME FILE (routed from a different lesson)
	// is caught here by the partial unique index, not by a check-then-insert.
	id, err := s.insertSkillPlaceholder(req, norm, ev)
	if err != nil {
		return 0, err
	}
	defer s.releaseOnPanic(id)

	diff, rationale, runErr := s.runSkill(ctx, ev)
	if err := s.finishProposal(id, ev, diff, rationale, runErr); err != nil {
		return 0, err
	}
	return id, nil
}

// OpenLessonProposalID returns the id of the open skill proposal routed from
// this LESSON identity, or 0.
//
// Distinct from OpenTargetProposalID on purpose, and both are needed. The unique
// index keys on the FILE, which is the right invariant for the apply pipeline —
// two lessons must not both have an open diff against one SKILL.md. But the
// caller of RouteSkill has only the lesson: re-triggering R11 for a lesson that
// already resolved to a file would pass a file-keyed check (it does not know the
// file yet), reach the placeholder insert, and surface as a 500-ish log line
// instead of the friendly 409 the operator deserves. The lesson lives in `agent`
// on BOTH resolved and needs_target rows, so this one query covers both.
func (s *Service) OpenLessonProposalID(norm string) (int64, error) {
	var id int64
	err := s.DB.QueryRow(`
		SELECT id FROM agent_change_proposals
		 WHERE target_kind = ? AND agent = ?
		   AND status IN ('proposed','approved','needs_target')
		 ORDER BY id DESC LIMIT 1`, TargetSkill, norm).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// resolveSkillFromAction maps a lesson action line to a SKILL.md in the apply
// repo. Both "the action names no skill" and "no pack ships that skill" are
// ErrSkillNotFound, because the caller does the same thing with either: create a
// needs_target row.
func (s *Service) resolveSkillFromAction(action string) (relPath, content string, err error) {
	m := actionSkillRe.FindStringSubmatch(action)
	if m == nil {
		return "", "", fmt.Errorf("%w: the lesson action names no skills/<name>", ErrSkillNotFound)
	}
	return resolveSkillInRepo(s.Exec, s.Repo, m[1])
}

// insertNeedsTarget records an unresolved routing as an OPEN proposal the
// operator can act on. diff/rationale stay empty and base_sha256 is '' — there
// is no file yet to hash.
func (s *Service) insertNeedsTarget(req SkillRouteReq, norm, why string) (int64, error) {
	var recID any
	if req.RecommendationID != nil {
		recID = *req.RecommendationID
	}
	res, err := s.DB.Exec(`
		INSERT INTO agent_change_proposals
			(recommendation_id, agent, agent_path, target_kind, target_path,
			 base_sha256, diff, rationale, status, error, created_at)
		VALUES (?, ?, '', ?, '', '', '', '', ?, ?, ?)`,
		recID, norm, TargetSkill, StatusNeedsTarget, why, fmtTS(time.Now()))
	if err != nil {
		if isUniqueViolation(err) {
			return 0, ErrOpenProposal
		}
		return 0, err
	}
	return res.LastInsertId()
}

// insertSkillPlaceholder reserves the target's single open slot with an empty
// 'proposed' row before the model runs — the skill twin of insertPlaceholder.
func (s *Service) insertSkillPlaceholder(req SkillRouteReq, norm string, ev *Evidence) (int64, error) {
	var recID any
	if req.RecommendationID != nil {
		recID = *req.RecommendationID
	}
	res, err := s.DB.Exec(`
		INSERT INTO agent_change_proposals
			(recommendation_id, agent, agent_path, target_kind, target_path,
			 base_sha256, diff, rationale, status, created_at)
		VALUES (?, ?, '', ?, ?, ?, '', '', 'proposed', ?)`,
		recID, norm, TargetSkill, ev.AgentPath, ev.BaseSHA256, fmtTS(time.Now()))
	if err != nil {
		if isUniqueViolation(err) {
			open, oerr := s.OpenTargetProposalID(TargetSkill, ev.AgentPath)
			if oerr == nil && open != 0 {
				return 0, fmt.Errorf("%w (proposal %d)", ErrOpenProposal, open)
			}
			return 0, ErrOpenProposal
		}
		return 0, err
	}
	return res.LastInsertId()
}

// runSkill is run()'s skill variant: the skill prompt instead of the agent one,
// the same output contract.
func (s *Service) runSkill(ctx context.Context, ev *Evidence) (diff, rationale string, err error) {
	out, err := s.Runner.Run(ctx, renderSkillPrompt(ev.AgentPath, ev.AgentContent, ev.Bundle))
	if err != nil {
		return "", "", err
	}
	diff, rationale, err = splitDiffRationale(out)
	if errors.Is(err, ErrNoChange) {
		return "", "", errors.New(errNoChangeRow)
	}
	if err != nil {
		return "", "", fmt.Errorf("output contract: %w", err)
	}
	return diff, rationale, nil
}

// lessonAction resolves the `**Action**:` line the routing target is derived
// from, preferring the value R11 ALREADY stored over re-deriving one.
//
// When the routing came from an accepted R11 row, the rule wrote its chosen
// action into the recommendation's evidence JSON as `latest_action` — the very
// string the operator read in the card's "Latest action: …" line. Reading it
// back makes the file the loop edits and the line the operator approved the same
// string BY CONSTRUCTION. An absent key is a decision, not a miss: R11 omits
// `latest_action` when the group's newest task recorded no action, so the card
// showed none and routing must land on needs_target rather than reaching further
// back for an older task's action.
//
// The fallback query runs only when there is no recommendation to read from (a
// direct route, the SkillEvidence preview) or when the stored evidence is
// unreadable. See latestLessonAction for how that fallback can disagree.
func (s *Service) lessonAction(recID *int64, norm string) (string, error) {
	if recID != nil {
		action, ok, err := s.recommendationLatestAction(*recID)
		if err != nil {
			return "", err
		}
		if ok {
			return action, nil
		}
	}
	return s.latestLessonAction(norm)
}

// recommendationLatestAction reads `latest_action` out of one recommendation's
// evidence JSON. ok=false means "no stored answer" — the row is gone or its
// evidence is not JSON — never "the action was empty": an evidence blob that
// parses but carries no `latest_action` returns ("", true), because R11 omitting
// the key IS the answer.
func (s *Service) recommendationLatestAction(id int64) (string, bool, error) {
	var raw string
	err := s.DB.QueryRow(`SELECT evidence FROM recommendations WHERE id = ?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	var ev struct {
		LatestAction string `json:"latest_action"`
	}
	if jerr := json.Unmarshal([]byte(raw), &ev); jerr != nil {
		log.Printf("warn: improve: recommendation %d evidence is not JSON (%v); falling back to the lesson query", id, jerr)
		return "", false, nil
	}
	return ev.LatestAction, true, nil
}

// latestLessonAction returns the most recent NON-EMPTY `**Action**:` line
// recorded for a lesson identity, newest task first.
//
// It is the fallback, and it is NOT equivalent to what advisor.lessonGroups
// picked for the R11 card. Three deliberate differences, stated rather than
// glossed over:
//
//   - lessonGroups takes the first row's action even when it is EMPTY; this
//     query skips empty ones and reaches further back.
//   - lessonGroups breaks an equal-started_at tie with `t.external_id DESC`;
//     this query has no tiebreaker, so co-starting tasks can order either way.
//   - lessonGroups is bound to the rule's window and to `p.archived = 0`; this
//     query sees every task ever recorded.
//
// So this function can name a skill the card never mentioned. That is acceptable
// only as a fallback: prefer lessonAction, which reads R11's own stored answer
// whenever a recommendation id is available.
func (s *Service) latestLessonAction(norm string) (string, error) {
	var action string
	err := s.DB.QueryRow(`
		SELECT COALESCE(l.action, '')
		  FROM retro_lessons l
		  JOIN task_retros tr ON tr.id = l.retro_id
		  JOIN tasks t ON t.id = tr.task_id
		 WHERE l.norm_title = ? AND COALESCE(l.action, '') <> ''
		 ORDER BY t.started_at DESC, l.seq ASC
		 LIMIT 1`, norm).Scan(&action)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return action, err
}

// buildSkillEvidence assembles the bundle a skill proposal is generated from:
// the lesson identity, how many distinct tasks learned it, and every recorded
// wording + action + task slug, followed by nothing else. An agent bundle quotes
// scorecards and transcript excerpts because the question is "how is this agent
// behaving"; here the question is "what does the fleet keep having to
// rediscover", and a scorecard cannot answer it.
func (s *Service) buildSkillEvidence(norm, relPath, content string) (*Evidence, error) {
	sum := sha256.Sum256([]byte(content))
	ev := &Evidence{
		AgentPath:    relPath,
		AgentContent: content,
		BaseSHA256:   hex.EncodeToString(sum[:]),
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Evidence — skill %s\n\nSource: %s (sha256 %s)\n", skillNameOf(relPath), relPath, ev.BaseSHA256)
	fmt.Fprintf(&b, "\n## Recurring lesson\nidentity (norm_title): %s\n", norm)

	rows, err := s.DB.Query(`
		SELECT l.title, COALESCE(l.action, ''),
		       COALESCE(t.external_id, CAST(t.id AS TEXT)), COALESCE(t.started_at, '')
		  FROM retro_lessons l
		  JOIN task_retros tr ON tr.id = l.retro_id
		  JOIN tasks t ON t.id = tr.task_id
		 WHERE l.norm_title = ?
		 ORDER BY t.started_at DESC, l.seq ASC`, norm)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var lines []string
	tasks := map[string]bool{}
	for rows.Next() {
		var title, action, task, started string
		if err := rows.Scan(&title, &action, &task, &started); err != nil {
			return nil, err
		}
		tasks[task] = true
		if len(lines) >= skillLessonLimit {
			continue
		}
		line := fmt.Sprintf("- [task %s] %s", task, title)
		if action != "" {
			line += "\n  Action: " + action
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	fmt.Fprintf(&b, "learned in %d distinct tasks: %s\n", len(tasks), strings.Join(sortedKeys(tasks), ", "))
	b.WriteString("\n## Lesson rows (newest task first)\n")
	if len(lines) == 0 {
		b.WriteString("(none)\n")
	} else {
		b.WriteString(strings.Join(lines, "\n") + "\n")
	}

	ev.Bundle = capBundle(b.String())
	return ev, nil
}

// sortedKeys renders a set deterministically so two runs over the same data
// produce byte-identical bundles (and therefore comparable prompts).
func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// SkillEvidence is the read-only preview of a skill bundle, for a dashboard that
// wants to show what the model would be fed before a (minutes-long) run.
//
// It has only the lesson identity, so it resolves the target through the
// latestLessonAction fallback: the preview can therefore name a different skill
// than a routing that reads R11's stored latest_action. Pass the recommendation
// id through here too if this ever becomes an operator-facing pre-approval view.
func (s *Service) SkillEvidence(norm string) (*Evidence, error) {
	if strings.TrimSpace(norm) == "" {
		return nil, ErrNoLessonIdentity
	}
	action, err := s.latestLessonAction(norm)
	if err != nil {
		return nil, err
	}
	relPath, content, err := s.resolveSkillFromAction(action)
	if err != nil {
		return nil, err
	}
	return s.buildSkillEvidence(norm, relPath, content)
}

// logSkillRoute records the routing decision so a needs_target row on the page
// can be traced to the action line that failed to name a skill.
func logSkillRoute(norm, action, outcome string) {
	log.Printf("info: improve: R11 route %q (action %q) → %s", norm, action, outcome)
}
