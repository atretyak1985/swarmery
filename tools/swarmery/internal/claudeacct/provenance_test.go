package claudeacct

import (
	"os"
	"path/filepath"
	"testing"
)

// FileProvenance reports the probe's four verdicts, with a detail only for
// unknown.
func TestFileProvenance(t *testing.T) {
	repo := newRepo(t)
	tracked := filepath.Join(repo, ".claude", "settings.json")
	writeAt(t, tracked, "{}\n")
	runGit(t, repo, "add", "-f", ".claude/settings.json")
	untracked := filepath.Join(repo, ".claude", "settings.local.json")
	writeAt(t, untracked, "{}\n")
	plain := filepath.Join(t.TempDir(), ".claude", "settings.json")
	writeAt(t, plain, "{}\n")
	broken := t.TempDir()
	unknown := filepath.Join(broken, ".claude", "settings.json")
	writeAt(t, unknown, "{}\n")
	if err := os.WriteFile(filepath.Join(broken, ".git"), []byte("gitdir: "+filepath.Join(broken, "nowhere")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for path, want := range map[string]Provenance{
		tracked:   ProvenanceTracked,
		untracked: ProvenanceUntracked,
		plain:     ProvenanceNotRepo,
		unknown:   ProvenanceUnknown,
	} {
		got, detail := FileProvenance(path)
		if got != want {
			t.Errorf("FileProvenance(%s) = %d, want %d", path, got, want)
		}
		if (got == ProvenanceUnknown) != (detail != "") {
			t.Errorf("FileProvenance(%s) detail = %q", path, detail)
		}
	}
}
