package api

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

// gitCounter puts a `git` on PATH that logs every invocation and then runs the
// real git, so a test can count the provenance probes a request costs.
func gitCounter(t *testing.T) func() int {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "git.log")
	script := "#!/bin/sh\necho x >> '" + log + "'\nexec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() int {
		b, _ := os.ReadFile(log)
		return strings.Count(string(b), "x\n")
	}
}

// P1-2: GET /api/accounts classifies bindings through the display verdict
// cache and caches its discovery for a short TTL — two consecutive lists over
// 5 seeds and 2 pins cost a bounded number of git probes the first time and
// none the second; the API's own binding write invalidates the view.
func TestAccountsListBindingDiscoveryIsCached(t *testing.T) {
	attachHomeAccounts(t, ingest.DefaultAccount, "work")
	t.Setenv("SWARMERY_SECRETS_DIR", filepath.Join(t.TempDir(), "secrets"))
	repo := t.TempDir()
	fixtureGit(t, repo, "init", "-q", ".") // a repository, so every binding is really probed
	writeBindingFile(t, repo, `{"estate":"estatex","claudeAccount":"work"}`)
	seeds := []string{}
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		dir := filepath.Join(repo, n)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		seeds = append(seeds, dir)
	}
	writeBindingFile(t, seeds[0], `{"claudeAccount":"default"}`)
	writeBindingFile(t, seeds[1], `{"claudeAccount":"work"}`)
	count := gitCounter(t)
	_, srv := accountsTestDB(t, "cache.db", seeds...)

	listAccountsOK(t, srv)
	first := count()
	if first == 0 || first > 6 {
		t.Errorf("first list ran %d git probe(s), want 1..6 for 3 binding files", first)
	}
	rows := listAccountsOK(t, srv)
	if second := count() - first; second != 0 {
		t.Errorf("second list within the TTL ran %d git probe(s), want 0", second)
	}
	if work := accountNamed(t, rows, "work"); !slices.Equal(work.Projects, []string{seeds[1]}) {
		t.Errorf("work.projects = %v", work.Projects)
	}

	// The API's own write invalidates the view: seed c appears at once.
	if status, body := acctDo(t, http.MethodPut, srv.URL+"/api/projects/3/account", `{"account":"work"}`); status != http.StatusOK {
		t.Fatalf("PUT = %d\n%s", status, body)
	}
	rows = listAccountsOK(t, srv)
	if work := accountNamed(t, rows, "work"); !slices.Contains(work.Projects, seeds[2]) {
		t.Errorf("after the write, work.projects = %v, want %s — the cache was not invalidated", work.Projects, seeds[2])
	}
}

// The cache expires: past the TTL a list rediscovers (a hand edit shows up).
func TestAccountsBindingCacheExpires(t *testing.T) {
	attachHomeAccounts(t, ingest.DefaultAccount, "work")
	proj := t.TempDir()
	_, srv := accountsTestDB(t, "expire.db", proj)
	if work := accountNamed(t, listAccountsOK(t, srv), "work"); len(work.Projects) != 0 {
		t.Fatalf("precondition: %v", work.Projects)
	}
	writeBindingFile(t, proj, `{"claudeAccount":"work"}`) // a hand edit, not through the API
	if work := accountNamed(t, listAccountsOK(t, srv), "work"); len(work.Projects) != 0 {
		t.Errorf("within the TTL the cached view should still be served: %v", work.Projects)
	}
	orig := bindingCacheNow
	t.Cleanup(func() { bindingCacheNow = orig })
	later := orig().Add(bindingCacheTTL + 1)
	bindingCacheNow = func() time.Time { return later }
	if work := accountNamed(t, listAccountsOK(t, srv), "work"); !slices.Equal(work.Projects, []string{proj}) {
		t.Errorf("past the TTL: %v, want the hand-edited binding", work.Projects)
	}
}
