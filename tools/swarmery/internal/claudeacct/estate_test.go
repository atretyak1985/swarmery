package claudeacct

// The estate WRITER follows SetBinding's surgery discipline, and the two writers
// share one namespace without deleting each other's field.

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSetEstate_PreservesForeignKeys(t *testing.T) {
	dir := t.TempDir()
	path := writeSettingsFile(t, dir, foreignSettings)
	before := parseJSON(t, readFile(t, path))

	if err := SetEstate(dir, "demo"); err != nil {
		t.Fatalf("SetEstate: %v", err)
	}
	after := parseJSON(t, readFile(t, path))
	for k, v := range before {
		if !reflect.DeepEqual(after[k], v) {
			t.Errorf("foreign key %q changed: %v → %v", k, v, after[k])
		}
	}
	if key, root := Estate(dir); key != "demo" || root != dir {
		t.Fatalf("Estate = %q at %q, want demo at %s", key, root, dir)
	}

	// Removing it restores the foreign content and prunes the empty namespace.
	if err := SetEstate(dir, ""); err != nil {
		t.Fatalf("SetEstate clear: %v", err)
	}
	cleared := parseJSON(t, readFile(t, path))
	if _, ok := cleared["swarmery"]; ok {
		t.Fatalf("namespace survived with nothing of ours left: %v", cleared["swarmery"])
	}
	if !reflect.DeepEqual(cleared, before) {
		t.Fatalf("declare+remove changed foreign content:\n%v\nvs\n%v", cleared, before)
	}
}

func TestSetEstate_AbortsOnUnparseableJSON(t *testing.T) {
	dir := t.TempDir()
	path := writeSettingsFile(t, dir, "{")
	err := SetEstate(dir, "demo")
	if err == nil {
		t.Fatal("SetEstate over unparseable JSON returned nil")
	}
	if got := readFile(t, path); !bytes.Equal(got, []byte("{")) {
		t.Fatalf("file rewritten over unparseable JSON: %q", got)
	}
	if _, serr := os.Stat(path + ".bak"); !os.IsNotExist(serr) {
		t.Fatal("a .bak was written by an aborted call")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("aborting without writing")) {
		t.Errorf("error %q is not readSettings' abort message", err)
	}
}

func TestSetEstate_IdempotentAndBacksUpOnce(t *testing.T) {
	dir := t.TempDir()
	path := writeSettingsFile(t, dir, foreignSettings)
	if err := SetEstate(dir, "demo"); err != nil {
		t.Fatal(err)
	}
	first := readFile(t, path)
	if got := readFile(t, path+".bak"); !bytes.Equal(got, []byte(foreignSettings)) {
		t.Fatal(".bak does not hold the pre-write bytes")
	}
	if err := SetEstate(dir, "demo"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); !bytes.Equal(got, first) {
		t.Fatal("a second identical SetEstate changed the file")
	}
	// A later, different write keeps the ORIGINAL .bak.
	if err := SetEstate(dir, "other"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path+".bak"); !bytes.Equal(got, []byte(foreignSettings)) {
		t.Fatal(".bak was overwritten by a second write")
	}
	// Removing a declaration that is not there touches nothing.
	fresh := t.TempDir()
	if err := SetEstate(fresh, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bindingPath(fresh)); !os.IsNotExist(err) {
		t.Fatal("clearing an undeclared estate created a file")
	}
	unrelated := t.TempDir()
	upath := writeSettingsFile(t, unrelated, `{"swarmery":{"claudeAccount":"work"}}`)
	if err := SetEstate(unrelated, ""); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, upath); string(got) != `{"swarmery":{"claudeAccount":"work"}}` {
		t.Fatalf("clearing an absent estate reformatted the file: %q", got)
	}
}

func TestSetEstate_CreatesTheFileWhenMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new")
	if err := SetEstate(dir, "demo"); err != nil {
		t.Fatalf("SetEstate on a missing file: %v", err)
	}
	if key, _ := Estate(dir); key != "demo" {
		t.Fatalf("Estate = %q after create", key)
	}
	if _, err := os.Stat(bindingPath(dir) + ".bak"); !os.IsNotExist(err) {
		t.Fatal("a .bak was written for a file that did not exist")
	}
}

func TestSetEstate_RejectsAnUnsafeKeyBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	for _, bad := range []string{"../../etc", "a/b", ".hidden", "a b", ".."} {
		if err := SetEstate(dir, bad); err == nil {
			t.Errorf("SetEstate(%q) accepted", bad)
		}
	}
	if _, err := os.Stat(bindingPath(dir)); !os.IsNotExist(err) {
		t.Fatal("a refused key still wrote the file")
	}
	if err := SetEstate("", "demo"); err == nil {
		t.Fatal("SetEstate(\"\") accepted an empty directory")
	}
	if key, root := Estate(""); key != "" || root != "" {
		t.Fatal("Estate(\"\") read something")
	}
}

// THE shared-namespace property, both directions: clearing one axis leaves the
// other on disk.
func TestSetEstate_AndSetBindingDoNotDeleteEachOther(t *testing.T) {
	dir := t.TempDir()
	if err := SetBinding(dir, "work"); err != nil {
		t.Fatal(err)
	}
	if err := SetEstate(dir, "demo"); err != nil {
		t.Fatal(err)
	}
	// SetEstate(dir, "") keeps the binding.
	if err := SetEstate(dir, ""); err != nil {
		t.Fatal(err)
	}
	if got := Binding(dir); got != "work" {
		t.Fatalf("SetEstate(dir, \"\") removed the account binding: %q", got)
	}
	// SetBinding(dir, "") keeps the estate — the prune must not destroy it.
	if err := SetEstate(dir, "demo"); err != nil {
		t.Fatal(err)
	}
	if err := SetBinding(dir, ""); err != nil {
		t.Fatal(err)
	}
	if key, _ := Estate(dir); key != "demo" {
		t.Fatalf("SetBinding(dir, \"\") removed the estate declaration: %q", key)
	}
	// With both gone, the namespace is pruned.
	if err := SetEstate(dir, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := parseJSON(t, readFile(t, bindingPath(dir)))["swarmery"]; ok {
		t.Fatal("empty swarmery namespace was not pruned")
	}
}

// Estate is the non-walking reader: a declaration on an ancestor is not "at" dir.
func TestEstate_DoesNotWalk(t *testing.T) {
	home := fakeHome(t)
	root := filepath.Join(home, "projects", "acme")
	declare(t, root, map[string]any{"estate": "acme"})
	child := filepath.Join(root, "child")
	mkdirs(t, child)
	if key, _ := Estate(child); key != "" {
		t.Fatalf("Estate(child) = %q, want \"\" — only Resolve climbs", key)
	}
	if key, r := Estate(root); key != "acme" || r != root {
		t.Fatalf("Estate(root) = %q at %q", key, r)
	}
	writeSettingsFile(t, child, "{")
	if key, _ := Estate(child); key != "" {
		t.Fatal("malformed file yielded an estate")
	}
}
