package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

// PUT /api/projects/{id}/account never promotes an untrusted file: a 0664 file
// that would need rewriting is refused with 409 naming the path and the reason,
// and is left byte- and mode-identical with no .bak; a file that already holds
// the key but that the walk ignores is NOT reported as bound — 409 as well. A
// trusted file is written and read back.
func TestPutProjectAccountReadsTheBindingBack(t *testing.T) {
	attachHomeAccounts(t, ingest.DefaultAccount, "nabu-org")
	fresh := t.TempDir()
	stale := t.TempDir()
	trusted := t.TempDir()
	settings := func(dir string) string { return filepath.Join(dir, ".claude", "settings.local.json") }
	const freshBody = `{"permissions":{}}`
	for dir, body := range map[string]string{
		fresh:   freshBody,
		stale:   `{"swarmery":{"claudeAccount":"nabu-org"}}`,
		trusted: freshBody,
	} {
		if err := os.MkdirAll(filepath.Dir(settings(dir)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(settings(dir), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if dir == trusted {
			continue
		}
		if err := os.Chmod(settings(dir), 0o664); err != nil {
			t.Fatal(err)
		}
	}
	_, srv := accountsTestDB(t, "project-account-readback.db", fresh, stale, trusted)

	status, body := acctDo(t, http.MethodPut, srv.URL+"/api/projects/1/account", `{"account":"nabu-org"}`)
	if status != http.StatusConflict || !strings.Contains(body, settings(fresh)) ||
		!strings.Contains(body, "group or other") || !strings.Contains(body, "chmod go-w") {
		t.Fatalf("PUT over a 0664 file = %d %s, want 409 naming the path, the reason and the fix", status, body)
	}
	fi, err := os.Stat(settings(fresh))
	if err != nil {
		t.Fatal(err)
	}
	if m := fi.Mode().Perm(); m != 0o664 {
		t.Fatalf("mode after a refused PUT = %04o, want 0664 untouched", m)
	}
	if raw, _ := os.ReadFile(settings(fresh)); string(raw) != freshBody {
		t.Fatalf("a refused PUT changed the file: %q", raw)
	}
	if _, err := os.Lstat(settings(fresh) + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("a refused PUT left a .bak (%v)", err)
	}

	status, body = acctDo(t, http.MethodPut, srv.URL+"/api/projects/2/account", `{"account":"nabu-org"}`)
	if status != http.StatusConflict || !strings.Contains(body, "group or other") {
		t.Fatalf("PUT over an untrusted already-bound file = %d %s, want 409 naming the mode reason", status, body)
	}

	status, body = acctDo(t, http.MethodPut, srv.URL+"/api/projects/3/account", `{"account":"nabu-org"}`)
	if status != http.StatusOK {
		t.Fatalf("PUT over a trusted file = %d, want 200\n%s", status, body)
	}
	if got := claudeacct.Binding(trusted); got != "nabu-org" {
		t.Fatalf("binding after PUT = %q, want nabu-org", got)
	}
}
