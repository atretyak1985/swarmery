package taskdir

import (
	"os"
	"path/filepath"
	"testing"
)

// DirIn must treat the namespace dir as opaque. The whole point of the variant
// is that a caller holding workspaces.root_path passes it through instead of
// rebuilding it from a slug that may not name the carved dir.
func TestDirIn_UsesNamespaceDirVerbatim(t *testing.T) {
	// A workspace carved by onboarding ("skygor") addressed by a caller that
	// only holds the registry slug ("-home-dev-skygor") — the split that minted
	// a second tree beside the real one.
	root := "/ws"
	got := DirIn(filepath.Join(root, "skygor"), "T-42", dispatchedAt)
	want := filepath.Join(root, "skygor", "workspace", "working", "2026", "08", "17", "card-t-42")
	if got != want {
		t.Fatalf("DirIn = %q, want %q", got, want)
	}
	if other := Dir(root, "-home-dev-skygor", "T-42", dispatchedAt); other == got {
		t.Fatal("registry-slug spelling collided with the carved dir — test no longer covers the split")
	}
}

// Dir must stay exactly DirIn(<root>/<project>) so the existing path contract
// (wsingest derives external_id from these segments) is untouched by the split.
func TestDir_IsDirInOfJoinedRoot(t *testing.T) {
	if got, want := Dir("/ws", "proj", "T-42", dispatchedAt),
		DirIn(filepath.Join("/ws", "proj"), "T-42", dispatchedAt); got != want {
		t.Fatalf("Dir = %q, want %q", got, want)
	}
}

func TestMintMicroPlanIn_MintsUnderNamespaceDir(t *testing.T) {
	wsDir := filepath.Join(t.TempDir(), "skygor")
	dir, err := MintMicroPlanIn(wsDir, testCard(), dispatchedAt)
	if err != nil {
		t.Fatalf("MintMicroPlanIn: %v", err)
	}
	if want := DirIn(wsDir, "T-42", dispatchedAt); dir != want {
		t.Fatalf("dir = %q, want %q", dir, want)
	}
	if _, err := os.Stat(PhaseDocPath(dir)); err != nil {
		t.Fatalf("phase doc not written: %v", err)
	}
}

// Idempotence is a promise of MintMicroPlan (a re-dispatch must not erase ticks
// or a Completion Report); the new entry point must keep it.
func TestMintMicroPlanIn_IdempotentByDir(t *testing.T) {
	wsDir := filepath.Join(t.TempDir(), "skygor")
	first, err := MintMicroPlanIn(wsDir, testCard(), dispatchedAt)
	if err != nil {
		t.Fatalf("first mint: %v", err)
	}
	edited := "ticked by the executor\n"
	if err := os.WriteFile(PhaseDocPath(first), []byte(edited), 0o644); err != nil {
		t.Fatalf("write phase doc: %v", err)
	}

	second, err := MintMicroPlanIn(wsDir, testCard(), dispatchedAt)
	if err != nil {
		t.Fatalf("second mint: %v", err)
	}
	if second != first {
		t.Fatalf("second mint moved the dir: %q -> %q", first, second)
	}
	if got := read(t, PhaseDocPath(first)); got != edited {
		t.Fatalf("re-mint overwrote the phase doc: %q", got)
	}
}

func TestMintMicroPlanIn_RequiresWorkspaceDir(t *testing.T) {
	if _, err := MintMicroPlanIn("", testCard(), dispatchedAt); err == nil {
		t.Fatal("expected an error for an empty workspace dir")
	}
}
