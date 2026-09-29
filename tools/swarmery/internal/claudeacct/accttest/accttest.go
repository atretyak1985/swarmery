// Package accttest builds admitted estates for tests outside internal/claudeacct,
// the way net/http/httptest serves net/http's callers. It writes only into a
// temporary SWARMERY_SECRETS_DIR: never into the operator's real store.
package accttest

import (
	"os"
	"path/filepath"
	"testing"
)

// AdmitEstate makes the estate store <key>.env admit root (D5 Lock 2): it points
// SWARMERY_SECRETS_DIR at a fresh 0700 temp dir unless the test already set one,
// and appends a `# swarmery-root: <root, resolved>` line to a 0600 store. A store
// holding only its root line is a healthy, credential-free estate.
func AdmitEstate(t testing.TB, key, root string) {
	t.Helper()
	dir := os.Getenv("SWARMERY_SECRETS_DIR")
	if dir == "" {
		dir = t.TempDir()
		t.Setenv("SWARMERY_SECRETS_DIR", dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("accttest: resolve root %s: %v", root, err)
	}
	f, err := os.OpenFile(filepath.Join(dir, key+".env"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("# swarmery-root: " + resolved + "\n"); err != nil {
		t.Fatal(err)
	}
}
