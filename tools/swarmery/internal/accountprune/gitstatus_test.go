package accountprune

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
)

// Real fixtures through the real probe: a committed file is TRACKED, a file in
// a repository that git does not track is untracked, a file in no repository
// is NO-REPO, and a repository git cannot read is TRACKED (fail closed).
func TestGitStatusFixtures(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tracked := trackedRepo(t, root, "repo", "{}\n")
	untracked := write(t, filepath.Join(root, "repo", ".claude", "settings.local.json"), "{}\n")
	norepo := write(t, filepath.Join(root, "plain", ".claude", "settings.json"), "{}\n")

	// Indeterminate: a .git FILE pointing at a gitdir that does not exist —
	// git dies with a fatal that is neither "untracked" nor "no repository".
	broken := filepath.Join(root, "broken")
	indeterminate := write(t, filepath.Join(broken, ".claude", "settings.json"), "{}\n")
	if err := os.WriteFile(filepath.Join(broken, ".git"), []byte("gitdir: "+filepath.Join(root, "nowhere")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for path, want := range map[string]GitStatus{
		tracked:       StatusTracked,
		untracked:     StatusUntracked,
		norepo:        StatusNoRepo,
		indeterminate: StatusTracked,
	} {
		if got := gitStatus(path); got != want {
			t.Errorf("gitStatus(%s) = %s, want %s", path, got, want)
		}
	}
}

// The mapping, as a table: anything that is not positively untracked or
// outside a repository is TRACKED.
func TestStatusFor(t *testing.T) {
	for v, want := range map[claudeacct.Provenance]GitStatus{
		claudeacct.ProvenanceTracked:   StatusTracked,
		claudeacct.ProvenanceUnknown:   StatusTracked,
		claudeacct.ProvenanceUntracked: StatusUntracked,
		claudeacct.ProvenanceNotRepo:   StatusNoRepo,
		claudeacct.Provenance(99):      StatusTracked,
	} {
		if got := statusFor(v); got != want {
			t.Errorf("statusFor(%d) = %s, want %s", v, got, want)
		}
	}
}

// An indeterminate verdict from the probe makes an eligible file TRACKED, so
// Apply refuses it.
func TestIndeterminateRefused(t *testing.T) {
	f := newEstate(t)
	p := write(t, filepath.Join(f.root, "a", ".claude", "settings.json"), `{"pluginConfigs":{"b@m":{"options":{}}}}`)
	orig := probe
	probe = func(string) (claudeacct.Provenance, string) { return claudeacct.ProvenanceUnknown, "stub" }
	t.Cleanup(func() { probe = orig })
	targets := mustPlan(t, f.root)
	if tg := byPath(targets)[p]; tg.Status != StatusTracked || !tg.Eligible {
		t.Fatalf("got %+v", tg)
	}
	if _, err := Apply(targets, Options{QuarantineDir: f.q}); !IsTrackedRefusal(err) {
		t.Errorf("err = %v, want the tracked refusal", err)
	}
}
