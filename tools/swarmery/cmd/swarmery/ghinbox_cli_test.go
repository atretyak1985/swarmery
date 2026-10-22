package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ghinbox"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

const ghFixtures = "../../testdata/fixtures/ghinbox"

// ghFixtureReader serves the ghinbox fixtures by endpoint path; any path it
// does not know answers "[]", and failPaths answer an error.
type ghFixtureReader struct {
	pages     map[string]string
	failPaths map[string]bool
}

func (r ghFixtureReader) API(_ context.Context, endpoint string, _ ...string) (string, error) {
	path, _, _ := strings.Cut(endpoint, "?")
	if r.failPaths[path] {
		return "", errors.New("HTTP 404: Not Found")
	}
	if out, ok := r.pages[path]; ok {
		return out, nil
	}
	return "[]", nil
}

func readGHFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(ghFixtures, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func useGHFixtures(t *testing.T, failPaths ...string) {
	t.Helper()
	r := ghFixtureReader{pages: map[string]string{
		"repos/octo/widgets/issues":          readGHFixture(t, "issues.json") + readGHFixture(t, "prs.json"),
		"repos/octo/widgets/issues/comments": readGHFixture(t, "comments.json"),
		"repos/octo/widgets/pulls/comments":  readGHFixture(t, "review_comments.json"),
	}, failPaths: map[string]bool{}}
	for _, p := range failPaths {
		r.failPaths[p] = true
	}
	prev := ghinboxNewReader
	ghinboxNewReader = func(string) (ghinbox.Reader, error) { return r, nil }
	t.Cleanup(func() { ghinboxNewReader = prev })
}

// ghinboxDB is a migrated database with one project whose project.json holds
// prBlock (nil ⇒ no prReview key), returned with the db path.
func ghinboxDB(t *testing.T, slug string, prBlock any) (string, *sql.DB) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "swarmery.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	addGHProject(t, db, slug, prBlock)
	return dbPath, db
}

func addGHProject(t *testing.T, db *sql.DB, slug string, prBlock any) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), slug)
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{"name": slug}
	if prBlock != nil {
		cfg["prReview"] = prBlock
	}
	raw, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(dir, ".claude", "project.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO projects (path, slug, first_seen) VALUES (?, ?, '2026-10-01T00:00:00.000Z')`, dir, slug); err != nil {
		t.Fatal(err)
	}
}

var ghWidgets = map[string]any{
	"owner": "octo",
	"repos": map[string]any{"widgets": map[string]any{"path": "/src/widgets", "gates": []string{"make test"}}},
}

func ghNow() time.Time { return time.Date(2026, 10, 22, 14, 0, 0, 0, time.UTC) }

func runGhinbox(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := ghinboxSync(context.Background(), args, &out, &errOut, ghNow)
	return code, out.String(), errOut.String()
}

func taskCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestGhinboxSyncDryRunPrintsWouldWrite: --dry-run prints each card it would
// write — label, title, link, description — then the project line, and leaves
// the board empty.
func TestGhinboxSyncDryRunPrintsWouldWrite(t *testing.T) {
	useGHFixtures(t)
	dbPath, db := ghinboxDB(t, "widgets-app", ghWidgets)
	code, out, errOut := runGhinbox(t, "--db", dbPath, "--project", "widgets-app", "--since", "2026-10-22T00:00:00Z", "--dry-run")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	for _, want := range []string{
		"would create github:octo/widgets#7  Issue octo/widgets#7: Docs: the install section is out of date  https://github.com/octo/widgets/issues/7  (+1 new comments)",
		"would create github:octo/widgets#9  PR octo/widgets#9: Retry the upload on 503",
		"would create github:octo/widgets#12  Issue octo/widgets#12: Crash on empty config",
		"would create github:octo/widgets#14  PR octo/widgets#14: Add a --dry-run flag",
		"    GitHub issue octo/widgets#12 — https://github.com/octo/widgets/issues/12",
		"widgets-app: would have created 4 · updated 0 · unchanged 0 · repos octo/widgets · since 2026-10-22T00:00:00Z",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output lacks %q:\n%s", want, out)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(out), "since 2026-10-22T00:00:00Z") {
		t.Errorf("the project line must come last (routine detail keeps the tail):\n%s", out)
	}
	if n := taskCount(t, db); n != 0 {
		t.Errorf("dry run wrote %d cards", n)
	}
}

// TestGhinboxSyncWritesAndQuotesNoGitHubText: a real sync writes the cards and
// its output — which the routine hands to a model — names labels and counts,
// never a title.
func TestGhinboxSyncWritesAndQuotesNoGitHubText(t *testing.T) {
	useGHFixtures(t)
	dbPath, db := ghinboxDB(t, "widgets-app", ghWidgets)
	code, out, errOut := runGhinbox(t, "--db", dbPath, "--all", "--since", "2026-10-22T00:00:00Z")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if n := taskCount(t, db); n != 4 {
		t.Errorf("cards = %d, want 4", n)
	}
	if !strings.Contains(out, "created github:octo/widgets#12 (+0 new comments)") ||
		!strings.Contains(out, "widgets-app: created 4 · updated 0 · unchanged 0") {
		t.Errorf("output:\n%s", out)
	}
	for _, s := range []string{"Crash", "Ignore", "Retry", "carol", "install section"} {
		if strings.Contains(out, s) {
			t.Errorf("output quotes GitHub text %q:\n%s", s, out)
		}
	}
	// Second run: nothing new.
	code, out, _ = runGhinbox(t, "--db", dbPath, "--all", "--since", "2026-10-22T00:00:00Z")
	if code != 0 || !strings.Contains(out, "created 0 · updated 0 · unchanged 4") || taskCount(t, db) != 4 {
		t.Errorf("second run exit %d:\n%s", code, out)
	}
}

// TestGhinboxSyncAllSkipsUnwatchedProjects: --all reads only projects with a
// prReview block and says how many it left alone; a token-less host is an
// explicit skip line, not a failure.
func TestGhinboxSyncAllSkipsUnwatchedProjects(t *testing.T) {
	prev := ghinboxNewReader
	ghinboxNewReader = func(host string) (ghinbox.Reader, error) {
		return nil, fmt.Errorf("%w: %s", repoprovider.ErrNoStoredToken, host)
	}
	t.Cleanup(func() { ghinboxNewReader = prev })
	dbPath, db := ghinboxDB(t, "widgets-app", ghWidgets)
	addGHProject(t, db, "plain-one", nil)
	addGHProject(t, db, "plain-two", nil)
	code, out, errOut := runGhinbox(t, "--db", dbPath, "--all")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, "widgets-app: skipped — no GitHub token stored for github.com") ||
		!strings.Contains(out, "2 project(s) without a prReview block: not watched") {
		t.Errorf("output:\n%s", out)
	}
}

func TestGhinboxSyncRepoFailureExitsOne(t *testing.T) {
	useGHFixtures(t, "repos/octo/widgets/issues")
	dbPath, _ := ghinboxDB(t, "widgets-app", ghWidgets)
	code, out, _ := runGhinbox(t, "--db", dbPath, "--project", "1")
	if code != 1 || !strings.Contains(out, "errors: octo/widgets:") {
		t.Errorf("exit %d, want 1 with the repo error on the project line:\n%s", code, out)
	}
}

func TestGhinboxSyncUsage(t *testing.T) {
	dbPath, _ := ghinboxDB(t, "widgets-app", ghWidgets)
	for _, args := range [][]string{
		{"--db", dbPath},
		{"--db", dbPath, "--all", "--project", "x"},
		{"--db", dbPath, "--all", "--since", "yesterday"},
		{"--db", dbPath, "--all", "stray"},
		{"--db", dbPath, "--nope"},
	} {
		if code, _, _ := runGhinbox(t, args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	if code, _, errOut := runGhinbox(t, "--db", dbPath, "--project", "missing"); code != 2 || !strings.Contains(errOut, `no project "missing"`) {
		t.Errorf("unknown project: exit %d stderr %q", code, errOut)
	}
}
