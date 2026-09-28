package api_test

import (
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
)

// resumeStoreVar is the one store variable NAME the stub reports the presence
// of — presence only ("set"/"unset"), never a value.
const resumeStoreVar = "SWARMERY_TEST_RESUME_ESTATE_SECRET"

// fakeClaude installs a stub `claude` that records the CLAUDE_CONFIG_DIR it was
// spawned with, so a test can assert the resume's account env WITHOUT a real CLI.
// Beside it (storeDump) it records whether resumeStoreVar was present. The
// store file is written FIRST, so awaiting the config-dir dump implies both.
func fakeClaude(t *testing.T) (dumpPath string) {
	t.Helper()
	dir := t.TempDir()
	dumpPath = filepath.Join(dir, "config-dir")
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\n" +
		"if [ -n \"${" + resumeStoreVar + "+x}\" ]; then printf set > " + storeDump(dumpPath) + "; else printf unset > " + storeDump(dumpPath) + "; fi\n" +
		"printf '%s' \"$CLAUDE_CONFIG_DIR\" > " + dumpPath + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWARMERY_CLAUDE_BIN", bin)
	return dumpPath
}

// storeDump is where fakeClaude records resumeStoreVar's presence.
func storeDump(dumpPath string) string { return filepath.Join(filepath.Dir(dumpPath), "store") }

// awaitDump waits for the stub to land its file and returns the contents.
func awaitDump(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil {
			return string(b)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("stub claude never ran (no %s)", path)
	return ""
}

// insertResumableSession adds a session row the composer will accept: existing
// cwd, no live process, and the given account key.
func insertResumableSession(t *testing.T, db *sql.DB, uuid, account string) {
	t.Helper()
	cwd := t.TempDir()
	if _, err := db.Exec(
		`INSERT INTO sessions (id, project_id, session_uuid, cwd, status, started_at, account)
		 VALUES (1, 1, ?, ?, 'idle', ?, ?)`,
		uuid, cwd, time.Now().UTC().Format(time.RFC3339), account); err != nil {
		t.Fatal(err)
	}
}

// THE regression: a session written under a non-default Claude account must be
// resumed under that same account's config dir. When the env delta is dropped,
// `claude -r` reads the DEFAULT config dir, finds no transcript, and exits with
// "No conversation found with session ID" — which, for the planning wizard, made
// every answer roll back to the same question forever.
func TestResumeRunsUnderTheSessionsAccount(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "") // the test process may run under an account itself
	dump := fakeClaude(t)

	h := openMessageTestDB(t)
	insertResumableSession(t, h.DB, "uuid-acct", "nanitor")

	if w := postMessage(t, h, "1", `{"text":"hello"}`); w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", w.Code, w.Body.String())
	}

	want := filepath.Join(home, ".claude-nanitor")
	if got := awaitDump(t, dump); got != want {
		t.Errorf("CLAUDE_CONFIG_DIR = %q, want %q", got, want)
	}
}

// The default account stays an EMPTY env delta — binding a project to `default`
// must not start pinning CLAUDE_CONFIG_DIR (claudeacct's package invariant).
func TestResumeUnderDefaultAccountPinsNoConfigDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "") // the test process may run under an account itself
	dump := fakeClaude(t)

	h := openMessageTestDB(t)
	insertResumableSession(t, h.DB, "uuid-default", "")

	if w := postMessage(t, h, "1", `{"text":"hello"}`); w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", w.Code, w.Body.String())
	}

	if got := awaitDump(t, dump); got != "" {
		t.Errorf("CLAUDE_CONFIG_DIR = %q, want empty", got)
	}
}

// The two axes at the one seam where they visibly differ. The session ran in a
// daemon WORKTREE cut from a repo under an estate root whose pin names a
// DIFFERENT account than the sessions row. The resume must keep the ROW's
// account (the transcript lives under that config dir) and take the ESTATE's
// store from the cwd (mapped back to its source checkout).
func TestResumeKeepsTheRowsAccountAndTakesTheCwdsEstate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	os.Unsetenv(resumeStoreVar)
	secrets := t.TempDir()
	if err := os.Chmod(secrets, 0o700); err != nil { // the loader refuses a store dir open beyond its owner
		t.Fatal(err)
	}
	t.Setenv("SWARMERY_SECRETS_DIR", secrets)
	if err := os.WriteFile(filepath.Join(secrets, "acme.env"), []byte(resumeStoreVar+"=marker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(secrets, "acme.env"), 0o600); err != nil {
		t.Fatal(err)
	}

	root := filepath.Join(home, "projects", "acme")
	src := filepath.Join(root, "repo")
	if err := claudeacct.SetBinding(root, "work"); err != nil { // the estate root pins a different payer
		t.Fatal(err)
	}
	if err := claudeacct.SetEstate(root, "acme"); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(home, ".swarmery", "worktrees", strings.ReplaceAll(src, "/", "-"), "T-1")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+filepath.Join(src, ".git", "worktrees", "T-1")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// git's back-pointer: the gitdir mapping is believed only when it pairs.
	admin := filepath.Join(src, ".git", "worktrees", "T-1")
	if err := os.MkdirAll(admin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(admin, "gitdir"), []byte(filepath.Join(wt, ".git")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := claudeacct.Resolve(wt); r.Account != "work" || r.Estate != "acme" {
		t.Fatalf("precondition: the cwd resolves %q/%q, want work/acme", r.Account, r.Estate)
	}

	dump := fakeClaude(t)
	h := openMessageTestDB(t)
	if _, err := h.DB.Exec(
		`INSERT INTO sessions (id, project_id, session_uuid, cwd, status, started_at, account)
		 VALUES (1, 1, ?, ?, 'idle', ?, ?)`,
		"uuid-estate", wt, time.Now().UTC().Format(time.RFC3339), "nanitor"); err != nil {
		t.Fatal(err)
	}
	if w := postMessage(t, h, "1", `{"text":"hello"}`); w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", w.Code, w.Body.String())
	}

	if got, want := awaitDump(t, dump), filepath.Join(home, ".claude-nanitor"); got != want {
		t.Errorf("CLAUDE_CONFIG_DIR = %q, want the ROW's %q — the cwd's pin must not re-home the resume", got, want)
	}
	if got, _ := os.ReadFile(storeDump(dump)); string(got) != "set" {
		t.Errorf("estate store variable presence = %q, want set — the cwd's estate did not reach the resume", got)
	}
}
