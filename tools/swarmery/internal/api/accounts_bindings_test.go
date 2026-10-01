package api

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

func writeBindingFile(t *testing.T, dir, ns string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude", "settings.local.json"), []byte(`{"swarmery":`+ns+`}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fixtureGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false", "-C", dir}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// SC-10 / criteria 16 and 35: every on-disk binding is visible — a binding with
// no projects row appears (projectsUnindexed); an archived row is excluded from
// projects; a path under the worktree root is excluded entirely; a tracked
// binding is listed under ignoredBindings with its reason and under no account.
func TestBindingsByAccount(t *testing.T) {
	home, _ := attachHomeAccounts(t, ingest.DefaultAccount, "work")
	t.Setenv("SWARMERY_SECRETS_DIR", filepath.Join(t.TempDir(), "secrets"))
	estate := t.TempDir()
	writeBindingFile(t, estate, `{"estate":"estatex","claudeAccount":"work"}`)
	noRow := filepath.Join(estate, "repos", "no-row")
	writeBindingFile(t, noRow, `{"claudeAccount":"default"}`)
	indexed := filepath.Join(estate, "repos", "indexed")
	writeBindingFile(t, indexed, `{"claudeAccount":"work"}`)
	wt := filepath.Join(home, ".swarmery", "worktrees", "slug", "task-1")
	writeBindingFile(t, wt, `{"claudeAccount":"default"}`)

	checkGit := true
	if _, err := exec.LookPath("git"); err != nil {
		checkGit = false
	}
	tracked := filepath.Join(estate, "repos", "tracked")
	if checkGit {
		writeBindingFile(t, tracked, `{"claudeAccount":"work"}`)
		fixtureGit(t, tracked, "init", "-q", ".")
		fixtureGit(t, tracked, "add", "-f", ".claude/settings.local.json")
		fixtureGit(t, tracked, "commit", "-q", "-m", "fixture")
	}

	db, srv := accountsTestDB(t, "bindings.db", estate, indexed, wt)
	if _, err := db.Exec(`UPDATE projects SET archived = 1 WHERE path = ?`, estate); err != nil {
		t.Fatal(err)
	}

	status, body := acctDo(t, http.MethodGet, srv.URL+"/api/accounts", "")
	if status != http.StatusOK {
		t.Fatalf("GET /api/accounts = %d\n%s", status, body)
	}
	var resp accountsResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatal(err)
	}
	work := accountNamed(t, resp.Accounts, "work")
	def := accountNamed(t, resp.Accounts, ingest.DefaultAccount)
	if !slices.Equal(work.Projects, []string{indexed}) {
		t.Errorf("work.projects = %v, want only the indexed row (the archived estate root is excluded)", work.Projects)
	}
	if !slices.Contains(work.ProjectsUnindexed, estate) {
		t.Errorf("work.projectsUnindexed = %v, want the archived estate root", work.ProjectsUnindexed)
	}
	if !slices.Equal(def.ProjectsUnindexed, []string{noRow}) || len(def.Projects) != 0 {
		t.Errorf("default = %v / %v, want the row-less binding unindexed", def.Projects, def.ProjectsUnindexed)
	}
	if strings.Contains(body, filepath.Join(".swarmery", "worktrees")) {
		t.Errorf("a daemon worktree path reached the response: %s", body)
	}
	if resp.IgnoredBindings == nil || !strings.Contains(body, `"ignoredBindings":[`) {
		t.Fatalf("ignoredBindings missing or null: %s", body)
	}
	if checkGit {
		if len(resp.IgnoredBindings) != 1 || resp.IgnoredBindings[0].Path != tracked ||
			resp.IgnoredBindings[0].Declares != "work" || !strings.Contains(resp.IgnoredBindings[0].Reason, "tracked by git") {
			t.Errorf("ignoredBindings = %+v", resp.IgnoredBindings)
		}
		for _, a := range resp.Accounts {
			if slices.Contains(a.Projects, tracked) || slices.Contains(a.ProjectsUnindexed, tracked) {
				t.Errorf("the tracked binding was counted under %s", a.Key)
			}
		}
	}
}

// An empty machine still answers ignoredBindings as [] (never null).
func TestBindingsByAccountEmptyIsArray(t *testing.T) {
	attachHomeAccounts(t, ingest.DefaultAccount)
	_, srv := accountsTestDB(t, "bindings-empty.db")
	_, body := acctDo(t, http.MethodGet, srv.URL+"/api/accounts", "")
	if !strings.Contains(body, `"ignoredBindings":[]`) || !strings.Contains(body, `"projectsUnindexed":[]`) {
		t.Errorf("body = %s", body)
	}
}
