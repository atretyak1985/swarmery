package lessons

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/surprise"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

const runUUID = "run-uuid-1"

var fixedNow = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "lessons.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) int64 {
	t.Helper()
	res, err := db.Exec(q, args...)
	if err != nil {
		t.Fatalf("exec %s: %v", q, err)
	}
	id, _ := res.LastInsertId()
	return id
}

const report = `Shipped the ingest change.

### Where reality diverged
The cache usage fields live under usage.cache_creation, not the flat total,
so internal/ingest/record.go needed a second parser.

Other notes.`

// seedRun inserts a project, a plan task, one finished phase with a Completion
// Report, a forecast, actuals and a surprise score of the given index.
func seedRun(t *testing.T, db *sql.DB, uuid string, index float64, completion string) int64 {
	t.Helper()
	mustExec(t, db, `INSERT OR IGNORE INTO projects(id, path, slug, first_seen) VALUES(1, '/repo', 'p', '2026-01-01T00:00:00Z')`)
	taskID := mustExec(t, db, `INSERT INTO tasks (project_id, title, prompt, status, created_at,
		started_at, source, external_id) VALUES (1, 'The Plan', 'goal', 'running',
		'2026-09-23T00:00:00Z', '2026-09-23T00:00:00Z', 'workspace', ?)`, "2026-09-23-plan-"+uuid)
	phaseID := mustExec(t, db, `INSERT INTO epic_phases
		(workspace_task_id, seq, name, doc_path, depends_on, run_state, run_session_uuid, completion_report)
		VALUES (?, 1, 'Phase One', ?, '[]', 'done', ?, ?)`, taskID, "/ws/plan/phase-"+uuid+".md", uuid, completion)
	mustExec(t, db, `INSERT INTO phase_forecasts
		(phase_id, kind, areas_json, size_band, duration_band, outcome, confidence, post_hoc, doc_hash)
		VALUES (?, 'prior', '["internal/cost"]', 'S', '30-90m', 'done', 0.8, 0, 'h1')`, phaseID)
	mustExec(t, db, `INSERT INTO phase_actuals
		(phase_id, session_uuid, files_json, areas_json, area_depth, lines_added, lines_removed, size_band,
		 duration_s, outcome, start_point, source, computed_at)
		VALUES (?, ?, '[{"path":"internal/ingest/record.go","added":120,"removed":4}]', '["internal/ingest"]', 2,
		        120, 4, 'M', 5400, 'completed', 'abc123', 'run-end', '2026-09-23T01:00:00Z')`, phaseID, uuid)
	mustExec(t, db, `INSERT INTO phase_surprise (phase_id, session_uuid, surprise_index, top_component,
		summary, detail_json, computed_at) VALUES (?, ?, ?, 'unexpected_areas', 'touched ingest, not cost',
		'{"unexpectedAreas":["internal/ingest"],"missedAreas":["internal/cost"]}', '2026-09-23T01:00:00Z')`,
		phaseID, uuid, index)
	return phaseID
}

type fakeRunner struct {
	out    string
	err    error
	calls  int
	prompt string
}

func (f *fakeRunner) Run(_ context.Context, prompt string) (string, error) {
	f.calls++
	f.prompt = prompt
	return f.out, f.err
}

func threshold(v float64) *float64 { return &v }

func newGen(db *sql.DB, r Runner) *Generator {
	g := &Generator{DB: db, Runner: r, Cfg: Config{Enabled: true, Threshold: threshold(0.6), Model: "m"}}
	g.Now = func() time.Time { return fixedNow }
	return g
}

const goodLesson = `{"title":"Cache usage lives under usage.cache_creation","guidance":"In internal/ingest, read cache usage from usage.cache_creation; the flat total is legacy.","area_globs":["internal/ingest/**"],"cause":"The forecast assumed the flat total was current.","evidence":["divergence:%d","file:internal/ingest/record.go"]}`

func lessonJSON(phaseID int64, extra ...string) string {
	items := []string{strings.Replace(goodLesson, "%d", itoa(phaseID), 1)}
	items = append(items, extra...)
	return `{"lessons":[` + strings.Join(items, ",") + `]}`
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

var wsNorm = wsingest.NormalizeLessonTitle

// ── divergence + config ──

func TestExtractDivergence(t *testing.T) {
	got := ExtractDivergence(report)
	if !strings.HasPrefix(got, "The cache usage fields") || strings.Contains(got, "Other notes") {
		t.Fatalf("paragraph = %q", got)
	}
	inline := "**Where reality diverged:** scope grew into the API.\nsecond line\n\nafter"
	if got := ExtractDivergence(inline); got != "scope grew into the API.\nsecond line" {
		t.Fatalf("inline = %q", got)
	}
	if ExtractDivergence("no such paragraph") != "" || ExtractDivergence("") != "" {
		t.Fatal("absent paragraph must be empty")
	}
	if got := ExtractDivergence("## Where reality diverged\n## Next"); got != "" {
		t.Fatalf("empty section = %q", got)
	}
}

func TestConfigFromEnv(t *testing.T) {
	env := map[string]string{}
	get := func(k string) string { return env[k] }
	cfg, warn := ConfigFromEnv(get, threshold(0.6))
	if !cfg.Enabled || cfg.Model != DefaultModel || *cfg.Threshold != 0.6 || len(warn) != 0 {
		t.Fatalf("default = %+v %v", cfg, warn)
	}
	env[EnvEnabled], env[EnvModel] = "off", "claude-opus-5-5"
	cfg, _ = ConfigFromEnv(get, nil)
	if cfg.Enabled || cfg.Model != "claude-opus-5-5" || cfg.Threshold != nil {
		t.Fatalf("off = %+v", cfg)
	}
	if !strings.Contains(cfg.String(), "threshold=off") {
		t.Errorf("String = %s", cfg)
	}
	env[EnvEnabled] = "maybe"
	if cfg, warn = ConfigFromEnv(get, nil); !cfg.Enabled || len(warn) != 1 {
		t.Fatalf("bad value = %+v %v", cfg, warn)
	}
}

// ── schema validation ──

func TestParseOutputSchema(t *testing.T) {
	for name, tc := range map[string]struct {
		raw  string
		want int
		fail string
	}{
		"empty list":      {raw: `{"lessons":[]}`, want: 0},
		"fenced":          {raw: "```json\n{\"lessons\":[{\"title\":\"t\"}]}\n```", want: 1},
		"no json":         {raw: "I found nothing", fail: "no JSON"},
		"missing lessons": {raw: `{"items":[]}`, fail: "schema"},
		"null lessons":    {raw: `{"lessons":null}`, fail: `no "lessons"`},
		"unknown field":   {raw: `{"lessons":[{"title":"t","why":"x"}]}`, fail: "schema"},
		"too many":        {raw: `{"lessons":[{},{},{}]}`, fail: "at most 2"},
		"wrong type":      {raw: `{"lessons":"none"}`, fail: "schema"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := ParseOutput(tc.raw)
			if tc.fail != "" {
				if err == nil || !strings.Contains(err.Error(), tc.fail) {
					t.Fatalf("err = %v, want %q", err, tc.fail)
				}
				return
			}
			if err != nil || len(got) != tc.want {
				t.Fatalf("got %d, %v", len(got), err)
			}
		})
	}
}

// ── citation validation ──

func TestValidateCandidateCitations(t *testing.T) {
	allowed := map[string]bool{"divergence:7": true, "file:a.go": true}
	base := Candidate{Title: "T", Guidance: "Do the thing in a.go.", AreaGlobs: []string{"internal/**"},
		Cause: "It differed.", Evidence: []string{"[E:divergence:7]", "file:a.go", "file:a.go"}}
	got, err := ValidateCandidate(base, allowed)
	if err != nil {
		t.Fatalf("valid lesson rejected: %v", err)
	}
	if len(got.Evidence) != 2 || got.Evidence[0] != "divergence:7" {
		t.Fatalf("normalized evidence = %v", got.Evidence)
	}

	mut := func(f func(*Candidate)) Candidate {
		c := base
		c.Evidence = append([]string{}, base.Evidence...)
		f(&c)
		return c
	}
	for name, tc := range map[string]struct {
		c       Candidate
		allowed map[string]bool
		want    string
	}{
		"uncited":         {mut(func(c *Candidate) { c.Evidence = nil }), allowed, "cites no evidence"},
		"foreign id":      {mut(func(c *Candidate) { c.Evidence = []string{"divergence:7", "file:invented.go"} }), allowed, "not in its input"},
		"nothing offered": {base, nil, "no evidence was offered"},
		"no cause":        {mut(func(c *Candidate) { c.Cause = " " }), allowed, "no cited cause"},
		"no title":        {mut(func(c *Candidate) { c.Title = "" }), allowed, "empty title"},
		"two lines":       {mut(func(c *Candidate) { c.Guidance = "One.\nTwo." }), allowed, "one sentence"},
		"no area":         {mut(func(c *Candidate) { c.AreaGlobs = []string{" "} }), allowed, "no area"},
		"absolute glob":   {mut(func(c *Candidate) { c.AreaGlobs = []string{"/etc/**"} }), allowed, "relative"},
		"bad glob":        {mut(func(c *Candidate) { c.AreaGlobs = []string{"internal/["} }), allowed, "not a valid pattern"},
		"spaced glob":     {mut(func(c *Candidate) { c.AreaGlobs = []string{"a b"} }), allowed, "whitespace"},
		"too many globs":  {mut(func(c *Candidate) { c.AreaGlobs = []string{"a", "b", "c", "d", "e", "f"} }), allowed, "at most 5"},
		"long guidance":   {mut(func(c *Candidate) { c.Guidance = strings.Repeat("x", 300) }), allowed, "one sentence, not a paragraph"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ValidateCandidate(tc.c, tc.allowed)
			if err == nil || !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}

	ok, rej := Validate([]Candidate{base, mut(func(c *Candidate) { c.Evidence = []string{"phase:999"} })}, allowed)
	if len(ok) != 1 || len(rej) != 1 || !strings.Contains(rej[0].Reason, "phase:999") {
		t.Fatalf("Validate = %v / %v", ok, rej)
	}
}

// ── generation ──

func TestGenerateInsertsOnlyCitedCandidates(t *testing.T) {
	db := openDB(t)
	phaseID := seedRun(t, db, runUUID, 0.8, report)
	foreign := `{"title":"Invented","guidance":"Do x.","area_globs":["a/**"],"cause":"c","evidence":["file:not/in/the/diff.go"]}`
	r := &fakeRunner{out: lessonJSON(phaseID, foreign)}
	g := newGen(db, r)
	var changed int64
	g.Changed = func(id int64) { changed = id }

	out, err := g.Generate(context.Background(), phaseID, runUUID)
	if err != nil {
		t.Fatal(err)
	}
	if out.Inserted != 1 || len(out.Rejected) != 1 || out.Linked != 0 || changed == 0 {
		t.Fatalf("outcome = %+v changed=%d", out, changed)
	}
	for _, want := range []string{"[E:divergence:", "[E:file:internal/ingest/record.go]", "[E:commit:abc123]",
		"[E:forecast:prior]", "usage.cache_creation", "Areas changed but not forecast: internal/ingest"} {
		if !strings.Contains(r.prompt, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	ls, err := List(db, "")
	if err != nil || len(ls) != 1 {
		t.Fatalf("list = %v, %v", ls, err)
	}
	l := ls[0]
	if l.Status != StatusCandidate || l.AreaGlobs[0] != "internal/ingest/**" || len(l.Evidence) != 2 ||
		!strings.Contains(l.SourceParagraph, "cache_creation") || l.NormTitle == "" {
		t.Fatalf("candidate = %+v", l)
	}
	var state, rejected string
	if err := db.QueryRow(`SELECT state, rejected_json FROM lesson_generations WHERE source_phase_run = ?`, runUUID).
		Scan(&state, &rejected); err != nil || state != "done" || !strings.Contains(rejected, "not/in/the/diff.go") {
		t.Fatalf("generation row = %s %s %v", state, rejected, err)
	}

	// Idempotent per run: the settled pass must not spend tokens again.
	again, err := g.Generate(context.Background(), phaseID, runUUID)
	if err != nil || again.Skipped == "" || r.calls != 1 {
		t.Fatalf("second generate = %+v %v calls=%d", again, err, r.calls)
	}
}

func TestGenerateSkipsWithoutSurpriseOrParagraph(t *testing.T) {
	db := openDB(t)
	low := seedRun(t, db, "low", 0.3, report)
	quiet := seedRun(t, db, "quiet", 0.9, "Shipped. Nothing diverged in a notable way.")
	r := &fakeRunner{out: `{"lessons":[]}`}
	g := newGen(db, r)
	for _, tc := range []struct {
		phase int64
		uuid  string
	}{{low, "low"}, {quiet, "quiet"}, {low, "unscored"}} {
		out, err := g.Generate(context.Background(), tc.phase, tc.uuid)
		if err != nil || out.Skipped == "" {
			t.Fatalf("%s: %+v %v", tc.uuid, out, err)
		}
	}
	if r.calls != 0 {
		t.Fatalf("runner called %d times without an eligible run", r.calls)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM lesson_generations`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d generations claimed for ineligible runs", n)
	}
}

func TestGenerateRecordsFailures(t *testing.T) {
	db := openDB(t)
	phaseID := seedRun(t, db, runUUID, 0.9, report)
	g := newGen(db, &fakeRunner{out: "not json"})
	if _, err := g.Generate(context.Background(), phaseID, runUUID); err == nil {
		t.Fatal("malformed output must fail the generation")
	}
	var state, msg string
	db.QueryRow(`SELECT state, error FROM lesson_generations`).Scan(&state, &msg)
	if state != "failed" || msg == "" {
		t.Fatalf("row = %s %q", state, msg)
	}
	g2 := newGen(db, &fakeRunner{err: errors.New("boom")})
	other := seedRun(t, db, "other", 0.9, report)
	if _, err := g2.Generate(context.Background(), other, "other"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("runner error = %v", err)
	}
	g3 := newGen(db, nil)
	third := seedRun(t, db, "third", 0.9, report)
	if _, err := g3.Generate(context.Background(), third, "third"); err == nil {
		t.Fatal("no runner must fail")
	}
}

// 14.5 — the dedup path: a lesson whose identity already exists is linked, not
// duplicated.
func TestGenerateLinksExistingIdentities(t *testing.T) {
	db := openDB(t)
	// An existing retro lesson with the same identity as goodLesson's title.
	p1 := seedRun(t, db, runUUID, 0.9, report)
	var taskID int64
	db.QueryRow(`SELECT workspace_task_id FROM epic_phases WHERE id = ?`, p1).Scan(&taskID)
	retroID := mustExec(t, db, `INSERT INTO task_retros (task_id, ingested_at) VALUES (?, '2026-09-23T00:00:00Z')`, taskID)
	mustExec(t, db, `INSERT INTO retro_lessons (retro_id, seq, title, norm_title) VALUES (?, 1, ?, ?)`,
		retroID, "Cache usage lives under usage.cache_creation", wsNorm("Cache usage lives under usage.cache_creation"))

	g := newGen(db, &fakeRunner{out: lessonJSON(p1)})
	out, err := g.Generate(context.Background(), p1, runUUID)
	if err != nil || out.Inserted != 0 || out.Linked != 1 {
		t.Fatalf("first = %+v %v", out, err)
	}
	ls, _ := List(db, StatusCandidate)
	if len(ls) != 1 || ls[0].LinkedNormTitle == "" {
		t.Fatalf("candidate not linked to the retro lesson: %+v", ls)
	}
	if len(ls[0].Matches) == 0 || ls[0].Matches[0].Kind != "retro" || !ls[0].Matches[0].Exact {
		t.Fatalf("merge suggestion missing: %+v", ls[0].Matches)
	}

	// A second run learning the same lesson folds into the existing candidate.
	p2 := seedRun(t, db, "run-2", 0.9, report)
	g2 := newGen(db, &fakeRunner{out: lessonJSON(p2)})
	out, err = g2.Generate(context.Background(), p2, "run-2")
	if err != nil || out.Linked != 1 || out.Inserted != 0 {
		t.Fatalf("second = %+v %v", out, err)
	}
	var rows, rec int
	db.QueryRow(`SELECT COUNT(*), MAX(recurrences) FROM surprise_lessons`).Scan(&rows, &rec)
	if rows != 1 || rec != 2 {
		t.Fatalf("rows=%d recurrences=%d, want 1 row seen twice", rows, rec)
	}
	// Replaying the same run cannot inflate the count.
	if err := addRecurrence(db, ls[0].ID, "run-2", nil, "now"); err != nil {
		t.Fatal(err)
	}
	db.QueryRow(`SELECT recurrences FROM surprise_lessons`).Scan(&rec)
	if rec != 2 {
		t.Fatalf("replay inflated recurrences to %d", rec)
	}
}

func TestAfterScoreRunsInBackgroundOnlyWhenEligible(t *testing.T) {
	db := openDB(t)
	phaseID := seedRun(t, db, runUUID, 0.9, report)
	r := &fakeRunner{out: lessonJSON(phaseID)}
	g := newGen(db, r)
	var jobs []func()
	g.Go = func(f func()) { jobs = append(jobs, f) }

	st := &surprise.Stored{PhaseID: phaseID, SessionUUID: runUUID}
	st.Index = 0.9
	g.AfterScore(st, "backfill")
	st2 := *st
	st2.Index = 0.1
	g.AfterScore(&st2, "run-end")
	g.AfterScore(nil, "run-end")
	var nilGen *Generator
	nilGen.AfterScore(st, "run-end")
	if len(jobs) != 0 {
		t.Fatalf("%d jobs queued for ineligible scores", len(jobs))
	}
	g.AfterScore(st, "run-end")
	if len(jobs) != 1 {
		t.Fatalf("jobs = %d, want 1", len(jobs))
	}
	jobs[0]()
	if r.calls != 1 {
		t.Fatalf("runner calls = %d", r.calls)
	}
	// A panicking generation is recovered, never propagated.
	g.Runner = panicRunner{}
	g.Go = func(f func()) { f() }
	other := seedRun(t, db, "p", 0.9, report)
	g.AfterScore(&surprise.Stored{PhaseID: other, SessionUUID: "p", Result: surprise.Result{Index: 0.9}}, "run-end")
}

type panicRunner struct{}

func (panicRunner) Run(context.Context, string) (string, error) { panic("boom") }

// ── review queue ──

func seedCandidate(t *testing.T, db *sql.DB, uuid, title string) int64 {
	t.Helper()
	phaseID := seedRun(t, db, uuid, 0.9, report)
	return mustExec(t, db, `INSERT INTO surprise_lessons (source_phase_run, phase_id, seq, title, norm_title,
		guidance, area_globs, evidence_json, status, created_at, updated_at)
		VALUES (?, ?, 1, ?, ?, 'Do it.', 'internal/**', '["divergence:1"]', 'candidate', 'now', 'now')`,
		uuid, phaseID, title, wsNorm(title))
}

func TestReviewLifecycle(t *testing.T) {
	db := openDB(t)
	a := seedCandidate(t, db, "a", "Read the schema before the migration")
	b := seedCandidate(t, db, "b", "Read the schema before every migration")
	c := seedCandidate(t, db, "c", "Something unrelated entirely")

	ls, err := List(db, StatusCandidate)
	if err != nil || len(ls) != 3 {
		t.Fatalf("list = %d %v", len(ls), err)
	}
	for _, l := range ls {
		if l.ID == a && (len(l.Matches) == 0 || l.Matches[0].LessonID != b) {
			t.Fatalf("fuzzy suggestion for a = %+v", l.Matches)
		}
	}

	got, err := Accept(db, a, fixedNow)
	if err != nil || got.Status != StatusActive || got.ActivatedAt == nil {
		t.Fatalf("accept = %+v %v", got, err)
	}
	if _, err := Accept(db, a, fixedNow); !errors.Is(err, ErrState) {
		t.Fatalf("re-accept = %v, want ErrState", err)
	}
	if _, err := Accept(db, 9999, fixedNow); !errors.Is(err, ErrNotFound) {
		t.Fatalf("accept unknown = %v", err)
	}

	title, bad := "Read the schema first", "one\ntwo"
	if _, err := Edit(db, a, EditInput{Guidance: &bad}, fixedNow); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad edit = %v", err)
	}
	globs := []string{"internal/store/**"}
	got, err = Edit(db, a, EditInput{Title: &title, AreaGlobs: &globs}, fixedNow)
	if err != nil || got.Title != title || got.Status != StatusActive || got.AreaGlobs[0] != "internal/store/**" {
		t.Fatalf("edit = %+v %v", got, err)
	}

	// Merge b into the (now active) a: a absorbs b's run.
	got, err = Merge(db, b, MergeTarget{LessonID: a}, fixedNow)
	if err != nil || got.Status != StatusMerged || got.MergedIntoID == nil || *got.MergedIntoID != a {
		t.Fatalf("merge = %+v %v", got, err)
	}
	target, _ := Get(db, a)
	if target.Recurrences != 2 || target.Status != StatusActive {
		t.Fatalf("target after merge = %+v", target)
	}
	if _, err := Merge(db, c, MergeTarget{LessonID: c}, fixedNow); !errors.Is(err, ErrInvalid) {
		t.Fatalf("self merge = %v", err)
	}
	if _, err := Merge(db, c, MergeTarget{LessonID: b}, fixedNow); !errors.Is(err, ErrInvalid) {
		t.Fatalf("merge into merged = %v", err)
	}
	if _, err := Merge(db, c, MergeTarget{NormTitle: "nope"}, fixedNow); !errors.Is(err, ErrInvalid) {
		t.Fatalf("merge into unknown retro = %v", err)
	}
	if _, err := Merge(db, c, MergeTarget{}, fixedNow); !errors.Is(err, ErrInvalid) {
		t.Fatalf("merge without target = %v", err)
	}
	if _, err := Merge(db, a, MergeTarget{LessonID: c}, fixedNow); !errors.Is(err, ErrState) {
		t.Fatalf("merge an active lesson = %v", err)
	}

	got, err = Dismiss(db, c, "one-off", fixedNow)
	if err != nil || got.Status != StatusDismissed || got.RetireReason == nil || *got.RetireReason != "one-off" {
		t.Fatalf("dismiss = %+v %v", got, err)
	}
	if _, err := Edit(db, c, EditInput{Title: &title}, fixedNow); !errors.Is(err, ErrState) {
		t.Fatalf("edit dismissed = %v", err)
	}
	if _, err := Retire(db, c, "x", fixedNow); !errors.Is(err, ErrState) {
		t.Fatalf("retire a dismissed lesson = %v", err)
	}
	got, err = Retire(db, a, "absorbed into the skill", fixedNow)
	if err != nil || got.Status != StatusRetired {
		t.Fatalf("retire = %+v %v", got, err)
	}
	if active, _ := List(db, StatusActive); len(active) != 0 {
		t.Fatalf("active after retire = %d", len(active))
	}
}

func TestMergeIntoRetroIdentity(t *testing.T) {
	db := openDB(t)
	id := seedCandidate(t, db, "a", "Pin the model in headless runs")
	var taskID int64
	db.QueryRow(`SELECT workspace_task_id FROM epic_phases LIMIT 1`).Scan(&taskID)
	retroID := mustExec(t, db, `INSERT INTO task_retros (task_id, ingested_at) VALUES (?, 'now')`, taskID)
	mustExec(t, db, `INSERT INTO retro_lessons (retro_id, seq, title, norm_title) VALUES (?, 1, 'Pin models', 'pin models')`, retroID)
	got, err := Merge(db, id, MergeTarget{NormTitle: "pin models"}, fixedNow)
	if err != nil || got.Status != StatusMerged || got.LinkedNormTitle != "pin models" {
		t.Fatalf("merge retro = %+v %v", got, err)
	}
	// 14.1: a retro lesson written by a human is already accepted knowledge.
	var status, area string
	db.QueryRow(`SELECT status, area_globs FROM retro_lessons WHERE retro_id = ?`, retroID).Scan(&status, &area)
	if status != "active" || area != "*" {
		t.Fatalf("retro lesson defaults = %q %q", status, area)
	}
}
