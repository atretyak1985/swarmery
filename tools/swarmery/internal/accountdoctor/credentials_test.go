package accountdoctor

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// storeFindings is every finding that is about a credential store.
func storeFindings(rep Report) []Finding {
	var out []Finding
	for _, f := range rep.Findings {
		switch f.ID {
		case "estate-unanchored", "store-rootless", "store-refused":
			out = append(out, f)
		}
	}
	return out
}

// Criterion 27 (i): an EMPTY store carrying its root line is the healthy,
// credential-free estate — zero credentials, varsMissing [] and no finding
// about the store at any severity.
func TestEstateEmptyAnchoredStore(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	store := f.anchoredEstate(t, root, "estate", "")

	rep, err := Fast(Options{Path: root})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Credentials != 0 || rep.CredentialStore != store {
		t.Errorf("credentials = %d store = %q, want 0 and %q", rep.Credentials, rep.CredentialStore, store)
	}
	if rep.VarsMissing == nil || len(rep.VarsMissing) != 0 {
		t.Errorf("VarsMissing = %#v, want []", rep.VarsMissing)
	}
	if got := storeFindings(rep); len(got) != 0 {
		t.Errorf("findings about the store = %+v, want none", got)
	}
	if countSeverity(rep, SevError) != 0 {
		t.Errorf("error findings on a healthy estate: %+v", rep.Findings)
	}

	// …plus one enabled pack referencing a ${VAR} nothing supplies: the ONE
	// escalation, by name, while credentials stays 0.
	installed := map[string][]installRecord{}
	f.plugin(t, installed, "db@m", `{"mcpServers":{"db":{"env":{"H":"${PACK_DB_HOST}"}}}}`)
	f.writeInstalled(t, installed)
	f.enable(t, map[string]bool{"db@m": true})
	t.Setenv("PACK_DB_HOST", "")
	rep, err = Fast(Options{Path: root})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rep.VarsMissing, []string{"PACK_DB_HOST"}) || rep.Credentials != 0 {
		t.Errorf("varsMissing = %v credentials = %d", rep.VarsMissing, rep.Credentials)
	}
	var errs []Finding
	for _, f := range rep.Findings {
		if f.Severity == SevError {
			errs = append(errs, f)
		}
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Detail, "PACK_DB_HOST") {
		t.Errorf("error findings = %+v, want exactly one naming PACK_DB_HOST", errs)
	}
}

// Criterion 27 (ii): NO store file — unanchored: zero credentials, no store
// path, the estate still reported, exactly one estate-unanchored warn and no
// error.
func TestEstateWithoutStore(t *testing.T) {
	newFixture(t)
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".claude", "settings.local.json"), `{"swarmery":{"estate":"estate"}}`, 0o644)

	rep, err := Fast(Options{Path: root})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Credentials != 0 || rep.CredentialStore != "" {
		t.Errorf("credentials = %d store = %q", rep.Credentials, rep.CredentialStore)
	}
	if rep.Estate != "estate" || rep.EstateRoot != root {
		t.Errorf("estate = %q root = %q", rep.Estate, rep.EstateRoot)
	}
	if n := countFindings(rep, "estate-unanchored", SevWarn); n != 1 {
		t.Errorf("estate-unanchored warns = %d: %+v", n, rep.Findings)
	}
	if n := countSeverity(rep, SevError); n != 0 {
		t.Errorf("error findings = %d: %+v", n, rep.Findings)
	}
}

// Criterion 27 (iii): a store whose only root is a SIBLING (<root>-evil) does
// not admit the root — admission is os.SameFile, never a string prefix.
func TestEstateStoreNotAdmitting(t *testing.T) {
	f := newFixture(t)
	parent := t.TempDir()
	root := filepath.Join(parent, "estate")
	evil := root + "-evil"
	mustMkdir(t, root, 0o755)
	mustMkdir(t, evil, 0o755)
	mustWrite(t, filepath.Join(root, ".claude", "settings.local.json"), `{"swarmery":{"estate":"estate"}}`, 0o644)
	f.store(t, "estate", "# swarmery-root: "+evil+"\nPACK_NAME=zzq-not-released\n")

	rep, err := Fast(Options{Path: root})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Credentials != 0 || rep.CredentialStore != "" {
		t.Errorf("credentials = %d store = %q, want 0 and \"\"", rep.Credentials, rep.CredentialStore)
	}
	if n := countFindings(rep, "estate-unanchored", SevWarn); n != 1 {
		t.Errorf("estate-unanchored warns = %d: %+v", n, rep.Findings)
	}
	for _, fd := range rep.Findings {
		if fd.ID == "estate-unanchored" && !strings.Contains(fd.Detail, "do not admit") {
			t.Errorf("detail = %q, want the not-admitting reason", fd.Detail)
		}
	}
	raw, _ := json.Marshal(rep)
	if strings.Contains(string(raw), "zzq-not-released") {
		t.Error("a store value reached the report")
	}
}

// A store present with no root line is unanchored too (the third reason).
func TestEstateRootlessStoreIsUnanchored(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".claude", "settings.local.json"), `{"swarmery":{"estate":"estate"}}`, 0o644)
	f.store(t, "estate", "PACK_NAME=x\n")
	rep, _ := Fast(Options{Path: root})
	if n := countFindings(rep, "estate-unanchored", SevWarn); n != 1 {
		t.Fatalf("estate-unanchored = %d: %+v", n, rep.Findings)
	}
	for _, fd := range rep.Findings {
		if fd.ID == "estate-unanchored" && !strings.Contains(fd.Detail, "no root line") {
			t.Errorf("detail = %q, want the no-root-line reason", fd.Detail)
		}
	}
}
