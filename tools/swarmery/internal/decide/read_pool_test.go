package decide

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// A labeler pass reads a session's evidence through the read pool, so those
// reads no longer hold the single writer every HTTP handler and ingest write
// queues on — and its own answers, written through the writer, are visible to
// the next pass's reads through the pool.
func TestLabelerReadsThroughReadPool(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decide.db")
	w, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	mustExec(t, w, `INSERT INTO projects(id, path, slug, first_seen) VALUES(1, '/repo', 'p', '2026-01-01T00:00:00Z')`)
	seedSession(t, w, "s-old", "2026-09-20T11:00:00.000Z", "success")
	r, err := store.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })

	now := func() time.Time { return time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC) }
	s := &stub{name: BackendLocal, byQ: map[string]Answer{
		QD2TaskType: {Value: "bugfix", Confidence: 0.9, Calibrated: true},
		QD2Outcome:  {Value: "partial", Confidence: 0.9, Calibrated: true},
		QD2Failure:  {Value: "none", Confidence: 0.9, Calibrated: true},
	}}
	e := &Engine{DB: w, ReadDB: r, Local: s, Now: now,
		Thresholds: map[string]float64{"d2": 0.6}, DefaultModes: map[string]Mode{"d2": ModeActive}}

	// Hold the writer — the shape of an ingest rescan or a prune.
	ctx := context.Background()
	conn, err := w.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	done := make(chan []Question, 1)
	go func() {
		done <- d2Questions(e.readDB(), d2Session{uuid: "s-old", title: "fix the parser",
			started: "2026-09-20T10:00:00.000Z", ended: "2026-09-20T11:00:00.000Z", outcome: "success"}, false)
	}()
	select {
	case qs := <-done:
		if len(qs) != 3 {
			t.Fatalf("questions = %d, want 3", len(qs))
		}
	case <-time.After(time.Second):
		t.Fatal("the labeler's evidence reads waited on the writer")
	}
	if _, err := conn.ExecContext(ctx, `ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	conn.Close()

	// A full pass labels the session; the next pass, reading the decisions it
	// just wrote through the pool, asks nothing again.
	if n, err := (&Labeler{E: e}).Run(ctx); err != nil || n != 1 {
		t.Fatalf("pass: labelled %d (%v), want 1", n, err)
	}
	if l, err := LabelFor(w, "s-old"); err != nil || l == nil || l.Outcome != "shipped" {
		t.Fatalf("labels = %+v err=%v", l, err)
	}
	calls := s.calls
	if n, _ := (&Labeler{E: e}).Run(ctx); n != 0 {
		t.Fatalf("second pass labelled %d — it did not see the first pass's writes through the pool", n)
	}
	if s.calls != calls {
		t.Errorf("second pass asked the backend %d more times", s.calls-calls)
	}
}
