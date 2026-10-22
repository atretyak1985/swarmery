package worktree

// The worktree is the repository's tree, and a repository can commit symlinks.
// Every file the daemon reads out of a worktree or writes into one therefore has
// to refuse a path that resolves through a symlink: following it would let the
// tree choose a file OUTSIDE the worktree for the daemon to read or overwrite.
// These tests pin that refusal for the three plan-doc/report paths.

import (
	"os"
	"path/filepath"
	"testing"
)

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// TestCollectReportRefusesSymlink: a report path that resolves through a
// symlink — the file itself or its .swarmery parent — reads as "no report",
// never as the target's contents.
func TestCollectReportRefusesSymlink(t *testing.T) {
	const secret = "outside-the-worktree"
	cases := map[string]func(t *testing.T, wt, outside string){
		"report file is a symlink": func(t *testing.T, wt, outside string) {
			target := filepath.Join(outside, "secret")
			writeFile(t, target, secret)
			mustSymlink(t, target, filepath.Join(wt, ReportPath))
		},
		"report dir is a symlink": func(t *testing.T, wt, outside string) {
			writeFile(t, filepath.Join(outside, "report.md"), secret)
			mustSymlink(t, outside, filepath.Join(wt, ".swarmery"))
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			wt, outside := t.TempDir(), t.TempDir()
			setup(t, wt, outside)
			if got := CollectReport(wt); got != "" {
				t.Errorf("CollectReport = %q, want \"\" — a symlinked report must not be followed", got)
			}
		})
	}
}

// TestLendPlanDocRefusesPlantedSymlink: a symlink already sitting where the lent
// copy goes is an error, and the file it points at is left exactly as it was.
func TestLendPlanDocRefusesPlantedSymlink(t *testing.T) {
	wt, outside := t.TempDir(), t.TempDir()
	doc := filepath.Join(t.TempDir(), "phase-1-x.md")
	writeFile(t, doc, "# Phase 1\n")
	victim := filepath.Join(outside, "victim")
	writeFile(t, victim, "original")
	mustSymlink(t, victim, filepath.Join(wt, PlanDocDir, "phase-1-x.md"))

	if _, err := LendPlanDoc(wt, doc); err == nil {
		t.Error("LendPlanDoc lent through a planted symlink; want an error")
	}
	if got := readFile(t, victim); got != "original" {
		t.Errorf("the symlink target was overwritten: %q", got)
	}
}

// TestLendPlanDocRefusesSymlinkedSwarmeryDir: the same refusal when a parent
// directory of the lent copy is the symlink — nothing is created at its target.
func TestLendPlanDocRefusesSymlinkedSwarmeryDir(t *testing.T) {
	for _, linked := range []string{".swarmery", PlanDocDir} {
		t.Run(linked, func(t *testing.T) {
			wt, outside := t.TempDir(), t.TempDir()
			doc := filepath.Join(t.TempDir(), "phase-1-x.md")
			writeFile(t, doc, "# Phase 1\n")
			mustSymlink(t, outside, filepath.Join(wt, linked))

			if _, err := LendPlanDoc(wt, doc); err == nil {
				t.Error("LendPlanDoc wrote through a symlinked directory; want an error")
			}
			entries, err := os.ReadDir(outside)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Errorf("the symlink target gained %d entries; want it untouched", len(entries))
			}
		})
	}
}

// TestReturnPlanDocRefusesSymlinkSource: a lent copy that resolves through a
// symlink is never copied back, so the workspace document cannot receive the
// bytes of a file outside the worktree.
func TestReturnPlanDocRefusesSymlinkSource(t *testing.T) {
	cases := map[string]func(t *testing.T, wt, outside string){
		"lent copy is a symlink": func(t *testing.T, wt, outside string) {
			target := filepath.Join(outside, "secret")
			writeFile(t, target, "outside bytes\n")
			mustSymlink(t, target, filepath.Join(wt, PlanDocDir, "phase-1-x.md"))
		},
		"plan dir is a symlink": func(t *testing.T, wt, outside string) {
			writeFile(t, filepath.Join(outside, "phase-1-x.md"), "outside bytes\n")
			mustSymlink(t, outside, filepath.Join(wt, PlanDocDir))
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			wt, outside := t.TempDir(), t.TempDir()
			doc := filepath.Join(t.TempDir(), "phase-1-x.md")
			writeFile(t, doc, "workspace doc\n")
			setup(t, wt, outside)

			wrote, err := ReturnPlanDoc(wt, LentPlanDocRel(doc), doc)
			if wrote || err == nil {
				t.Errorf("ReturnPlanDoc = (%v, %v), want (false, error)", wrote, err)
			}
			if got := readFile(t, doc); got != "workspace doc\n" {
				t.Errorf("the workspace doc was overwritten: %q", got)
			}
		})
	}
}
