package provision

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

type stubRunner struct {
	calls   [][]string
	failOn  string // fail when the args-join has this prefix
	failErr error
}

func (s *stubRunner) Claude(_ context.Context, _, _ string, args ...string) (string, error) {
	s.calls = append(s.calls, args)
	if s.failOn != "" && strings.HasPrefix(strings.Join(args, " "), s.failOn) {
		return "", s.failErr
	}
	return "", nil
}

func (s *stubRunner) generateCalls() int {
	n := 0
	for _, c := range s.calls {
		if len(c) > 0 && c[0] == "-p" {
			n++
		}
	}
	return n
}

// migratedTestDB opens a fully-migrated temp SQLite DB via store.Open — the same
// canonical path internal/advisor's testDB uses (store.Open runs Migrate). This
// is the real helper substituted for the plan's openMigratedTestDB placeholder.
func migratedTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "provision.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// newSvc builds a Service on a migrated temp DB with the given actions.
func newSvc(t *testing.T, r Runner, actions map[string]GenerateAction) *Service {
	t.Helper()
	db := migratedTestDB(t)
	// seed a project row so the FK holds (mirror how other tests insert projects)
	if _, err := db.Exec(`INSERT INTO projects(id, path, slug, first_seen) VALUES(1,'/tmp/p','p','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	s := NewService(db, r)
	if actions != nil {
		s.Actions = actions
	}
	return s
}

func TestEnqueueSingleFlight(t *testing.T) {
	s := newSvc(t, &stubRunner{}, map[string]GenerateAction{})
	id1, started1, err := s.Enqueue(1, "architecture-pack")
	if err != nil || !started1 {
		t.Fatalf("first enqueue: started=%v err=%v", started1, err)
	}
	id2, started2, err := s.Enqueue(1, "architecture-pack")
	if err != nil || started2 || id2 != id1 {
		t.Fatalf("second enqueue must reuse: id=%d started=%v err=%v", id2, started2, err)
	}
}

// TestInflightUniqueIndex proves the migration's partial unique index rejects a
// second in-flight row for the same (project, pack) — the DB-level backstop for
// Enqueue's TOCTOU window.
func TestInflightUniqueIndex(t *testing.T) {
	s := newSvc(t, &stubRunner{}, map[string]GenerateAction{})
	ins := func() error {
		_, err := s.DB.Exec(
			`INSERT INTO provision_jobs(project_id, pack, status, started_at) VALUES(1,'architecture-pack','pending','2026-01-01T00:00:00Z')`)
		return err
	}
	if err := ins(); err != nil {
		t.Fatalf("first in-flight insert: %v", err)
	}
	if err := ins(); err == nil {
		t.Fatal("second in-flight insert must violate the partial unique index")
	}
	// A terminal row for the same key is allowed (the index only covers in-flight).
	if _, err := s.DB.Exec(
		`INSERT INTO provision_jobs(project_id, pack, status, started_at, finished_at) VALUES(1,'architecture-pack','done','2026-01-01T00:00:00Z','2026-01-01T00:00:01Z')`); err != nil {
		t.Fatalf("terminal row for same key must be allowed: %v", err)
	}
}

func TestRunInstallOnlyPack(t *testing.T) {
	r := &stubRunner{}
	s := newSvc(t, r, map[string]GenerateAction{}) // no action for any pack
	id, _, _ := s.Enqueue(1, "graphify-pack")
	if err := s.Run(context.Background(), id, "/tmp/p", "graphify-pack"); err != nil {
		t.Fatal(err)
	}
	if r.generateCalls() != 0 {
		t.Fatal("install-only pack must not generate")
	}
	if got := status(t, s, id); got != "installed" {
		t.Fatalf("status=%s", got)
	}
}

func TestRunSkipsWhenFresh(t *testing.T) {
	r := &stubRunner{}
	acts := map[string]GenerateAction{"architecture-pack": {Prompt: "x", Timeout: time.Minute, Fresh: func(string) bool { return true }}}
	s := newSvc(t, r, acts)
	id, _, _ := s.Enqueue(1, "architecture-pack")
	if err := s.Run(context.Background(), id, "/tmp/p", "architecture-pack"); err != nil {
		t.Fatal(err)
	}
	if r.generateCalls() != 0 {
		t.Fatal("fresh → no generate spawn")
	}
	if got := status(t, s, id); got != "skipped" {
		t.Fatalf("status=%s", got)
	}
}

func TestRunGeneratesWhenStale(t *testing.T) {
	r := &stubRunner{}
	acts := map[string]GenerateAction{"architecture-pack": {Prompt: "x", Timeout: time.Minute, Fresh: func(string) bool { return false }}}
	s := newSvc(t, r, acts)
	id, _, _ := s.Enqueue(1, "architecture-pack")
	if err := s.Run(context.Background(), id, "/tmp/p", "architecture-pack"); err != nil {
		t.Fatal(err)
	}
	if r.generateCalls() != 1 {
		t.Fatalf("stale → exactly one generate, got %d", r.generateCalls())
	}
	if got := status(t, s, id); got != "done" {
		t.Fatalf("status=%s", got)
	}
}

func TestRunFailsOnInstallError(t *testing.T) {
	r := &stubRunner{failOn: "plugin install", failErr: errors.New("boom")}
	s := newSvc(t, r, map[string]GenerateAction{})
	id, _, _ := s.Enqueue(1, "architecture-pack")
	if err := s.Run(context.Background(), id, "/tmp/p", "architecture-pack"); err == nil {
		t.Fatal("expected error")
	}
	j, _, _ := s.Latest(1, "architecture-pack")
	if j.Status != "failed" || !strings.Contains(j.Error, "boom") {
		t.Fatalf("job=%+v", j)
	}
}

func TestHealStale(t *testing.T) {
	s := newSvc(t, &stubRunner{}, map[string]GenerateAction{})
	id, _, _ := s.Enqueue(1, "architecture-pack") // pending
	if err := s.HealStale(); err != nil {
		t.Fatal(err)
	}
	if got := status(t, s, id); got != "failed" {
		t.Fatalf("status=%s", got)
	}
}

func status(t *testing.T, s *Service, id int64) string {
	t.Helper()
	var st string
	if err := s.DB.QueryRow(`SELECT status FROM provision_jobs WHERE id=?`, id).Scan(&st); err != nil {
		t.Fatal(err)
	}
	return st
}
