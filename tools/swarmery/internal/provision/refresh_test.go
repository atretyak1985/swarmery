package provision

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// listRunner is a stubRunner that also answers `plugin list --json` with a
// canned installed[] entry pointing at a fake pack on disk — what
// architectureRefresh needs to find the build script.
type listRunner struct {
	stubRunner
	listJSON string
}

func (l *listRunner) Claude(ctx context.Context, dir, stdin string, args ...string) (string, error) {
	if len(args) >= 2 && args[0] == "plugin" && args[1] == "list" {
		l.calls = append(l.calls, args)
		l.dirs = append(l.dirs, dir)
		return l.listJSON, nil
	}
	return l.stubRunner.Claude(ctx, dir, stdin, args...)
}

// fakePack lays out <root>/skills/architecture-map/scripts/build.sh that
// writes a recognisable viewer, and returns root.
func fakePack(t *testing.T, marker string) string {
	t.Helper()
	root := t.TempDir()
	scripts := filepath.Join(root, "skills", "architecture-map", "scripts")
	if err := os.MkdirAll(scripts, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "#!/bin/sh\n" +
		"# args: --json <in> --out <out>\n" +
		"while [ $# -gt 0 ]; do case \"$1\" in --json) in=\"$2\"; shift 2;; --out) out=\"$2\"; shift 2;; *) shift;; esac; done\n" +
		"[ -f \"$in\" ] || exit 3\n" +
		"printf '<html>" + marker + "</html>' > \"$out\"\n"
	if err := os.WriteFile(filepath.Join(scripts, "build.sh"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func projectWithMap(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "architecture-out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "architecture-map.json"), []byte(`{"schemaVersion":1,"analyzedAtCommit":"abcdef0123456789"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "architecture-map.html"), []byte("<html>OLD-VIEWER</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The case that motivated the step: the map's commit still matches HEAD, so the
// LLM generate is skipped — and before this, the viewer stayed whatever version
// of the template had rendered it. Now the installed pack's build script
// re-renders it first, and the job still reports "skipped".
func TestRunRefreshesViewerEvenWhenGenerateIsSkipped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh build script")
	}
	dir := projectWithMap(t)
	pack := fakePack(t, "NEW-VIEWER")
	r := &listRunner{listJSON: `{"installed":[{"id":"architecture-pack@swarmery","version":"1.4.0","scope":"user","installPath":"` + pack + `"}]}`}
	s := newSvc(t, r, map[string]GenerateAction{
		"architecture-pack": {
			Prompt:  "x",
			Timeout: 0,
			Fresh:   func(string) bool { return true },
			Refresh: architectureRefresh,
		},
	})

	id, _, _ := s.Enqueue(1, "architecture-pack")
	if err := s.Run(context.Background(), id, dir, "architecture-pack"); err != nil {
		t.Fatal(err)
	}

	html, err := os.ReadFile(filepath.Join(dir, "architecture-out", "architecture-map.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(html), "NEW-VIEWER") {
		t.Fatalf("viewer was not re-rendered by the installed pack; got %q", string(html))
	}
	if st := jobStatus(t, s, id); st != "skipped" {
		t.Errorf("job status = %q, want skipped — the refresh must not change the gate's verdict", st)
	}
	// The plugin lookup must carry the project dir: that is what binds it to
	// the project's Claude account.
	for i, c := range r.calls {
		if len(c) >= 2 && c[0] == "plugin" && c[1] == "list" && r.dirs[i] != dir {
			t.Errorf("plugin list ran with dir %q, want %q", r.dirs[i], dir)
		}
	}
	// No generate call happened.
	for _, c := range r.calls {
		if len(c) > 0 && c[0] == "-p" {
			t.Error("generate ran although Fresh reported the map current")
		}
	}
}

// Without a map on disk there is nothing to render: no plugin lookup, no build.
func TestRefreshIsANoOpWithoutAMap(t *testing.T) {
	dir := t.TempDir()
	r := &listRunner{listJSON: `{"installed":[]}`}
	s := newSvc(t, r, nil)
	if err := architectureRefresh(context.Background(), s, dir); err != nil {
		t.Fatalf("refresh without a map must be nil, got %v", err)
	}
	for _, c := range r.calls {
		if len(c) >= 2 && c[0] == "plugin" && c[1] == "list" {
			t.Error("plugin list was consulted although there is no map to render")
		}
	}
}

// A pack that is not installed for the project is an error the caller logs —
// and the job carries on to the generate step, which is what installs a map in
// the first place.
func TestRefreshReportsMissingPackButRunContinues(t *testing.T) {
	dir := projectWithMap(t)
	r := &listRunner{listJSON: `{"installed":[]}`}
	s := newSvc(t, r, map[string]GenerateAction{
		"architecture-pack": {
			Prompt:  "x",
			Timeout: 0,
			Fresh:   func(string) bool { return true },
			Refresh: architectureRefresh,
		},
	})
	if err := architectureRefresh(context.Background(), s, dir); err == nil {
		t.Fatal("expected an error when the pack is not installed for the project")
	}
	id, _, _ := s.Enqueue(1, "architecture-pack")
	if err := s.Run(context.Background(), id, dir, "architecture-pack"); err != nil {
		t.Fatalf("a refresh failure must not fail the job: %v", err)
	}
	if st := jobStatus(t, s, id); st != "skipped" {
		t.Errorf("job status = %q, want skipped", st)
	}
	html, _ := os.ReadFile(filepath.Join(dir, "architecture-out", "architecture-map.html"))
	if string(html) != "<html>OLD-VIEWER</html>" {
		t.Errorf("viewer changed although no pack could render it: %q", string(html))
	}
}

func jobStatus(t *testing.T, s *Service, id int64) string {
	t.Helper()
	var st string
	if err := s.DB.QueryRow(`SELECT status FROM provision_jobs WHERE id = ?`, id).Scan(&st); err != nil {
		t.Fatal(err)
	}
	return st
}
