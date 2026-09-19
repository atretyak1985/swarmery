package advisor

// agent-memory phase 4 — R11 tests. The threshold IS the rule, so the pair that
// matters is "2 tasks stays silent" / "3 tasks fires"; the rest pin the exact
// detail string, the distinct-TASK grain (two lessons in one retro are one
// occurrence), the '' guard, and the persistence path — which is also what
// proves migration 0072 widened the target_kind CHECK to accept 'skill'.

import (
	"database/sql"
	"strings"
	"testing"
)

// seedLessonTask inserts one task, its retro header, and its lessons.
// daysAgo places the task inside (or outside) the evaluation window.
func seedLessonTask(t *testing.T, db *sql.DB, id int, externalID string, daysAgo int, lessons ...[3]string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO tasks
		(id, project_id, title, prompt, status, created_at, started_at, source, external_id)
		VALUES (?, 1, ?, 'goal', 'done', ?, ?, 'workspace', ?)`,
		id, externalID, ago(daysAgo), ago(daysAgo), externalID)
	mustExec(t, db, `INSERT INTO task_retros (id, task_id, ingested_at) VALUES (?, ?, ?)`,
		id, id, ago(daysAgo))
	for i, l := range lessons {
		// l = {title, norm_title, action}; action "" is stored as NULL.
		var action any
		if l[2] != "" {
			action = l[2]
		}
		mustExec(t, db, `INSERT INTO retro_lessons (retro_id, seq, title, action, norm_title)
			VALUES (?, ?, ?, ?, ?)`, id, i+1, l[0], action, l[1])
	}
}

const (
	syncTitle = "Sync-cache before build"
	syncNorm  = "sync cache before build"
)

func syncLesson(title, action string) [3]string { return [3]string{title, syncNorm, action} }

// TestR11SilentBelowThreshold: the same lesson in TWO tasks is a coincidence.
func TestR11SilentBelowThreshold(t *testing.T) {
	db := testDB(t)
	seedLessonTask(t, db, 1, "2026-07-20-task-a", 1, syncLesson(syncTitle, "run sync-cache.sh"))
	seedLessonTask(t, db, 2, "2026-07-19-task-b", 2, syncLesson("sync the cache before the build", ""))

	fs, err := r11RecurringLesson(db, evalWindow())
	if err != nil {
		t.Fatalf("r11RecurringLesson: %v", err)
	}
	if len(fs) != 0 {
		t.Fatalf("findings = %d, want 0 at %d tasks (threshold is %d): %+v", len(fs), 2, R11MinTasks, fs)
	}
}

// TestR11FiresAtThreshold: three distinct tasks make it a pattern. Pins the
// finding identity, the exact detail string, and the evidence shape.
func TestR11FiresAtThreshold(t *testing.T) {
	db := testDB(t)
	seedLessonTask(t, db, 1, "2026-07-20-task-a", 1, syncLesson(syncTitle, "add sync-cache to the build skill"))
	seedLessonTask(t, db, 2, "2026-07-19-task-b", 2, syncLesson("sync the cache before the build", "older action"))
	seedLessonTask(t, db, 3, "2026-07-18-task-c", 3, syncLesson("SYNC-CACHE, BEFORE BUILD!", ""))

	fs, err := r11RecurringLesson(db, evalWindow())
	if err != nil {
		t.Fatalf("r11RecurringLesson: %v", err)
	}
	if len(fs) != 1 {
		t.Fatalf("findings = %d, want 1: %+v", len(fs), fs)
	}
	f := fs[0]
	if f.rule != "R11" || f.targetKind != "skill" || f.target != syncNorm {
		t.Fatalf("finding identity = %s/%s/%s, want R11/skill/%s",
			f.rule, f.targetKind, f.target, syncNorm)
	}
	if f.title != "Recurring lesson: "+syncTitle {
		t.Errorf("title = %q, want %q", f.title, "Recurring lesson: "+syncTitle)
	}

	// Exact detail: the newest wording wins, tasks are newest first, and the
	// newest occurrence's action is the one quoted.
	want := `"Sync-cache before build" recurred in 3 tasks: ` +
		`2026-07-20-task-a, 2026-07-19-task-b, 2026-07-18-task-c.` +
		` Latest action: add sync-cache to the build skill.` +
		` A lesson learned this often belongs in the procedure the next run reads,` +
		` not in a finished task's retro.`
	if f.detail != want {
		t.Errorf("detail mismatch:\n got %q\nwant %q", f.detail, want)
	}

	counts, ok := f.evidence["counts"].(map[string]int)
	if !ok || counts["tasks"] != 3 {
		t.Errorf("evidence counts = %+v, want tasks=3", f.evidence["counts"])
	}
	tasks, ok := f.evidence["tasks"].([]string)
	if !ok || len(tasks) != 3 || tasks[0] != "2026-07-20-task-a" {
		t.Errorf("evidence tasks = %+v, want 3 ids newest first", f.evidence["tasks"])
	}
}

// TestR11CountsDistinctTasksNotRows: one retro repeating the same lesson twice
// must not count as two — otherwise a single sloppy retro doc manufactures a
// finding on its own.
func TestR11CountsDistinctTasksNotRows(t *testing.T) {
	db := testDB(t)
	seedLessonTask(t, db, 1, "2026-07-20-task-a", 1,
		syncLesson(syncTitle, ""), syncLesson("sync the cache before the build", ""))
	seedLessonTask(t, db, 2, "2026-07-19-task-b", 2, syncLesson(syncTitle, ""))

	fs, err := r11RecurringLesson(db, evalWindow())
	if err != nil {
		t.Fatalf("r11RecurringLesson: %v", err)
	}
	if len(fs) != 0 {
		t.Fatalf("findings = %d, want 0 — 3 rows but only 2 tasks: %+v", len(fs), fs)
	}
}

// TestR11IgnoresUnfoldedAndOutOfWindow: ” is the "not folded yet" marker, never
// an identity — grouping by it would pile every unrelated pre-0069 lesson into
// one enormous bogus finding. A task outside the window does not count either.
func TestR11IgnoresUnfoldedAndOutOfWindow(t *testing.T) {
	db := testDB(t)
	seedLessonTask(t, db, 1, "2026-07-20-task-a", 1, [3]string{"Unfolded one", "", ""})
	seedLessonTask(t, db, 2, "2026-07-19-task-b", 2, [3]string{"Unfolded two", "", ""})
	seedLessonTask(t, db, 3, "2026-07-18-task-c", 3, [3]string{"Unfolded three", "", ""})
	// Three real occurrences, but one of them is 30 days old — outside the window.
	seedLessonTask(t, db, 4, "2026-07-17-task-d", 4, syncLesson(syncTitle, ""))
	seedLessonTask(t, db, 5, "2026-07-16-task-e", 5, syncLesson(syncTitle, ""))
	seedLessonTask(t, db, 6, "2026-06-20-task-f", 30, syncLesson(syncTitle, ""))

	fs, err := r11RecurringLesson(db, evalWindow())
	if err != nil {
		t.Fatalf("r11RecurringLesson: %v", err)
	}
	if len(fs) != 0 {
		t.Fatalf("findings = %d, want 0: %+v", len(fs), fs)
	}
}

// TestR11DetailCapsSlugs: past R11MaxSlugs the detail names the first five and
// says how many more, so the line stays readable while the count stays honest.
func TestR11DetailCapsSlugs(t *testing.T) {
	db := testDB(t)
	for i := 1; i <= 7; i++ {
		seedLessonTask(t, db, i, "task-"+string(rune('a'+i-1)), i, syncLesson(syncTitle, ""))
	}
	fs, err := r11RecurringLesson(db, evalWindow())
	if err != nil {
		t.Fatalf("r11RecurringLesson: %v", err)
	}
	if len(fs) != 1 {
		t.Fatalf("findings = %d, want 1", len(fs))
	}
	if !strings.Contains(fs[0].detail, "recurred in 7 tasks") {
		t.Errorf("detail lost the true count: %q", fs[0].detail)
	}
	if !strings.Contains(fs[0].detail, "(+2 more)") {
		t.Errorf("detail did not cap the slug list at %d: %q", R11MaxSlugs, fs[0].detail)
	}
	if strings.Contains(fs[0].detail, "task-g") {
		t.Errorf("detail listed more than %d slugs: %q", R11MaxSlugs, fs[0].detail)
	}
}

// TestR11PersistsAsSkillKind is the migration-0072 proof: upsert writes the row
// with target_kind='skill'. Before the CHECK was widened this failed the whole
// advisor pass, exactly as R7/R9/R10 did before their own migrations.
func TestR11PersistsAsSkillKind(t *testing.T) {
	db := testDB(t)
	seedLessonTask(t, db, 1, "2026-07-20-task-a", 1, syncLesson(syncTitle, "do the thing"))
	seedLessonTask(t, db, 2, "2026-07-19-task-b", 2, syncLesson(syncTitle, ""))
	seedLessonTask(t, db, 3, "2026-07-18-task-c", 3, syncLesson(syncTitle, ""))

	stats, err := Run(db, testNow)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Proposed < 1 {
		t.Fatalf("stats = %s, want at least one proposal", stats)
	}
	var kind, target, dedup string
	if err := db.QueryRow(
		`SELECT target_kind, target, dedup_key FROM recommendations WHERE rule = 'R11'`).
		Scan(&kind, &target, &dedup); err != nil {
		t.Fatalf("R11 row: %v", err)
	}
	if kind != "skill" {
		t.Errorf("target_kind = %q, want %q (phase 5 routes on this)", kind, "skill")
	}
	if target != syncNorm {
		t.Errorf("target = %q, want the norm_title %q", target, syncNorm)
	}
	if dedup != "R11:"+syncNorm {
		t.Errorf("dedup_key = %q, want %q", dedup, "R11:"+syncNorm)
	}
}

// TestR11MetricValue: BaselineFor must know R11, or accepting the card 500s on
// "unknown rule". The metric is distinct tasks in the window, lower is better.
func TestR11MetricValue(t *testing.T) {
	db := testDB(t)
	seedLessonTask(t, db, 1, "2026-07-20-task-a", 1, syncLesson(syncTitle, ""))
	seedLessonTask(t, db, 2, "2026-07-19-task-b", 2, syncLesson(syncTitle, ""))
	seedLessonTask(t, db, 3, "2026-07-18-task-c", 3, syncLesson(syncTitle, ""))

	name, v, ok, err := metricValue(db, "R11", syncNorm, evalWindow())
	if err != nil || !ok {
		t.Fatalf("metricValue = ok %v, err %v", ok, err)
	}
	if name != "lesson_task_count" || v != 3 {
		t.Fatalf("metric = %s/%v, want lesson_task_count/3", name, v)
	}
	// Lower is better: two fewer tasks re-learning it reads as an improvement.
	if imp := relImprovement("R11", v, 1); imp <= 0 {
		t.Errorf("relImprovement(3 -> 1) = %v, want > 0 (lower is better)", imp)
	}
	if _, err := BaselineFor(db, "R11", syncNorm, testNow); err != nil {
		t.Fatalf("BaselineFor: %v", err)
	}
	// An unknown lesson has no signal, and verification must never act on that.
	//
	// This assertion used to demand the opposite (ok=true, "a real zero"). It was
	// wrong: zero rows means EITHER the lesson was absorbed OR nobody wrote a
	// retrospective in the window, and those are indistinguishable from here. Read
	// as a measured zero it turns base=3 -> cur=0 into a 100% improvement and
	// auto-verifies a lesson nobody fixed. ok=false is what every other rule
	// reports on an empty activity floor.
	if _, v, ok, err := metricValue(db, "R11", "no such lesson", evalWindow()); ok || err != nil {
		t.Fatalf("absent lesson: value %v ok %v err %v — want ok=false (no evidence is not a measured zero)", v, ok, err)
	}
}

// TestR11NotSelfChecking pins the deliberate choice: R11 reads stored rows over
// a trailing window, so a fortnight with no retrospectives looks exactly like a
// fortnight in which the lesson was absorbed. Sweeping an ACCEPTED row on that
// would be guessing.
func TestR11NotSelfChecking(t *testing.T) {
	if selfCheckingRules["R11"] {
		t.Fatal("R11 must NOT be self-checking — absence of retros is not evidence of repair")
	}
}
