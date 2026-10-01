package accountdoctor

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeproj"
)

const dupSentinel = "zzq-dup-sentinel"

// Criterion 25 (fixture half): one planted duplicate of each Kind is
// reported, by name, never by value — and nothing else is.
func TestStaleDuplicates(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	// (i) two stores sharing one NAME with DIFFERENT values
	alpha := f.store(t, "alpha", "ALPHA_ONLY="+dupSentinel+"-a1\nPACK_SHARED="+dupSentinel+"-a2\n")
	beta := f.store(t, "beta", "PACK_SHARED="+dupSentinel+"-b1\nBETA_ONLY="+dupSentinel+"-b2\n")
	f.anchoredEstate(t, root, "estate", "") // an empty store pairs with nothing

	// (ii) the estate's settings, and a sub-repo copy that carries NO binding
	estateFile := filepath.Join(root, ".claude", "settings.json")
	mustWrite(t, estateFile, `{
		"pluginConfigs": {"a@m": {"options": {"k": "`+dupSentinel+`-pc"}}, "b@m": {"options": {}}},
		"extraKnownMarketplaces": {"mk": {"source": {"source": "github", "repo": "o/r"}}},
		"permissions": {"allow": ["Bash(ls)"]},
		"enabledPlugins": {"a@m": true}
	}`, 0o644)
	subFile := filepath.Join(root, "repos", "sub", ".claude", "settings.local.json")
	mustWrite(t, subFile, `{
		"pluginConfigs": {"a@m": {"options": {"k": "`+dupSentinel+`-pc"}}},
		"extraKnownMarketplaces": {"mk": {"source": {"source": "github", "repo": "o/r"}}, "extra": {"source": {"source": "directory", "path": "/x"}}},
		"permissions": {"allow": ["Bash(ls)"]},
		"enabledPlugins": {"a@m": true}
	}`, 0o644)

	// (iii) a lent binding in a fixture worktree root, whose slug is the estate
	// root's — the source binding joins its paths.
	lent := filepath.Join(f.home, ".swarmery", "worktrees", claudeproj.Slug(root), "task-1", ".claude", "settings.local.json")
	mustWrite(t, lent, `{"swarmery":{"claudeAccount":"default","estate":"estate"},"permissions":{}}`, 0o644)

	rep, err := Fast(Options{Path: root})
	if err != nil {
		t.Fatal(err)
	}
	if rep.SettingsFile == "" {
		t.Fatal("precondition: the estate is not admitted")
	}
	byKind := map[string][]Duplicate{}
	for _, d := range rep.StaleDuplicates {
		byKind[d.Kind] = append(byKind[d.Kind], d)
	}
	if len(byKind) != 3 {
		t.Errorf("kinds = %v, want exactly the three the contract defines", byKind)
	}
	if cs := byKind[KindCredentialStore]; len(cs) != 1 ||
		!reflect.DeepEqual(cs[0].Paths, []string{alpha, beta}) ||
		!reflect.DeepEqual(cs[0].Overlap, []string{"PACK_SHARED"}) || cs[0].Count != 1 || cs[0].Key != "" {
		t.Errorf("credential-store = %+v", cs)
	}
	if sb := byKind[KindSettingsBlock]; len(sb) != 1 || sb[0].Key != "pluginConfigs" ||
		!reflect.DeepEqual(sb[0].Paths, []string{estateFile, subFile}) ||
		!reflect.DeepEqual(sb[0].Overlap, []string{"a@m"}) || sb[0].Count != 1 {
		t.Errorf("settings-block = %+v, want only pluginConfigs (the subset); extraKnownMarketplaces carries an entry the estate lacks", sb)
	}
	if b := byKind[KindBinding]; len(b) != 1 || b[0].Key != "swarmery" ||
		!reflect.DeepEqual(b[0].Overlap, []string{"claudeAccount", "estate"}) || b[0].Count != 2 ||
		!reflect.DeepEqual(b[0].Paths, []string{lent, filepath.Join(root, ".claude", "settings.local.json")}) {
		t.Errorf("binding = %+v", b)
	}

	raw := mustJSON(t, rep)
	for _, k := range []string{`"kind"`, `"paths"`, `"key"`, `"overlap"`, `"count"`} {
		if !strings.Contains(raw, k) {
			t.Errorf("a duplicate lacks %s", k)
		}
	}
	var js, txt bytes.Buffer
	if err := RenderJSON(&js, rep); err != nil {
		t.Fatal(err)
	}
	if err := RenderText(&txt, rep); err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{raw, js.String(), txt.String()} {
		if strings.Contains(out, dupSentinel) {
			t.Error("a fixture VALUE reached the output")
		}
	}
	if !strings.Contains(txt.String(), "PACK_SHARED") {
		t.Error("the text rendering lacks the overlapping NAME")
	}
}

// Criterion 24: a clean fixture — one store, no duplicated settings key — has
// "staleDuplicates":[] in its raw JSON, and it unmarshals to a non-nil empty
// slice.
func TestStaleDuplicatesEmptyIsArray(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	f.anchoredEstate(t, root, "estate", "PACK_ONLY=x\n")
	mustWrite(t, filepath.Join(root, ".claude", "settings.json"), `{"pluginConfigs":{"a@m":{}}}`, 0o644)
	mustWrite(t, filepath.Join(root, "sub", ".claude", "settings.json"), `{"pluginConfigs":{"a@m":{"other":1}}}`, 0o644)

	rep, err := Fast(Options{Path: root})
	if err != nil {
		t.Fatal(err)
	}
	var js bytes.Buffer
	if err := RenderJSON(&js, rep); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(js.String(), `"staleDuplicates":[]`) {
		t.Fatalf("raw JSON lacks \"staleDuplicates\":[]: %s", js.String())
	}
	var back struct {
		StaleDuplicates []Duplicate `json:"staleDuplicates"`
	}
	if err := json.Unmarshal(js.Bytes(), &back); err != nil {
		t.Fatal(err)
	}
	if back.StaleDuplicates == nil || len(back.StaleDuplicates) != 0 {
		t.Errorf("unmarshalled = %#v, want a non-nil empty slice", back.StaleDuplicates)
	}
	if got := StaleDuplicates(claudeResolve(root)); got == nil || len(got) != 0 {
		t.Errorf("StaleDuplicates = %#v, want []", got)
	}
}

// An UNADMITTED estate delivers nothing, so a sub-repo's copy is the only live
// one and is never a duplicate; a store the mode gate refuses is a warn
// finding, never a duplicate.
func TestStaleDuplicatesUnadmittedAndRefused(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".claude", "settings.local.json"), `{"swarmery":{"estate":"estate"}}`, 0o644)
	mustWrite(t, filepath.Join(root, ".claude", "settings.json"), `{"pluginConfigs":{"a@m":{}}}`, 0o644)
	mustWrite(t, filepath.Join(root, "sub", ".claude", "settings.json"), `{"pluginConfigs":{"a@m":{}}}`, 0o644)
	open := f.store(t, "open", "PACK_X=1\n")
	chmod(t, open, 0o644)
	f.store(t, "other", "PACK_X=2\n")

	rep, _ := Fast(Options{Path: root})
	if len(rep.StaleDuplicates) != 0 {
		t.Errorf("staleDuplicates = %+v, want none", rep.StaleDuplicates)
	}
	if countFindings(rep, "store-refused", SevWarn) != 1 {
		t.Errorf("findings = %+v, want one store-refused warn", rep.Findings)
	}
}

// The settings-block detector reports exactly what `account prune` removes: a
// redundant copy in the estate root's own binding file, in a nested estate's
// supply, in a file that resolves to that nested estate, or behind a .claude
// symlinked out of the estate is never a duplicate — prune never removes any
// of them, so reporting one would be a finding no command clears. An ordinary
// sub-repo copy still is.
func TestStaleDuplicatesSettingsBlockMatchesPruneScope(t *testing.T) {
	f := newFixture(t)
	base := t.TempDir()
	root := filepath.Join(base, "estate")
	const pc = `{"pluginConfigs":{"a@m":{}}}`
	f.anchoredEstate(t, root, "estate", "")
	mustWrite(t, filepath.Join(root, ".claude", "settings.local.json"),
		`{"swarmery":{"estate":"estate"},"pluginConfigs":{"a@m":{}}}`, 0o644) // the root's binding: an estate source
	estateFile := filepath.Join(root, ".claude", "settings.json")
	mustWrite(t, estateFile, pc, 0o644)
	plain := filepath.Join(root, "repos", "plain", ".claude", "settings.json")
	mustWrite(t, plain, pc, 0o644)

	sub := filepath.Join(root, "sub") // a nested, admitted estate
	f.anchoredEstate(t, sub, "sub", "")
	mustWrite(t, filepath.Join(sub, ".claude", "settings.json"), pc, 0o644)
	mustWrite(t, filepath.Join(sub, "x", ".claude", "settings.json"), pc, 0o644)

	outside := filepath.Join(base, "outside") // a .claude linked out of the estate
	mustWrite(t, filepath.Join(outside, ".claude", "settings.json"), pc, 0o644)
	mustMkdir(t, filepath.Join(root, "linked"), 0o755)
	if err := os.Symlink(filepath.Join(outside, ".claude"), filepath.Join(root, "linked", ".claude")); err != nil {
		t.Fatal(err)
	}

	var got [][]string
	for _, d := range StaleDuplicates(claudeResolve(root)) {
		if d.Kind == KindSettingsBlock {
			got = append(got, append([]string{d.Key}, d.Paths...))
		}
	}
	want := [][]string{{"pluginConfigs", estateFile, plain}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("settings-block duplicates = %v\nwant %v — only the copy `account prune` removes", got, want)
	}
}
