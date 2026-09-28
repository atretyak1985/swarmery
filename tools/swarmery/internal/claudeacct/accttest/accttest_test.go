package accttest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
)

func TestAdmitEstateMakesTheEstateAdmitted(t *testing.T) {
	t.Setenv("SWARMERY_SECRETS_DIR", "")
	root := t.TempDir()
	if err := claudeacct.SetEstate(root, "acme"); err != nil {
		t.Fatalf("SetEstate: %v", err)
	}
	if claudeacct.Resolve(root).EstateAdmitted {
		t.Fatal("an estate with no store must not be admitted")
	}
	AdmitEstate(t, "acme", root)
	if !claudeacct.Resolve(root).EstateAdmitted {
		t.Fatal("AdmitEstate did not admit the estate")
	}
	fi, err := os.Stat(filepath.Join(os.Getenv("SWARMERY_SECRETS_DIR"), "acme.env"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("store = %v, %v; want a 0600 file", fi, err)
	}
}
