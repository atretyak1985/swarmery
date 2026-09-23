package dispatch

import (
	"database/sql"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/playbooks"
)

// setTaskPlaybook stamps a playbook name on a task row (the board write surface
// does this in production; here we set it directly for the dispatcher to read).
func setTaskPlaybook(t *testing.T, db *sql.DB, id int64, name string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE tasks SET playbook=? WHERE id=?`, name, id); err != nil {
		t.Fatalf("set playbook: %v", err)
	}
}

// writeProjectPlaybook writes a project-local playbook file and points project 1
// at that root (so the registry's project overlay finds it).
func writeProjectPlaybook(t *testing.T, db *sql.DB, projectRoot, name, content string) {
	t.Helper()
	dir := playbooks.ProjectDir(projectRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/"+name, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE projects SET path=? WHERE id=1`, projectRoot); err != nil {
		t.Fatalf("point project at root: %v", err)
	}
}

func newRegistry(t *testing.T) *playbooks.Registry {
	t.Helper()
	r, err := playbooks.New()
	if err != nil {
		t.Fatalf("playbooks.New: %v", err)
	}
	return r
}

// setTaskPrompt replaces a task's prompt (the auto-profile rule reads its size).
func setTaskPrompt(t *testing.T, db *sql.DB, id int64, prompt string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE tasks SET prompt=? WHERE id=?`, prompt, id); err != nil {
		t.Fatalf("set prompt: %v", err)
	}
}

// setTaskModel stamps a per-card model override (the board write surface's field).
func setTaskModel(t *testing.T, db *sql.DB, id int64, model string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE tasks SET model=? WHERE id=?`, model, id); err != nil {
		t.Fatalf("set model: %v", err)
	}
}

// ── model + permission resolution (phase 5.2) ──
//
// The `model:` frontmatter knob was parsed, surfaced in the UI as a chip, and
// never read by dispatch. These pin the three-step fallback that makes the chip
// honest, and the permission mode that resolves alongside it.

// A card's own model override beats the playbook's, which beats the global
// default. One test, three rows, because the ORDER is the contract.
func TestPlaybook_ModelFallbackCardThenPlaybookThenDefault(t *testing.T) {
	const playbookModel = "claude-opus-5"
	const cardModel = "claude-haiku-5"

	cases := []struct {
		name      string
		pbModel   string // "" ⇒ the recipe declares no model
		cardModel string // "" ⇒ the card declares no model
		want      string
	}{
		{"card override wins", playbookModel, cardModel, cardModel},
		{"playbook model when the card is silent", playbookModel, "", playbookModel},
		{"global default when both are silent", "", "", DefaultModel},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := testDB(t)
			root := t.TempDir()
			model := ""
			if tc.pbModel != "" {
				model = "model: " + tc.pbModel + "\n"
			}
			writeProjectPlaybook(t, db, root, "standard.md", `---
name: standard
`+model+`verify: normal
---
## Stage: implement
{task_prompt}
`)
			r := &stubRunner{run: func(spec RunSpec) (*Run, error) {
				ingestSession(t, db, spec.SessionUUID, "done")
				return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
			}}
			s := newTestService(t, db, r, &stubWt{})
			s.Playbooks = newRegistry(t)
			id := insertTask(t, db, "T-model", taskOpts{})
			setTaskPlaybook(t, db, id, "standard")
			if tc.cardModel != "" {
				setTaskModel(t, db, id, tc.cardModel)
			}

			s.Schedule()

			if r.count() != 1 {
				t.Fatalf("ran %d stages, want 1", r.count())
			}
			if got := r.spec(0).Model; got != tc.want {
				t.Errorf("spawn spec model = %q, want %q", got, tc.want)
			}
		})
	}
}

// The playbook's permission_mode reaches the spawn spec; a recipe that declares
// none leaves the field empty so the global knob still applies at the runner.
func TestPlaybook_PermissionModeReachesSpawnSpec(t *testing.T) {
	for _, tc := range []struct {
		name string
		decl string // frontmatter line ("" ⇒ knob absent)
		want string
	}{
		{"declared", "permission_mode: acceptEdits\n", "acceptEdits"},
		{"absent inherits the global knob", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := testDB(t)
			root := t.TempDir()
			writeProjectPlaybook(t, db, root, "standard.md", `---
name: standard
`+tc.decl+`verify: normal
---
## Stage: implement
{task_prompt}
`)
			r := &stubRunner{run: func(spec RunSpec) (*Run, error) {
				ingestSession(t, db, spec.SessionUUID, "done")
				return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
			}}
			s := newTestService(t, db, r, &stubWt{})
			s.Playbooks = newRegistry(t)
			id := insertTask(t, db, "T-perm", taskOpts{})
			setTaskPlaybook(t, db, id, "standard")

			s.Schedule()

			if got := r.spec(0).PermissionMode; got != tc.want {
				t.Errorf("spawn spec permission mode = %q, want %q", got, tc.want)
			}
		})
	}
}

// Every stage of a multi-stage recipe runs under the same model and permission
// mode — the knobs belong to the recipe, not to one step of it.
func TestPlaybook_KnobsApplyToEveryStage(t *testing.T) {
	db := testDB(t)
	root := t.TempDir()
	writeProjectPlaybook(t, db, root, "standard.md", `---
name: standard
model: claude-opus-5
permission_mode: acceptEdits
---
## Stage: one
{task_prompt}
## Stage: two
follow up: {previous_stage_output}
`)
	r := &stubRunner{run: func(spec RunSpec) (*Run, error) {
		ingestSession(t, db, spec.SessionUUID, "reply "+spec.SessionUUID)
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}}
	s := newTestService(t, db, r, &stubWt{})
	s.Playbooks = newRegistry(t)
	id := insertTask(t, db, "T-knobs", taskOpts{})
	setTaskPlaybook(t, db, id, "standard")

	s.Schedule()

	if r.count() != 2 {
		t.Fatalf("ran %d stages, want 2", r.count())
	}
	for i := 0; i < 2; i++ {
		if got := r.spec(i).Model; got != "claude-opus-5" {
			t.Errorf("stage %d model = %q, want claude-opus-5", i+1, got)
		}
		if got := r.spec(i).PermissionMode; got != "acceptEdits" {
			t.Errorf("stage %d permission mode = %q, want acceptEdits", i+1, got)
		}
	}
}

// ── auto-profile (phase 5.3) ──

// The rule itself: prompt size and declared dependencies are the only two
// signals available before any model runs.
func TestAutoProfile_Rule(t *testing.T) {
	long := strings.Repeat("x", 1501)
	cases := []struct {
		name   string
		prompt string
		deps   []string
		want   string
	}{
		{"short prompt, no deps", "fix the typo", nil, "standard"},
		{"long prompt", long, nil, "plan-first"},
		{"exactly at the threshold stays standard", strings.Repeat("x", 1500), nil, "standard"},
		{"dependencies present", "fix the typo", []string{"T-1"}, "plan-first"},
		{"long AND dependent", long, []string{"T-1"}, "plan-first"},
		{"empty dep list is no dependency", "fix the typo", []string{}, "standard"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := autoProfile(tc.prompt, tc.deps); got != tc.want {
				t.Errorf("autoProfile(len=%d, deps=%d) = %q, want %q",
					len(tc.prompt), len(tc.deps), got, tc.want)
			}
		})
	}
}

// review-heavy stays opt-in: auto-escalating to strict verification would spend
// verify budget on noise. No input may produce it.
func TestAutoProfile_NeverPicksReviewHeavy(t *testing.T) {
	for _, size := range []int{0, 1, 1500, 1501, 50000} {
		for _, deps := range [][]string{nil, {"T-1"}, {"T-1", "T-2"}} {
			if got := autoProfile(strings.Repeat("x", size), deps); got == "review-heavy" {
				t.Fatalf("autoProfile(len=%d, deps=%d) auto-selected review-heavy", size, len(deps))
			}
		}
	}
}

// A card that never chose a playbook gets one AND the choice is stamped on the
// row — an unstamped implicit default is how the playbook column stayed 99% NULL
// for its whole life. The stamp emits task_updated so the chip appears live.
func TestAutoProfile_StampsChoiceOnTheCard(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{run: func(spec RunSpec) (*Run, error) {
		ingestSession(t, db, spec.SessionUUID, "done")
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}}
	s := newTestService(t, db, r, &stubWt{})
	s.Playbooks = newRegistry(t)
	var notified int
	s.Notify = func(int64) { notified++ }
	id := insertTask(t, db, "T-auto", taskOpts{}) // short prompt, no deps, no playbook

	s.Schedule()

	got := taskField(t, db, id, "playbook")
	if !got.Valid || got.String != "standard" {
		t.Fatalf("playbook after auto-profile = %v, want standard stamped on the row", got)
	}
	if notified == 0 {
		t.Error("no task_updated frame emitted; the chip would not appear until a reload")
	}
}

// A LONG prompt auto-profiles to plan-first — and the stamped recipe is the one
// that actually ran (two stages, not one).
func TestAutoProfile_LongPromptStampsPlanFirst(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{run: func(spec RunSpec) (*Run, error) {
		ingestSession(t, db, spec.SessionUUID, "reply "+spec.SessionUUID)
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}}
	s := newTestService(t, db, r, &stubWt{})
	s.Playbooks = newRegistry(t)
	id := insertTask(t, db, "T-long", taskOpts{})
	setTaskPrompt(t, db, id, strings.Repeat("x", 1600))

	s.Schedule()

	got := taskField(t, db, id, "playbook")
	if !got.Valid || got.String != "plan-first" {
		t.Fatalf("playbook after auto-profile = %v, want plan-first", got)
	}
	if r.count() != 2 {
		t.Errorf("stamped plan-first but ran %d stages, want 2 — the stamp must name what ran", r.count())
	}
}

// An explicit choice is NEVER overwritten by the auto-profile, even when the
// rule would have picked something else.
func TestAutoProfile_NeverOverwritesAnExplicitChoice(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{run: func(spec RunSpec) (*Run, error) {
		ingestSession(t, db, spec.SessionUUID, "reply "+spec.SessionUUID)
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}}
	s := newTestService(t, db, r, &stubWt{})
	s.Playbooks = newRegistry(t)
	id := insertTask(t, db, "T-explicit", taskOpts{})
	// A long prompt: the rule WOULD say plan-first if the card had not chosen.
	setTaskPrompt(t, db, id, strings.Repeat("x", 1600))
	setTaskPlaybook(t, db, id, "review-heavy")

	s.Schedule()

	got := taskField(t, db, id, "playbook")
	if !got.Valid || got.String != "review-heavy" {
		t.Fatalf("playbook = %v, want review-heavy (an explicit choice is never overwritten)", got)
	}
}

// A row written before the alias landed still dispatches: the registry resolves
// 'quick-fix' to the standard recipe. The stored spelling is left alone —
// canonicalization happens on WRITE (the API stores the resolved name), and the
// auto-profile must not rewrite a value the operator chose.
func TestAutoProfile_LeavesAliasedCardsToTheRegistry(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{run: func(spec RunSpec) (*Run, error) {
		ingestSession(t, db, spec.SessionUUID, "done")
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}}
	s := newTestService(t, db, r, &stubWt{})
	s.Playbooks = newRegistry(t)
	id := insertTask(t, db, "T-alias", taskOpts{})
	setTaskPlaybook(t, db, id, "quick-fix")

	s.Schedule()

	if r.count() != 1 {
		t.Fatalf("aliased card ran %d stages, want 1 (standard)", r.count())
	}
	if got := column(t, db, id); got != "in_review" {
		t.Errorf("column = %q, want in_review — a stored alias must still dispatch", got)
	}
	if got := taskField(t, db, id, "playbook"); !got.Valid || got.String != "quick-fix" {
		t.Errorf("playbook = %v, want the stored alias untouched by the auto-profile", got)
	}
}

// With NO registry attached the dispatcher keeps its pre-playbook shape: one
// implicit stage, and nothing stamped on the card.
func TestAutoProfile_NoRegistryStampsNothing(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{run: func(spec RunSpec) (*Run, error) {
		ingestSession(t, db, spec.SessionUUID, "done")
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}}
	s := newTestService(t, db, r, &stubWt{}) // s.Playbooks left nil
	id := insertTask(t, db, "T-noreg", taskOpts{})

	s.Schedule()

	if got := taskField(t, db, id, "playbook"); got.Valid && got.String != "" {
		t.Errorf("playbook = %q with no registry attached, want untouched", got.String)
	}
	if r.count() != 1 {
		t.Errorf("ran %d stages, want 1", r.count())
	}
}

// A review-heavy task runs TWO sequential stages in ONE worktree, both sessions
// linked to the task (acceptance criterion #1).
func TestPlaybook_ReviewHeavyRunsTwoLinkedStages(t *testing.T) {
	db := testDB(t)
	wt := &stubWt{}
	// Each stage ingests a distinct assistant reply (no sentinel) and exits 0.
	var mu sync.Mutex
	seen := map[string]bool{}
	r := &stubRunner{run: func(spec RunSpec) (*Run, error) {
		mu.Lock()
		seen[spec.SessionUUID] = true
		mu.Unlock()
		ingestSession(t, db, spec.SessionUUID, "stage output for "+spec.SessionUUID)
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}}
	s := newTestService(t, db, r, wt)
	s.Playbooks = newRegistry(t)
	id := insertTask(t, db, "T-rh", taskOpts{})
	setTaskPlaybook(t, db, id, "review-heavy")

	s.Schedule()

	// Two stages → two runs → two linked sessions, one worktree acquired.
	if r.count() != 2 {
		t.Fatalf("runner started %d times, want 2 (implement + self-review)", r.count())
	}
	if wt.acquiredCount() != 1 {
		t.Fatalf("worktree acquired %d times, want 1 (both stages share it)", wt.acquiredCount())
	}
	var links int
	if err := db.QueryRow(`SELECT COUNT(*) FROM task_sessions WHERE task_id=?`, id).Scan(&links); err != nil {
		t.Fatalf("count links: %v", err)
	}
	if links != 2 {
		t.Fatalf("task_sessions links = %d, want 2 (both stage sessions linked)", links)
	}
	// Both stage runs shared the same cwd (the one worktree).
	cwds := map[string]bool{}
	for _, spec := range r.specs {
		cwds[spec.Cwd] = true
	}
	if len(cwds) != 1 {
		t.Fatalf("stages ran in %d distinct cwds, want 1 worktree", len(cwds))
	}
	if got := column(t, db, id); got != "in_review" {
		t.Errorf("column after 2-stage run = %q, want in_review", got)
	}
}

// plan-first stage 2 receives stage 1's reply via {previous_stage_output}
// (acceptance criterion #1, second half).
func TestPlaybook_PlanFirstInjectsPreviousOutput(t *testing.T) {
	db := testDB(t)
	const planReply = "PLAN: 1. do X  2. do Y"
	var stage2Prompt string
	var call int
	r := &stubRunner{run: func(spec RunSpec) (*Run, error) {
		call++
		if call == 1 {
			// Stage 1 (plan): reply with the plan text, exit 0.
			ingestSession(t, db, spec.SessionUUID, planReply)
		} else {
			// Stage 2 (implement): capture the prompt it received.
			stage2Prompt = spec.Prompt
			ingestSession(t, db, spec.SessionUUID, "implemented per plan")
		}
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}}
	s := newTestService(t, db, r, &stubWt{})
	s.Playbooks = newRegistry(t)
	id := insertTask(t, db, "T-pf", taskOpts{})
	setTaskPlaybook(t, db, id, "plan-first")

	s.Schedule()

	if call != 2 {
		t.Fatalf("plan-first ran %d stages, want 2", call)
	}
	if !strings.Contains(stage2Prompt, planReply) {
		t.Fatalf("stage 2 prompt did not inject stage 1's plan via {previous_stage_output}:\n%s", stage2Prompt)
	}
}

// A non-final stage that exits nonzero STOPS the chain with a stage-scoped
// dispatch_error; the later stage never runs.
func TestPlaybook_Stage1FailureStopsChain(t *testing.T) {
	db := testDB(t)
	var call int
	r := &stubRunner{run: func(spec RunSpec) (*Run, error) {
		call++
		// Stage 1 fails (nonzero exit, no sentinel).
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 3, Stderr: "compile error"}, nil
	}}
	s := newTestService(t, db, r, &stubWt{})
	s.Playbooks = newRegistry(t)
	id := insertTask(t, db, "T-fail", taskOpts{})
	setTaskPlaybook(t, db, id, "review-heavy")

	s.Schedule()

	if call != 1 {
		t.Fatalf("chain ran %d stages, want 1 (stage-1 failure must stop it)", call)
	}
	if got := column(t, db, id); got != "in_review" {
		t.Errorf("column = %q, want in_review", got)
	}
	e := taskField(t, db, id, "dispatch_error")
	if !e.Valid || !strings.Contains(e.String, "stage implement") {
		t.Errorf("dispatch_error = %q, want a 'stage implement failed' message", e.String)
	}
}

// A sentinel on the FIRST stage is authoritative and stops the chain (an honest
// PREMISE STALE means later stages are pointless).
func TestPlaybook_SentinelOnStage1StopsChain(t *testing.T) {
	db := testDB(t)
	wt := &stubWt{}
	var call int
	r := &stubRunner{run: func(spec RunSpec) (*Run, error) {
		call++
		ingestSession(t, db, spec.SessionUUID, "PREMISE STALE: already implemented on HEAD")
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}}
	s := newTestService(t, db, r, wt)
	s.Playbooks = newRegistry(t)
	id := insertTask(t, db, "T-stale2", taskOpts{})
	setTaskPlaybook(t, db, id, "review-heavy")

	s.Schedule()

	if call != 1 {
		t.Fatalf("sentinel on stage 1 should stop the chain; ran %d stages", call)
	}
	if got := column(t, db, id); got != "done" {
		t.Errorf("column = %q, want done (PREMISE STALE sentinel)", got)
	}
}

// The default (NULL) playbook keeps the classic single-stage flow even with a
// registry attached — one run, one link, in_review.
func TestPlaybook_NullFallsBackToSingleStage(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{run: func(spec RunSpec) (*Run, error) {
		ingestSession(t, db, spec.SessionUUID, "done")
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}}
	s := newTestService(t, db, r, &stubWt{})
	s.Playbooks = newRegistry(t)
	id := insertTask(t, db, "T-null", taskOpts{}) // no playbook set

	s.Schedule()

	if r.count() != 1 {
		t.Fatalf("null playbook ran %d stages, want 1 (standard/default)", r.count())
	}
	if got := column(t, db, id); got != "in_review" {
		t.Errorf("column = %q, want in_review", got)
	}
}

// A project-local playbook overriding a built-in name is honored by the
// dispatcher (the registry resolves project → built-in).
func TestPlaybook_ProjectOverrideResolvedByDispatcher(t *testing.T) {
	db := testDB(t)
	root := t.TempDir()
	// Override 'standard' with a TWO-stage project recipe.
	writeProjectPlaybook(t, db, root, "standard.md", `---
name: standard
verify: normal
---
## Stage: one
{task_prompt}
## Stage: two
follow up: {previous_stage_output}
`)
	r := &stubRunner{run: func(spec RunSpec) (*Run, error) {
		ingestSession(t, db, spec.SessionUUID, "reply "+spec.SessionUUID)
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}}
	s := newTestService(t, db, r, &stubWt{})
	s.Playbooks = newRegistry(t)
	id := insertTask(t, db, "T-ovr", taskOpts{})
	setTaskPlaybook(t, db, id, "standard")

	s.Schedule()

	if r.count() != 2 {
		t.Fatalf("project override 'standard' ran %d stages, want 2", r.count())
	}
}
