package acctops

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// fakeHome points HOME at a temp dir holding the default and the insart config
// dirs, and the credential store at a second temp dir. Nothing real is read.
func fakeHome(t *testing.T) (home, secrets string) {
	t.Helper()
	home = t.TempDir()
	for _, d := range []string{".claude/projects", ".claude-insart/projects"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	secrets = filepath.Join(t.TempDir(), "secrets")
	if err := os.Mkdir(secrets, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("SWARMERY_SECRETS_DIR", secrets)
	t.Setenv(quotaIntervalEnv, "") // the staleness bound must not follow the caller's env
	return home, secrets
}

// writeBinding writes <dir>/.claude/settings.local.json with body, mode 0600.
func writeBinding(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func sha(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func exists_(p string) bool { _, err := os.Lstat(p); return err == nil }

var nameValueLine = regexp.MustCompile(`[A-Z][A-Z0-9_]{2,}=`)

// assertNoSecretShape fails when any line could carry a variable's name or value.
func assertNoSecretShape(t *testing.T, lines []string) {
	t.Helper()
	for _, l := range lines {
		if nameValueLine.MatchString(l) || regexp.MustCompile(`^[A-Z][A-Z0-9_]*=`).MatchString(l) {
			t.Errorf("a line has a NAME=value shape (content withheld)")
		}
		if strings.Contains(l, "fixture-value") {
			t.Errorf("a credential value reached the output")
		}
	}
}
