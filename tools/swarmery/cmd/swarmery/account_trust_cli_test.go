package main

// The CLI half of the writer/reader agreement: `use` leaves a file the walk
// reads (or says why it could not), `which` names a binding file it ignores,
// `estate clear` refuses to edit an untrusted file, and `estate use|show`
// describe a credential store as present, absent or REFUSED — never by path,
// name or value. Every tree is a t.TempDir(); values are literal non-secrets.

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
)

func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

// A pre-existing 0664 settings.local.json is never promoted: `account use`
// refuses, naming the path, the reason and the fix, and the file keeps its
// bytes and mode and grows no .bak. After the operator fixes the mode, the same
// command binds.
func TestAccountUseLeavesATrustedFile(t *testing.T) {
	fakeHome(t, "default", "work")
	dir := t.TempDir()
	const body = `{"permissions":{}}`
	writeSettings(t, dir, body)
	if err := os.Chmod(settingsPath(dir), 0o664); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := accountUse([]string{"work", "--path", dir}, &out, io.Discard, nil)
	if err == nil || !strings.Contains(err.Error(), settingsPath(dir)) ||
		!strings.Contains(err.Error(), "group or other") || !strings.Contains(err.Error(), "chmod go-w") {
		t.Fatalf("account use over a 0664 file: err = %v, want a refusal naming the path, the reason and the fix", err)
	}
	if strings.Contains(out.String(), "bound") {
		t.Errorf("stdout claimed success: %q", out.String())
	}
	if raw, _ := os.ReadFile(settingsPath(dir)); string(raw) != body {
		t.Errorf("a refused use changed the file: %q", raw)
	}
	if m := modeOf(t, settingsPath(dir)); m != 0o664 {
		t.Errorf("a refused use changed the mode to %04o", m)
	}
	if _, statErr := os.Lstat(settingsPath(dir) + ".bak"); !os.IsNotExist(statErr) {
		t.Error("a refused use left a .bak")
	}

	// The operator's fix, then the re-run the refusal asked for.
	if err := os.Chmod(settingsPath(dir), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := accountUse([]string{"work", "--path", dir}, &out, io.Discard, nil); err != nil {
		t.Fatalf("account use after chmod go-w: %v", err)
	}
	if got := claudeacct.Binding(dir); got != "work" {
		t.Errorf("Binding after account use = %q, want work", got)
	}
	if !strings.Contains(out.String(), "bound "+dir+" → work") {
		t.Errorf("stdout = %q", out.String())
	}
}

// --clear-pins over an estate root with an UNTRUSTED descendant binding file and
// a pin whose clear is refused: the untrusted file is reported by path and
// reason (never read, never rewritten), the refused pin is skipped and named,
// every other pin is still cleared, the root is bound, and the run ends
// non-zero naming what was skipped.
func TestAccountUseClearPinsSkipsAndReports(t *testing.T) {
	fakeHome(t, "default", "work")
	root := t.TempDir()
	if err := claudeacct.SetBinding(root, "default"); err != nil {
		t.Fatal(err)
	}
	if err := claudeacct.SetEstate(root, "acme"); err != nil {
		t.Fatal(err)
	}
	ok1, ok2, stuck, bad := filepath.Join(root, "a"), filepath.Join(root, "b"), filepath.Join(root, "c"), filepath.Join(root, "u")
	for _, p := range []string{ok1, ok2, stuck} {
		if err := claudeacct.SetBinding(p, "work"); err != nil {
			t.Fatal(err)
		}
	}
	// A .bak that is a symlink makes the clear at `stuck` a refused write.
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("untouched"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, settingsPath(stuck)+".bak"); err != nil {
		t.Fatal(err)
	}
	const badBody = `{"swarmery":{"claudeAccount":"work"},"permissions":{"allow":["CONTENT_MARKER"]}}`
	writeSettings(t, bad, badBody)
	if err := os.Chmod(settingsPath(bad), 0o664); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	err := accountUse([]string{"work", "--path", root, "--clear-pins=all"}, &out, &errOut, nil)
	if err == nil || !strings.Contains(err.Error(), stuck) || strings.Contains(err.Error(), ok1) {
		t.Fatalf("err = %v, want a non-zero end naming only %s", err, stuck)
	}
	if !strings.Contains(out.String(), "bound "+root+" → work") || claudeacct.Binding(root) != "work" {
		t.Errorf("the root was not bound: stdout %q, binding %q", out.String(), claudeacct.Binding(root))
	}
	if got := claudeacct.ShadowingPins(root); len(got) != 1 || got[0] != stuck {
		t.Errorf("pins left = %v, want only the refused %s", got, stuck)
	}
	for _, want := range []string{"skipped: " + settingsPath(bad), "group or other", "skipped pin: " + stuck, "cleared pin: " + ok1, "cleared pin: " + ok2} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("stderr = %q, missing %q", errOut.String(), want)
		}
	}
	if strings.Contains(errOut.String(), "CONTENT_MARKER") {
		t.Errorf("stderr printed the untrusted file's contents: %q", errOut.String())
	}
	if raw, _ := os.ReadFile(settingsPath(bad)); string(raw) != badBody || modeOf(t, settingsPath(bad)) != 0o664 {
		t.Errorf("the untrusted descendant was rewritten: %q %04o", raw, modeOf(t, settingsPath(bad)))
	}
	if raw, _ := os.ReadFile(victim); string(raw) != "untouched" {
		t.Errorf("the symlinked .bak's target was written: %q", raw)
	}
}

// A file that already holds the key but is untrusted is not rewritten (the write
// is a no-op), so `use` must fail and say why rather than print "bound".
func TestAccountUseRefusesAWriteThatDoesNotReadBack(t *testing.T) {
	fakeHome(t, "default", "work")
	dir := t.TempDir()
	writeSettings(t, dir, `{"swarmery":{"claudeAccount":"work"}}`)
	if err := os.Chmod(settingsPath(dir), 0o664); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := accountUse([]string{"work", "--path", dir}, &out, io.Discard, nil)
	if err == nil || !strings.Contains(err.Error(), "does not take effect") || !strings.Contains(err.Error(), "group or other") {
		t.Fatalf("account use over an untrusted already-bound file: err = %v, want the read-back refusal", err)
	}
	if strings.Contains(out.String(), "bound") {
		t.Errorf("stdout claimed success: %q", out.String())
	}
}

// `which` names the project's own binding file when it exists but is ignored —
// path and reason, never contents — and says nothing for a trusted one.
func TestAccountWhichNamesAnIgnoredBindingFile(t *testing.T) {
	fakeHome(t, "default", "work")
	t.Setenv(secretsDirVar, t.TempDir())
	dir := t.TempDir()
	writeSettings(t, dir, `{"swarmery":{"claudeAccount":"work"},"permissions":{"allow":["CONTENT_MARKER"]}}`)

	var out bytes.Buffer
	if err := accountWhich([]string{"--path", dir}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "ignored:") {
		t.Fatalf("a trusted file was reported as ignored: %q", out.String())
	}

	if err := os.Chmod(settingsPath(dir), 0o664); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := accountWhich([]string{"--path", dir}, &out); err != nil {
		t.Fatal(err)
	}
	if n := countLines(out.String(), "ignored:"); n != 1 {
		t.Fatalf("which = %q, want exactly one ignored: line", out.String())
	}
	for _, want := range []string{"ignored:    " + settingsPath(dir), "group or other", "account:    default"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("which = %q, missing %q", out.String(), want)
		}
	}
	if strings.Contains(out.String(), "CONTENT_MARKER") {
		t.Errorf("which printed the ignored file's contents: %q", out.String())
	}
}

// `estate clear` over an untrusted file refuses: the file is byte-identical
// afterwards, keeps its mode, and grows no .bak.
func TestAccountEstateClearRefusesAnUntrustedFile(t *testing.T) {
	fakeHome(t, "default")
	t.Setenv(secretsDirVar, t.TempDir())
	dir := t.TempDir()
	const body = `{"swarmery":{"estate":"acme","claudeAccount":"work"}}`
	writeSettings(t, dir, body)
	if err := os.Chmod(settingsPath(dir), 0o664); err != nil {
		t.Fatal(err)
	}
	err := accountEstate([]string{"clear", "--path", dir}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "refusing to clear") || !strings.Contains(err.Error(), "group or other") {
		t.Fatalf("estate clear over a 0664 file: err = %v, want a refusal with the reason", err)
	}
	if raw, _ := os.ReadFile(settingsPath(dir)); string(raw) != body {
		t.Errorf("a refused estate clear changed the file: %q", raw)
	}
	if m := modeOf(t, settingsPath(dir)); m != 0o664 {
		t.Errorf("a refused estate clear changed the mode to %04o", m)
	}
	if _, statErr := os.Lstat(settingsPath(dir) + ".bak"); !os.IsNotExist(statErr) {
		t.Error("a refused estate clear left a .bak")
	}
}

// A store the loader refuses is reported as REFUSED with the reason by `estate
// use` and `estate show` — not as "present", not as "absent" — and neither
// prints the store's path, a variable name or a value.
func TestAccountEstateReportsARefusedStore(t *testing.T) {
	fakeHome(t, "default")
	storeDir := seedSecretStore(t, "acme")
	if err := os.Chmod(filepath.Join(storeDir, "acme.env"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()

	var use, show bytes.Buffer
	if err := accountEstate([]string{"use", "acme", "--path", dir}, &use, io.Discard); err != nil {
		t.Fatalf("estate use: %v", err)
	}
	if err := accountEstate([]string{"show", "--path", dir}, &show, io.Discard); err != nil {
		t.Fatalf("estate show: %v", err)
	}
	for name, got := range map[string]string{"use": use.String(), "show": show.String()} {
		if !strings.Contains(got, "credential store REFUSED") || !strings.Contains(got, "mode 0644") {
			t.Errorf("estate %s = %q, want the REFUSED state with the mode reason", name, got)
		}
		if strings.Contains(got, "present") || strings.Contains(got, "no credential store on this machine") {
			t.Errorf("estate %s = %q, called a refused store present or absent", name, got)
		}
		for _, leak := range []string{storeDir, storeSecretVar, storeSecretValue} {
			if strings.Contains(got, leak) {
				t.Errorf("estate %s printed %q", name, leak)
			}
		}
	}
	if !strings.Contains(show.String(), "credentials: 0 (") {
		t.Errorf("estate show = %q, want a zero count for a refused store", show.String())
	}

	// And a store directory open beyond its owner is refused the same way.
	if err := os.Chmod(filepath.Join(storeDir, "acme.env"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	show.Reset()
	if err := accountEstate([]string{"show", "--path", dir}, &show, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(show.String(), "REFUSED: the store directory's mode 0755") {
		t.Errorf("estate show with an open store dir = %q", show.String())
	}
	// Present, for contrast: the count is printed. (D5: anchored at the
	// declaring directory, so it is admitted.)
	if err := os.Chmod(storeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	anchorCLIStore(t, storeDir, "acme", dir)
	show.Reset()
	if err := accountEstate([]string{"show", "--path", dir}, &show, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(show.String(), "credentials: 1 (credential store present: 1 names)") {
		t.Errorf("estate show with a healthy store = %q", show.String())
	}
}
