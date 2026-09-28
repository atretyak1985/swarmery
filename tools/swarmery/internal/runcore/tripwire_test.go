package runcore

// The runcore half of D5's Lock 1 tripwire: a binding git TRACKS reaches no
// engine. AccountFor is what the key-carrying engines (planrun, phaserun) put
// in Spec.Resolution, so a committed binding must resolve to nothing here, and
// a real child started from that Spec must see neither the account's config
// dir nor a name from its store. Counts and absences only.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gitFixture runs git for fixture building with the operator's global and
// system config neutralised (a global excludesfile would otherwise decide
// whether the "committed" binding is tracked at all — hence add -f as well).
func gitFixture(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-c", "user.name=swarmery test", "-c", "user.email=test@example.invalid",
		"-c", "commit.gpgsign=false", "-C", dir}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestTrackedBindingReachesNoSpawnSeam(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	os.Unsetenv("CLAUDE_CONFIG_DIR")
	if err := os.MkdirAll(filepath.Join(home, ".claude-work", "projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	seedSecretStoreNamed(t, "work")

	repo := t.TempDir()
	gitFixture(t, repo, "init", "-q", ".")
	if err := os.MkdirAll(filepath.Join(repo, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".claude", "settings.local.json"),
		[]byte(`{"swarmery":{"claudeAccount":"work","estate":"work"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitFixture(t, repo, "add", "-f", "--", ".claude/settings.local.json")
	gitFixture(t, repo, "commit", "-qm", "commit the binding")

	r := AccountFor(repo)
	if r.Account != "" || r.Estate != "" {
		t.Fatalf("AccountFor read a committed binding: account %q estate %q", r.Account, r.Estate)
	}
	res, err := ClaudeRunner{Engine: "test"}.Start(context.Background(), Spec{
		Prompt: "p", SessionUUID: "u-tripwire", Cwd: t.TempDir(),
		Resolution:    r,
		Bin:           fakeBin(t, `printf '%s|%s\n' "${`+secretVar+`-`+absentMarker+`}" "${CLAUDE_CONFIG_DIR-`+absentMarker+`}"`+"\n"),
		Timeout:       30 * time.Second,
		CaptureStdout: true,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	store, configDir, _ := strings.Cut(strings.TrimSpace(res.Output), "|")
	if store != absentMarker {
		t.Error("a committed binding released a store name to the child")
	}
	if configDir != absentMarker {
		t.Error("a committed binding chose the child's config dir")
	}
}
