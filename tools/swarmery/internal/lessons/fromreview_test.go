package lessons

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

// seedReviewGroups loads the reference plan's backfilled review findings
// (testdata/fixtures/lessons/review-groups.sql).
func seedReviewGroups(t *testing.T, db *sql.DB) {
	t.Helper()
	b, err := os.ReadFile("../../testdata/fixtures/lessons/review-groups.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(b)); err != nil {
		t.Fatalf("load the review-groups seed: %v", err)
	}
}

// findingMarkerRe finds the finding markers a review prompt offers.
var findingMarkerRe = regexp.MustCompile(`\[E:(finding:\d+)\]`)

// citingRunner is a well-behaved model: it drafts one lesson citing the first two
// finding ids its prompt offers — or, when cite is set, exactly those ids.
type citingRunner struct {
	calls   int
	prompts []string
	cite    []string
	err     error
}

func (r *citingRunner) Run(_ context.Context, prompt string) (string, error) {
	r.calls++
	r.prompts = append(r.prompts, prompt)
	if r.err != nil {
		return "", r.err
	}
	ev := r.cite
	if ev == nil {
		for _, m := range findingMarkerRe.FindAllStringSubmatch(prompt, 2) {
			ev = append(ev, m[1])
		}
	}
	evJSON, _ := json.Marshal(ev)
	return fmt.Sprintf(`{"lessons":[{"title":"Check the call path the findings name","guidance":"Before finishing, trace each changed handler's error path end to end.","area_globs":["somewhere/else/**"],"cause":"The same mistake recurred across phases.","evidence":%s}]}`, evJSON), nil
}

// reviewNow is the review tests' clock: after the seed's findings were first seen
// (2026-10-22T17:35Z), as a real dismissal would be.
var reviewNow = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

func newProposer(db *sql.DB, r Runner, now time.Time) *ReviewProposer {
	return &ReviewProposer{DB: db, Runner: r, Model: "m", Enabled: true, Now: func() time.Time { return now }}
}

type groupWant struct {
	typ, area string
	findings  []int64
	phases    []int64
}

// The reference plan's repeats: three correctness groups and one quality group.
// README.md (contract, phase 2) sits two levels above internal/api (contract,
// phase 4), so the depth bound keeps them apart; every other type/area lives in
// one phase only.
var referenceGroups = []groupWant{
	{"correctness", "tools/swarmery/internal/api", []int64{8, 9, 10}, []int64{903, 904}},
	{"correctness", "tools/swarmery/internal/repoprovider", []int64{1, 3, 5, 21}, []int64{901, 902, 906}},
	{"correctness", "tools/swarmery/internal/wsingest", []int64{7, 15}, []int64{903, 904}},
	{"quality", "tools/swarmery/internal/api", []int64{18, 22}, []int64{905, 906}},
}

func TestGroupFindingsOnTheReferencePlan(t *testing.T) {
	db := openDB(t)
	seedReviewGroups(t, db)
	groups, err := GroupFindings(db)
	if err != nil {
		t.Fatal(err)
	}
	var got []groupWant
	for _, g := range groups {
		got = append(got, groupWant{g.Type, g.Area, g.FindingIDs, g.PhaseIDs})
		if g.Phases() < MinReviewPhases {
			t.Errorf("group %s @ %s spans %d phase(s)", g.Type, g.Area, g.Phases())
		}
	}
	if !reflect.DeepEqual(got, referenceGroups) {
		t.Fatalf("groups:\n got %+v\nwant %+v", got, referenceGroups)
	}
	if globs := groups[1].AreaGlobs(); !reflect.DeepEqual(globs, []string{"tools/swarmery/internal/repoprovider/**"}) {
		t.Errorf("area globs = %v", globs)
	}
}

func TestGroupFindingsJoinsModuleRelativePathsAndSkipsUntyped(t *testing.T) {
	db := openDB(t)
	seedReviewGroups(t, db)
	// A module-relative path is the same place as its repo-relative twin; a
	// finding without a type, or without a file, cannot be placed.
	mustExec(t, db, `INSERT INTO review_findings (id, workspace_task_id, phase_id, doc_path, round, finding_id, type, file, source, created_at)
		VALUES (30, 900, 908, '/ws/plan/phase-8-device-flow-and-pat.md', '1', 'R1-1', 'contract', 'internal/api/phaserun.go', 'interactive', '2026-10-22T18:00:00Z'),
		       (31, 900, 907, '/ws/plan/phase-7-status-poller.md', '1', 'R1-1', NULL, 'internal/api/x.go', 'interactive', '2026-10-22T18:00:00Z'),
		       (32, 900, 907, '/ws/plan/phase-7-status-poller.md', '1', 'R1-2', 'contract', '', 'interactive', '2026-10-22T18:00:00Z')`)
	groups, err := GroupFindings(db)
	if err != nil {
		t.Fatal(err)
	}
	var contract *Group
	for i := range groups {
		if groups[i].Type == "contract" {
			contract = &groups[i]
		}
	}
	if contract == nil {
		t.Fatalf("no contract group: %+v", groups)
	}
	if !reflect.DeepEqual(contract.FindingIDs, []int64{13, 30}) || contract.Area != "internal/api" {
		t.Errorf("contract group = %s %v; want internal/api [13 30] (README.md stays out)", contract.Area, contract.FindingIDs)
	}
}

func countReviewLessons(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM surprise_lessons WHERE source = 'review'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

type reviewRow struct {
	id                         int64
	status, title, guidance    string
	globs, evidence, typ, area string
	key                        string
	seq, recurrences           int
	phaseID                    int64
}

func readReviewRow(t *testing.T, db *sql.DB, typ, area string) reviewRow {
	t.Helper()
	var r reviewRow
	if err := db.QueryRow(`SELECT id, status, title, guidance, area_globs, evidence_json, review_type, review_area,
		source_phase_run, seq, recurrences, phase_id
		FROM surprise_lessons WHERE source = 'review' AND review_type = ? AND review_area = ? ORDER BY id DESC LIMIT 1`, typ, area).
		Scan(&r.id, &r.status, &r.title, &r.guidance, &r.globs, &r.evidence, &r.typ, &r.area, &r.key, &r.seq,
			&r.recurrences, &r.phaseID); err != nil {
		t.Fatalf("review lesson %s @ %s: %v", typ, area, err)
	}
	return r
}

func evidenceOf(ids ...int64) string {
	var out []string
	for _, id := range ids {
		out = append(out, FindingRef(id))
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func TestProposeFromReviewOneCandidatePerGroupAndNoneOnASecondRun(t *testing.T) {
	db := openDB(t)
	seedReviewGroups(t, db)
	r := &citingRunner{}
	p := newProposer(db, r, reviewNow)

	out, err := p.Tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out.Groups != 4 || out.Inserted != 4 || out.Extended != 0 || out.Drafted != 4 || r.calls != 4 {
		t.Fatalf("first tick = %+v (runner calls %d); want 4 groups, 4 inserted, 4 drafted", out, r.calls)
	}
	for _, w := range referenceGroups {
		row := readReviewRow(t, db, w.typ, w.area)
		if row.status != StatusCandidate {
			t.Errorf("%s @ %s: status %q", w.typ, w.area, row.status)
		}
		if row.evidence != evidenceOf(w.findings...) {
			t.Errorf("%s @ %s: evidence %s, want every finding of the group %s", w.typ, w.area, row.evidence, evidenceOf(w.findings...))
		}
		if row.globs != w.area+"/**" {
			t.Errorf("%s @ %s: area_globs %q — the group's own, not the model's", w.typ, w.area, row.globs)
		}
		if row.recurrences != len(w.phases) || row.phaseID != w.phases[len(w.phases)-1] {
			t.Errorf("%s @ %s: recurrences %d phase %d", w.typ, w.area, row.recurrences, row.phaseID)
		}
		if row.guidance != "Before finishing, trace each changed handler's error path end to end." {
			t.Errorf("%s @ %s: guidance %q", w.typ, w.area, row.guidance)
		}
		if row.key != "review:"+w.typ+":"+w.area || row.seq != 1 {
			t.Errorf("%s @ %s: key %q seq %d", w.typ, w.area, row.key, row.seq)
		}
	}
	// The draft read the findings, each behind its marker.
	if !strings.Contains(r.prompts[0], "[E:finding:8]") || !strings.Contains(r.prompts[0], "Type: correctness. Area: tools/swarmery/internal/api.") {
		t.Errorf("draft prompt lacks the group's findings:\n%s", r.prompts[0])
	}

	out, err = p.Tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out.Inserted != 0 || out.Extended != 0 || r.calls != 4 {
		t.Fatalf("second tick = %+v (runner calls %d); want nothing new and no model spend", out, r.calls)
	}
	if n := countReviewLessons(t, db); n != 4 {
		t.Fatalf("%d review lessons after two ticks, want 4", n)
	}
}

func TestProposeFromReviewExtendsTheGroupOnALaterMatch(t *testing.T) {
	db := openDB(t)
	seedReviewGroups(t, db)
	r := &citingRunner{}
	p := newProposer(db, r, reviewNow)
	if _, err := p.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A third phase repeats correctness in internal/api, in a subdirectory.
	mustExec(t, db, `INSERT INTO review_findings (id, workspace_task_id, phase_id, doc_path, round, finding_id, type, file, source, created_at)
		VALUES (40, 900, 907, '/ws/plan/phase-7-status-poller.md', '1', 'R1-1', 'correctness', 'tools/swarmery/internal/api/landing/poll.go', 'daemon', '2026-10-23T00:00:00Z')`)
	out, err := p.Tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out.Inserted != 0 || out.Extended != 1 || r.calls != 4 {
		t.Fatalf("tick = %+v (runner calls %d); want one extension and no new draft", out, r.calls)
	}
	row := readReviewRow(t, db, "correctness", "tools/swarmery/internal/api")
	if row.evidence != evidenceOf(8, 9, 10, 40) || row.recurrences != 3 {
		t.Errorf("after the later match: evidence %s recurrences %d", row.evidence, row.recurrences)
	}
	if n := countReviewLessons(t, db); n != 4 {
		t.Errorf("%d review lessons, want 4 — a later match extends, never inserts", n)
	}
}

func TestProposeFromReviewKeepsTheRepeatWhenTheDraftFails(t *testing.T) {
	db := openDB(t)
	seedReviewGroups(t, db)
	r := &citingRunner{err: errors.New("claude: model offline")}
	p := newProposer(db, r, reviewNow)
	out, err := p.Tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out.Inserted != 4 || out.Drafted != 0 {
		t.Fatalf("tick = %+v; want 4 candidates even without a model", out)
	}
	row := readReviewRow(t, db, "correctness", "tools/swarmery/internal/wsingest")
	if row.guidance != DraftPendingGuidance || row.title != "Repeated correctness findings in tools/swarmery/internal/wsingest" {
		t.Fatalf("placeholder row: title %q guidance %q", row.title, row.guidance)
	}
	// The group grows and the model is back: the extension drafts it.
	r.err = nil
	mustExec(t, db, `INSERT INTO review_findings (id, workspace_task_id, phase_id, doc_path, round, finding_id, type, file, source, created_at)
		VALUES (41, 900, 908, '/ws/plan/phase-8-device-flow-and-pat.md', '1', 'R1-1', 'correctness', 'tools/swarmery/internal/wsingest/epics.go', 'daemon', '2026-10-23T00:00:00Z')`)
	out, err = p.Tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out.Extended != 1 || out.Drafted != 1 {
		t.Fatalf("tick = %+v; want the grown group extended and drafted", out)
	}
	row = readReviewRow(t, db, "correctness", "tools/swarmery/internal/wsingest")
	if row.guidance == DraftPendingGuidance || row.evidence != evidenceOf(7, 15, 41) {
		t.Errorf("after the redraft: guidance %q evidence %s", row.guidance, row.evidence)
	}
	// An unchanged placeholder group is never re-drafted: no new findings, no spend.
	calls := r.calls
	if _, err := p.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.calls != calls {
		t.Errorf("an unchanged tick spent %d model call(s)", r.calls-calls)
	}
}

// The fabrication check covers the review shape: a lesson citing a finding id
// that is not in its input is rejected, and the candidate keeps its placeholder.
func TestDraftReviewRejectsAnInventedFindingID(t *testing.T) {
	db := openDB(t)
	seedReviewGroups(t, db)
	groups, err := GroupFindings(db)
	if err != nil {
		t.Fatal(err)
	}
	in := NewReviewInput(groups[0]) // correctness @ internal/api: findings 8, 9, 10
	if !in.Allowed["finding:8"] || in.Allowed["finding:7"] || len(in.Allowed) != 3 {
		t.Fatalf("allowed = %v", in.Allowed)
	}
	_, err = ValidateCandidate(Candidate{Title: "t", Guidance: "Do it.", Cause: "c", AreaGlobs: in.AreaGlobs,
		Evidence: []string{"finding:8", "finding:999"}}, in.Allowed)
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "finding:999") {
		t.Fatalf("invented id: err = %v", err)
	}
	// A finding of ANOTHER group is just as foreign to this input.
	if _, err := ValidateCandidate(Candidate{Title: "t", Guidance: "Do it.", Cause: "c", AreaGlobs: in.AreaGlobs,
		Evidence: []string{"finding:7"}}, in.Allowed); !errors.Is(err, ErrInvalid) {
		t.Fatalf("another group's finding: err = %v", err)
	}

	r := &citingRunner{cite: []string{"finding:8", "finding:999"}}
	c, ok, rejected, err := DraftReview(context.Background(), r, in)
	if err != nil || ok || len(rejected) != 1 || !strings.Contains(rejected[0].Reason, "not in its input") {
		t.Fatalf("DraftReview = %+v, %t, %+v, %v; want the lesson rejected", c, ok, rejected, err)
	}

	p := newProposer(db, r, reviewNow)
	pr, err := p.ProposeFromReview(context.Background(), groups[0])
	if err != nil {
		t.Fatal(err)
	}
	if pr.Action != ProposalInserted || pr.Drafted || len(pr.Rejected) != 1 {
		t.Fatalf("proposal = %+v", pr)
	}
	row := readReviewRow(t, db, "correctness", "tools/swarmery/internal/api")
	if row.guidance != DraftPendingGuidance || strings.Contains(row.evidence, "999") {
		t.Errorf("a fabricated draft reached the row: guidance %q evidence %s", row.guidance, row.evidence)
	}
}

func TestDismissMutesTheGroupForThirtyDays(t *testing.T) {
	db := openDB(t)
	seedReviewGroups(t, db)
	r := &citingRunner{}
	p := newProposer(db, r, reviewNow)
	if _, err := p.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	row := readReviewRow(t, db, "quality", "tools/swarmery/internal/api")
	l, err := Dismiss(db, row.id, "noise", reviewNow)
	if err != nil || l.Status != StatusDismissed || l.Source != SourceReview {
		t.Fatalf("Dismiss = %+v, %v", l, err)
	}
	var at, until string
	var lessonID int64
	if err := db.QueryRow(`SELECT muted_at, muted_until, lesson_id FROM lesson_mutes WHERE type = 'quality' AND area = 'tools/swarmery/internal/api'`).
		Scan(&at, &until, &lessonID); err != nil {
		t.Fatalf("no mute after the dismissal: %v", err)
	}
	if at != stamp(reviewNow) || until != stamp(reviewNow.AddDate(0, 0, 30)) || lessonID != row.id {
		t.Errorf("mute = %s .. %s (lesson %d)", at, until, lessonID)
	}

	// Within the 30 days nothing re-proposes it — not even a new finding.
	mustExec(t, db, `INSERT INTO review_findings (id, workspace_task_id, phase_id, doc_path, round, finding_id, type, file, source, created_at)
		VALUES (50, 900, 907, '/ws/plan/phase-7-status-poller.md', '1', 'R1-1', 'quality', 'tools/swarmery/internal/api/epics.go', 'daemon', ?)`,
		stamp(reviewNow.Add(24*time.Hour)))
	p.Now = func() time.Time { return reviewNow.AddDate(0, 0, 10) }
	out, err := p.Tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out.Inserted != 0 || out.Muted != 1 {
		t.Fatalf("tick inside the mute = %+v", out)
	}
	// After it: the finding first seen after the dismissal lifts it.
	p.Now = func() time.Time { return reviewNow.AddDate(0, 0, 31) }
	if out, err = p.Tick(context.Background()); err != nil || out.Inserted != 1 {
		t.Fatalf("tick after the mute with a newer finding = %+v, %v", out, err)
	}
	again := readReviewRow(t, db, "quality", "tools/swarmery/internal/api")
	if again.id == row.id || again.seq != 2 || again.status != StatusCandidate {
		t.Errorf("re-proposal = %+v", again)
	}
	// Dismissing a SURPRISE lesson mutes nothing.
	sid := seedCandidate(t, db, "surprise-run", "Unrelated surprise lesson")
	if _, err := Dismiss(db, sid, "", reviewNow); err != nil {
		t.Fatal(err)
	}
	var mutes int
	if err := db.QueryRow(`SELECT COUNT(*) FROM lesson_mutes`).Scan(&mutes); err != nil || mutes != 1 {
		t.Errorf("%d mutes after dismissing a surprise lesson, want 1", mutes)
	}
}

func TestAnExpiredMuteWithoutNewFindingsStillHolds(t *testing.T) {
	db := openDB(t)
	seedReviewGroups(t, db)
	p := newProposer(db, &citingRunner{}, reviewNow)
	if _, err := p.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	row := readReviewRow(t, db, "correctness", "tools/swarmery/internal/wsingest")
	if _, err := Dismiss(db, row.id, "", reviewNow); err != nil {
		t.Fatal(err)
	}
	p.Now = func() time.Time { return reviewNow.AddDate(0, 0, 45) }
	out, err := p.Tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out.Inserted != 0 || out.Muted != 1 {
		t.Fatalf("tick = %+v; the same old findings must not come back on their own", out)
	}
}

func TestAMergedReviewCandidateForwardsItsGroup(t *testing.T) {
	db := openDB(t)
	seedReviewGroups(t, db)
	p := newProposer(db, &citingRunner{}, reviewNow)
	if _, err := p.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	src := readReviewRow(t, db, "correctness", "tools/swarmery/internal/wsingest")
	target := seedCandidate(t, db, "target-run", "Keep wsingest upserts complete")
	if _, err := Merge(db, src.id, MergeTarget{LessonID: target}, reviewNow); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO review_findings (id, workspace_task_id, phase_id, doc_path, round, finding_id, type, file, source, created_at)
		VALUES (60, 900, 908, '/ws/plan/phase-8-device-flow-and-pat.md', '1', 'R1-1', 'correctness', 'tools/swarmery/internal/wsingest/review.go', 'daemon', '2026-10-23T00:00:00Z')`)
	out, err := p.Tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out.Inserted != 0 || out.Extended != 1 {
		t.Fatalf("tick = %+v; the merge target absorbs the group", out)
	}
	tgt, err := Get(db, target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(tgt.Evidence, ","), "finding:60") || tgt.Source != SourceSurprise {
		t.Errorf("target = source %q evidence %v", tgt.Source, tgt.Evidence)
	}
	if len(tgt.Findings) != 3 || tgt.Findings[0].PhaseSeq != 3 || tgt.Findings[0].ProjectSlug != "swarmery" {
		t.Errorf("target findings = %+v", tgt.Findings)
	}
}

func TestLessonCarriesSourceAndFindingLinks(t *testing.T) {
	db := openDB(t)
	seedReviewGroups(t, db)
	p := newProposer(db, &citingRunner{}, reviewNow)
	if _, err := p.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	ls, err := List(db, StatusCandidate)
	if err != nil {
		t.Fatal(err)
	}
	var api *Lesson
	for i := range ls {
		if ls[i].ReviewType == "quality" {
			api = &ls[i]
		}
	}
	if api == nil {
		t.Fatalf("no quality lesson in %+v", ls)
	}
	want := []FindingLink{
		{ID: 18, TaskID: 900, ProjectSlug: "swarmery", PlanID: "2026-10-09-phase-landing-repo-provider", PhaseID: 905,
			PhaseSeq: 5, PhaseName: "Phase 5 — Plans review UI", Round: "1", FindingID: "R1-3", Type: "quality",
			File: "tools/swarmery/internal/api/vcs.go"},
		{ID: 22, TaskID: 900, ProjectSlug: "swarmery", PlanID: "2026-10-09-phase-landing-repo-provider", PhaseID: 906,
			PhaseSeq: 6, PhaseName: "Phase 6 — GitLab provider", Round: "1", FindingID: "R1-2", Type: "quality",
			File: "tools/swarmery/internal/api/tasks_diff.go"},
	}
	if api.Source != SourceReview || api.ReviewArea != "tools/swarmery/internal/api" || !reflect.DeepEqual(api.Findings, want) {
		t.Errorf("lesson = source %q area %q findings %+v", api.Source, api.ReviewArea, api.Findings)
	}
	// A surprise lesson reads as one, with no findings.
	sid := seedCandidate(t, db, "s-run", "A surprise lesson")
	sl, err := Get(db, sid)
	if err != nil || sl.Source != SourceSurprise || sl.Findings == nil || len(sl.Findings) != 0 {
		t.Errorf("surprise lesson = %+v, %v", sl, err)
	}
}

func TestPlaceholderLessonsAreNeverInjected(t *testing.T) {
	db := openDB(t)
	seedActive(t, db, 1, 1, DraftPendingGuidance, "internal/api/**", "2026-10-01T00:00:00Z")
	id := seedActive(t, db, 1, 2, "Trace the error path.", "internal/api/**", "2026-10-01T00:00:00Z")
	sel, err := Select(db, Scope{Areas: []string{"internal/api"}}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.Lessons) != 1 || sel.Lessons[0].ID != id {
		t.Errorf("selected %+v; a (draft pending) lesson must not be injected", sel.Lessons)
	}
}

func TestTickIsOffWhenLessonsAreOff(t *testing.T) {
	db := openDB(t)
	seedReviewGroups(t, db)
	p := newProposer(db, &citingRunner{}, reviewNow)
	p.Enabled = false
	if out, err := p.Tick(context.Background()); err != nil || out != (TickOutcome{}) || countReviewLessons(t, db) != 0 {
		t.Fatalf("disabled tick = %+v, %v", out, err)
	}
}

func TestKickCoalescesIntoOneDelayedTick(t *testing.T) {
	db := openDB(t)
	seedReviewGroups(t, db)
	p := newProposer(db, &citingRunner{}, reviewNow)
	var scheduled []func()
	var delays []time.Duration
	p.AfterFunc = func(d time.Duration, f func()) {
		delays = append(delays, d)
		scheduled = append(scheduled, f)
	}
	p.Kick()
	p.Kick()
	if len(scheduled) != 1 || delays[0] != defaultKickDelay {
		t.Fatalf("%d tick(s) scheduled (delays %v); want one after %s", len(scheduled), delays, defaultKickDelay)
	}
	scheduled[0]()
	if n := countReviewLessons(t, db); n != 4 {
		t.Fatalf("the kicked tick wrote %d lessons, want 4", n)
	}
	p.Kick()
	if len(scheduled) != 2 {
		t.Errorf("a kick after the tick ran was swallowed")
	}
}

// A model that looks and returns no lesson leaves a placeholder that says so.
func TestADeclinedDraftSaysSoOnThePlaceholder(t *testing.T) {
	db := openDB(t)
	seedReviewGroups(t, db)
	groups, err := GroupFindings(db)
	if err != nil {
		t.Fatal(err)
	}
	p := newProposer(db, &fakeRunner{out: `{"lessons":[]}`}, reviewNow)
	pr, err := p.ProposeFromReview(context.Background(), groups[2])
	if err != nil || pr.Action != ProposalInserted || pr.DraftErr != noLessonReason {
		t.Fatalf("proposal = %+v, %v", pr, err)
	}
	var guidance, cause string
	if err := db.QueryRow(`SELECT guidance, cause FROM surprise_lessons WHERE id = ?`, pr.LessonID).Scan(&guidance, &cause); err != nil {
		t.Fatal(err)
	}
	if guidance != DraftPendingGuidance || !strings.Contains(cause, "found no thread they share") {
		t.Errorf("placeholder: guidance %q cause %q", guidance, cause)
	}
}
