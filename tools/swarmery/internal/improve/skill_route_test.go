package improve

// Phase-5 routing, bundle and prompt tests: R11 (an accepted skill-kind
// recommendation) → a proposal against a SKILL.md, down BOTH paths the design
// admits — a resolved target and a needs_target fallback. The fallback is not an
// error case to be tolerated; it is half the contract, because guessing a target
// is the one thing the loop must never do with evidence it does not have.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// skillRepoExec fakes an origin/main that ships the given skills (and one agent,
// so the agent path stays exercisable from the same fixture).
func skillRepoExec(skills map[string]string) *resolverExec {
	var tree []string
	show := map[string]string{}
	for name, body := range skills {
		rel := "plugins/core/skills/" + name + "/SKILL.md"
		tree = append(tree, rel, "plugins/core/skills/"+name+"/resources/x.md")
		show[rel] = body
	}
	return &resolverExec{lsTree: strings.Join(tree, "\n") + "\n", show: show}
}

// seedLesson writes one task + retro + lesson row with the given identity.
func seedLesson(t *testing.T, db *sql.DB, id int, externalID, title, norm, action string) {
	t.Helper()
	mustExec(t, db, `INSERT OR IGNORE INTO projects (id, path, slug, first_seen)
		VALUES (1, '/p', 'p', ?)`, day(20))
	mustExec(t, db, `INSERT INTO tasks
		(id, project_id, title, prompt, status, created_at, started_at, source, external_id)
		VALUES (?, 1, ?, 'goal', 'done', ?, ?, 'workspace', ?)`,
		id, externalID, day(id), day(id), externalID)
	mustExec(t, db, `INSERT INTO task_retros (id, task_id, ingested_at) VALUES (?, ?, ?)`,
		id, id, day(id))
	var act any
	if action != "" {
		act = action
	}
	mustExec(t, db, `INSERT INTO retro_lessons (retro_id, seq, title, action, norm_title)
		VALUES (?, 1, ?, ?, ?)`, id, title, act, norm)
}

const (
	routeNorm  = "sync the plugin cache before building"
	routeTitle = "Sync the plugin cache before building"
)

// seedRecurringLesson writes the three distinct tasks R11 needs before it fires,
// newest first (task 1 is the most recent — day(n) counts backwards).
func seedRecurringLesson(t *testing.T, db *sql.DB, newestAction string) {
	t.Helper()
	seedLesson(t, db, 3, "2026-09-10-alpha", routeTitle, routeNorm, "remember to sync")
	seedLesson(t, db, 2, "2026-09-14-beta", routeTitle, routeNorm, "")
	seedLesson(t, db, 1, "2026-09-18-gamma", routeTitle, routeNorm, newestAction)
}

func routeService(t *testing.T, db *sql.DB, r Runner, skills map[string]string) *Service {
	t.Helper()
	return &Service{DB: db, Runner: r, Repo: "/repo", Exec: skillRepoExec(skills)}
}

// skillRow reads the target-bearing columns of a proposal.
type skillRow struct {
	agent, agentPath, kind, path, sha, diff, status string
	errCol                                          sql.NullString
	recID                                           sql.NullInt64
}

func readSkillProposal(t *testing.T, db *sql.DB, id int64) skillRow {
	t.Helper()
	var p skillRow
	if err := db.QueryRow(`
		SELECT agent, agent_path, target_kind, target_path, base_sha256, diff, status,
		       error, recommendation_id
		  FROM agent_change_proposals WHERE id = ?`, id).
		Scan(&p.agent, &p.agentPath, &p.kind, &p.path, &p.sha, &p.diff, &p.status,
			&p.errCol, &p.recID); err != nil {
		t.Fatalf("read proposal %d: %v", id, err)
	}
	return p
}

// TestRouteSkillResolvesTargetFromAction is the RESOLVED path: the newest
// lesson action names skills/docker-build, that skill ships at origin/main, and
// the proposal is generated against exactly that file.
func TestRouteSkillResolvesTargetFromAction(t *testing.T) {
	db := openDB(t)
	seedRecurringLesson(t, db, "add it to the plugins/core/skills/docker-build procedure")
	runner := &mockRunner{out: validOut}
	svc := routeService(t, db, runner, map[string]string{"docker-build": skillBody})

	recID := int64(42)
	seedR11Recommendation(t, db, recID, "add it to the plugins/core/skills/docker-build procedure")
	id, err := svc.RouteSkill(context.Background(), SkillRouteReq{NormTitle: routeNorm, RecommendationID: &recID})
	if err != nil {
		t.Fatalf("RouteSkill: %v", err)
	}
	p := readSkillProposal(t, db, id)
	if p.status != "proposed" {
		t.Fatalf("status = %q (err %v), want proposed", p.status, p.errCol.String)
	}
	if p.kind != TargetSkill {
		t.Errorf("target_kind = %q, want skill", p.kind)
	}
	if p.path != skillRel {
		t.Errorf("target_path = %q, want %q", p.path, skillRel)
	}
	if p.agentPath != "" {
		t.Errorf("agent_path = %q, want empty on a skill row", p.agentPath)
	}
	if p.agent != routeNorm {
		t.Errorf("agent = %q, want the lesson identity %q", p.agent, routeNorm)
	}
	if !p.recID.Valid || p.recID.Int64 != recID {
		t.Errorf("recommendation_id = %v, want %d", p.recID, recID)
	}
	sum := sha256.Sum256([]byte(skillBody))
	if want := hex.EncodeToString(sum[:]); p.sha != want {
		t.Errorf("base_sha256 = %q, want the sha of the origin/main SKILL.md", p.sha)
	}
	if p.diff == "" {
		t.Error("diff is empty — the model output was not stored")
	}

	// The model saw the SKILL prompt, not the agent one.
	if len(runner.prompts) != 1 {
		t.Fatalf("runner called %d times, want 1", len(runner.prompts))
	}
	prompt := runner.prompts[0]
	for _, want := range []string{
		"SKILL.md procedure file",
		"<skill-file path=\"" + skillRel + "\">",
		"may touch ONLY " + skillRel,
		"name: plus description: within the",
		routeNorm,
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("skill prompt missing %q", want)
		}
	}
	if strings.Contains(prompt, "<agent-file") {
		t.Error("the agent prompt was used for a skill target")
	}
}

// TestRouteSkillNeedsTargetWhenActionNamesNoSkill is the FALLBACK path: the
// evidence is real, the file is unknown, and the row says so instead of the loop
// inventing a target.
func TestRouteSkillNeedsTargetWhenActionNamesNoSkill(t *testing.T) {
	db := openDB(t)
	seedRecurringLesson(t, db, "somebody should write this down somewhere")
	runner := &mockRunner{out: validOut}
	svc := routeService(t, db, runner, map[string]string{"docker-build": skillBody})

	id, err := svc.RouteSkill(context.Background(), SkillRouteReq{NormTitle: routeNorm})
	if err != nil {
		t.Fatalf("RouteSkill: %v", err)
	}
	p := readSkillProposal(t, db, id)
	if p.status != StatusNeedsTarget {
		t.Fatalf("status = %q, want %s", p.status, StatusNeedsTarget)
	}
	if p.kind != TargetSkill || p.path != "" {
		t.Errorf("row = %s/%q, want skill with an empty target_path", p.kind, p.path)
	}
	if p.agent != routeNorm {
		t.Errorf("agent = %q, want the lesson identity", p.agent)
	}
	if !p.errCol.Valid || !strings.Contains(p.errCol.String, "names no skills/") {
		t.Errorf("error = %q, want it to say why no target was resolved", p.errCol.String)
	}
	if p.diff != "" {
		t.Error("a needs_target row must not carry a diff")
	}
	if len(runner.prompts) != 0 {
		t.Errorf("the model ran %d times for an unresolved target; want 0", len(runner.prompts))
	}
}

// TestRouteSkillNeedsTargetWhenSkillIsUnknown: the action names a skill no pack
// ships. Same outcome as "named nothing" — the operator picks — but the stored
// reason must distinguish the two, or a typo'd skill name is indistinguishable
// from a lesson that never proposed one.
func TestRouteSkillNeedsTargetWhenSkillIsUnknown(t *testing.T) {
	db := openDB(t)
	seedRecurringLesson(t, db, "put it in skills/no-such-skill")
	svc := routeService(t, db, &mockRunner{out: validOut}, map[string]string{"docker-build": skillBody})

	id, err := svc.RouteSkill(context.Background(), SkillRouteReq{NormTitle: routeNorm})
	if err != nil {
		t.Fatalf("RouteSkill: %v", err)
	}
	p := readSkillProposal(t, db, id)
	if p.status != StatusNeedsTarget {
		t.Fatalf("status = %q, want %s", p.status, StatusNeedsTarget)
	}
	if !p.errCol.Valid || !strings.Contains(p.errCol.String, "no-such-skill") {
		t.Errorf("error = %q, want it to name the unresolvable skill", p.errCol.String)
	}
}

// TestRouteSkillRejectsEmptyIdentity: '' is retro_lessons' "not folded yet"
// marker, never an identity. Routing on it would gather every unfolded lesson in
// the database into one bogus target.
func TestRouteSkillRejectsEmptyIdentity(t *testing.T) {
	db := openDB(t)
	svc := routeService(t, db, &mockRunner{out: validOut}, nil)
	if _, err := svc.RouteSkill(context.Background(), SkillRouteReq{NormTitle: "  "}); !errors.Is(err, ErrNoLessonIdentity) {
		t.Fatalf("err = %v, want ErrNoLessonIdentity", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM agent_change_proposals`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d rows written for an empty identity; want 0", n)
	}
}

// TestOneOpenProposalPerTarget: the invariant is keyed on the TARGET since
// phase 5 — an open agent proposal does not block a skill of the same name, but
// a second routing of the same lesson does.
func TestOneOpenProposalPerTarget(t *testing.T) {
	db := openDB(t)
	seedRecurringLesson(t, db, "add it to skills/docker-build")
	svc := routeService(t, db, &mockRunner{out: validOut}, map[string]string{"docker-build": skillBody})

	// An OPEN agent proposal that happens to share the lesson's name.
	mustExec(t, db, `INSERT INTO agent_change_proposals
		(agent, agent_path, target_kind, target_path, base_sha256, diff, rationale, status, created_at)
		VALUES (?, 'plugins/core/agents/tech-lead.md', 'agent', '', 'x', 'd', 'r', 'proposed', ?)`,
		routeNorm, day(1))

	id, err := svc.RouteSkill(context.Background(), SkillRouteReq{NormTitle: routeNorm})
	if err != nil {
		t.Fatalf("an open AGENT proposal must not block a SKILL target: %v", err)
	}
	if got, err := svc.OpenTargetProposalID(TargetSkill, skillRel); err != nil || got != id {
		t.Errorf("OpenTargetProposalID(skill, %s) = %d, %v; want %d", skillRel, got, err, id)
	}

	// Routing the same lesson again must be refused while that row is open.
	if _, err := svc.RouteSkill(context.Background(), SkillRouteReq{NormTitle: routeNorm}); !errors.Is(err, ErrOpenProposal) {
		t.Fatalf("second routing err = %v, want ErrOpenProposal", err)
	}

	// Closing it frees the target.
	mustExec(t, db, `UPDATE agent_change_proposals SET status = 'rejected' WHERE id = ?`, id)
	if _, err := svc.RouteSkill(context.Background(), SkillRouteReq{NormTitle: routeNorm}); err != nil {
		t.Fatalf("a rejected row must free the target: %v", err)
	}
}

// TestSkillEvidenceBundleContent pins what the skill bundle carries: the lesson
// identity, the distinct-task count and slugs, every wording and action — and
// NOT the agent-shaped sections, which cannot answer "what does the fleet keep
// rediscovering".
func TestSkillEvidenceBundleContent(t *testing.T) {
	db := openDB(t)
	seedRecurringLesson(t, db, "add it to skills/docker-build")
	svc := routeService(t, db, &mockRunner{out: validOut}, map[string]string{"docker-build": skillBody})

	ev, err := svc.SkillEvidence(routeNorm)
	if err != nil {
		t.Fatalf("SkillEvidence: %v", err)
	}
	if ev.AgentPath != skillRel || ev.AgentContent != skillBody {
		t.Errorf("evidence target = %q, want %q with the origin/main body", ev.AgentPath, skillRel)
	}
	sum := sha256.Sum256([]byte(skillBody))
	if want := hex.EncodeToString(sum[:]); ev.BaseSHA256 != want {
		t.Errorf("BaseSHA256 = %q, want the sha of the SKILL.md", ev.BaseSHA256)
	}
	for _, want := range []string{
		"# Evidence — skill docker-build",
		"identity (norm_title): " + routeNorm,
		"learned in 3 distinct tasks",
		"2026-09-10-alpha", "2026-09-14-beta", "2026-09-18-gamma",
		"Action: add it to skills/docker-build",
		"Action: remember to sync",
	} {
		if !strings.Contains(ev.Bundle, want) {
			t.Errorf("skill bundle missing %q; got:\n%s", want, ev.Bundle)
		}
	}
	for _, unwanted := range []string{"## Scorecard", "## Ledger assessments", "## Transcript excerpts"} {
		if strings.Contains(ev.Bundle, unwanted) {
			t.Errorf("skill bundle carries the agent-shaped section %q", unwanted)
		}
	}
	if len(ev.Bundle) > bundleCap {
		t.Errorf("bundle is %d bytes, over the %d cap", len(ev.Bundle), bundleCap)
	}
}

// TestSkillEvidenceBundleCap: a lesson with a pathological number
// of enormous rows still produces a capped bundle with the truncation marker —
// the same contract the agent bundle carries, proven separately because the
// skill builder assembles its text on a different path.
func TestSkillEvidenceBundleCap(t *testing.T) {
	db := openDB(t)
	huge := strings.Repeat("a very long lesson wording ", 400) // ~10.8KB per row
	for i := 1; i <= 8; i++ {
		seedLesson(t, db, i, "task-"+string(rune('a'+i)), huge, routeNorm, "see skills/docker-build")
	}
	svc := routeService(t, db, &mockRunner{out: validOut}, map[string]string{"docker-build": skillBody})

	ev, err := svc.SkillEvidence(routeNorm)
	if err != nil {
		t.Fatalf("SkillEvidence: %v", err)
	}
	if len(ev.Bundle) > bundleCap {
		t.Fatalf("bundle is %d bytes, over the %d cap", len(ev.Bundle), bundleCap)
	}
	if !strings.Contains(ev.Bundle, "[evidence truncated]") {
		t.Error("an over-cap bundle must carry the truncation marker, not silently lose rows")
	}
}

// TestGenerateAgentPromptUnchanged is the regression guard for the split: adding
// a skill variant must not have changed a single byte of what an AGENT target's
// prompt asks for.
func TestGenerateAgentPromptUnchanged(t *testing.T) {
	prompt := renderPrompt("plugins/core/agents/tech-lead.md", "body", "bundle")
	for _, want := range []string{
		"You are improving ONE Claude Code agent definition file",
		"<agent-file path=\"plugins/core/agents/tech-lead.md\">body</agent-file>",
		"<evidence>bundle</evidence>",
		"Target ≤120 changed lines",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("agent prompt missing %q", want)
		}
	}
	if strings.Contains(prompt, "SKILL.md") {
		t.Error("the agent prompt leaked skill wording")
	}
}
