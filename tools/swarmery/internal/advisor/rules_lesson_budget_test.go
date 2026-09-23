package advisor

import (
	"fmt"
	"testing"
	"time"
)

func TestR12LessonStoreBudget(t *testing.T) {
	db := testDB(t)
	k := 0
	add := func(n int, globs string) {
		for j := 0; j < n; j++ {
			k++
			mustExec(t, db, `INSERT INTO surprise_lessons (source_phase_run, phase_id, seq, title, norm_title, guidance,
				area_globs, status, created_at, updated_at) VALUES (?, 1, 1, 't', 't', 'g', ?, 'active', 'now', 'now')`,
				fmt.Sprintf("r12-%d", k), globs)
		}
	}
	win := window{From: fmtTS(time.Now().AddDate(0, 0, -WindowDays)), To: fmtTS(time.Now())}

	add(R12ActivePerArea, "internal/ingest/**")
	fs, err := r12LessonBudget(db, win)
	if err != nil || len(fs) != 0 {
		t.Fatalf("at the per-area budget nothing fires: %+v, %v", fs, err)
	}
	add(1, "internal/ingest/*.go, internal/cost/**") // one lesson, two areas, ingest counted once
	fs, err = r12LessonBudget(db, win)
	if err != nil || len(fs) != 1 || fs[0].target != r12AreaPrefix+"internal/ingest" || fs[0].targetKind != "memory" {
		t.Fatalf("per-area = %+v, %v", fs, err)
	}
	add(R12ActiveTotal, "*")
	fs, _ = r12LessonBudget(db, win)
	targets := map[string]bool{}
	for _, f := range fs {
		targets[f.target] = true
	}
	if !targets[r12TotalTarget] || !targets[r12AreaPrefix+"."] || !targets[r12AreaPrefix+"internal/ingest"] {
		t.Fatalf("targets = %v", targets)
	}
	if v, ok, err := r12Metric(db, r12TotalTarget); err != nil || !ok || v != float64(R12ActivePerArea+1+R12ActiveTotal) {
		t.Fatalf("total metric = %v %v %v", v, ok, err)
	}
	if v, _, _ := r12Metric(db, r12AreaPrefix+"internal/cost"); v != 1 {
		t.Fatalf("cost metric = %v", v)
	}
	// through the full pass: R12 persists as a memory-kind recommendation
	if _, err := Run(db, time.Now()); err != nil {
		t.Fatalf("advisor run: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recommendations WHERE rule = 'R12'`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("persisted R12 rows = %d, %v", n, err)
	}
	name, _, ok, err := metricValue(db, "R12", r12TotalTarget, win)
	if err != nil || !ok || name != "active_lessons" {
		t.Fatalf("metricValue = %q %v %v", name, ok, err)
	}
}
