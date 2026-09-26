package lessons

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// On the run-end pass the completion_report column still holds the PREVIOUS
// run's report (the wsingest scan is debounced); the divergence paragraph must
// come from the doc the run just returned.
func TestLoadInputReadsTheDocNotTheStaleColumn(t *testing.T) {
	db := openDB(t)
	old := "## Completion Report\n\n**Where reality diverged:** the OLD run's story.\n"
	p := seedRun(t, db, "fresh", 0.9, old)
	doc := filepath.Join(t.TempDir(), "phase.md")
	body := "# P\n\n## Completion Report\n\n**Where reality diverged:** the NEW run's story.\n"
	if err := os.WriteFile(doc, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `UPDATE epic_phases SET doc_path = ? WHERE id = ?`, doc, p)
	in, ok, why, err := LoadInput(db, p, "fresh")
	if err != nil || !ok {
		t.Fatalf("LoadInput ok=%v why=%q err=%v", ok, why, err)
	}
	if !strings.Contains(in.Divergence, "NEW run") {
		t.Fatalf("divergence = %q, want the doc's (new) paragraph", in.Divergence)
	}
}
