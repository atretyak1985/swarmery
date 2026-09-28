package main

// The CLI surfaces of D5's two locks: `which` names every ignored rung and each
// store's admission on STDOUT (stderr stays empty), `use` writes an unadmitted
// payer and says what did not follow, `estate use` refuses a tracked estate
// settings file unless told otherwise, and every writer refuses a tracked
// binding. Stores hold literal non-secrets; nothing asserts on a value.

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
)

// gitCLI runs git for fixture building with global and system config
// neutralised, so the operator's excludesfile cannot decide what is tracked.
func gitCLI(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-c", "user.name=swarmery test", "-c", "user.email=test@example.invalid",
		"-c", "commit.gpgsign=false", "-C", dir}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// captureStderr returns everything written to os.Stderr while fn runs.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stderr
	os.Stderr = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	w.Close()
	os.Stderr = prev
	return <-done
}

// storesAt writes key.env stores (0600, in a 0700 dir) and points the loader at
// them; body is the file's whole content.
func storesAt(t *testing.T, stores map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(secretsDirVar, dir)
	for key, body := range stores {
		p := filepath.Join(dir, key+".env")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestWhichReportsIgnoredAndAdmissionOnStdout(t *testing.T) {
	home := fakeHome(t, "default", "work", "solo")
	root := filepath.Join(home, "projects", "estate")
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	storesAt(t, map[string]string{
		"work": "# swarmery-root: " + root + "\nWORK_ONE=1\n",
		"acme": "ACME_ONE=1\n",
		"solo": "SOLO_ONE=1\n",
	})
	if err := claudeacct.SetBinding(root, "work"); err != nil {
		t.Fatal(err)
	}
	if err := claudeacct.SetEstate(root, "acme"); err != nil {
		t.Fatal(err)
	}
	// A sub-repo that COMMITS a binding: ignored, and said so.
	gitCLI(t, sub, "init", "-q", ".")
	writeSettings(t, sub, `{"swarmery":{"claudeAccount":"default"}}`)
	gitCLI(t, sub, "add", "-f", "--", ".claude/settings.local.json")
	gitCLI(t, sub, "commit", "-qm", "commit a pin")

	which := func(dir string) (stdout, stderr string) {
		var out bytes.Buffer
		stderr = captureStderr(t, func() {
			if err := accountWhich([]string{"--path", dir}, &out); err != nil {
				t.Fatalf("which %s: %v", dir, err)
			}
		})
		return out.String(), stderr
	}

	out, errOut := which(sub)
	if errOut != "" {
		t.Fatalf("which wrote %d bytes to stderr, want 0:\n%s", len(errOut), errOut)
	}
	for _, want := range []string{
		"ignored:    " + filepath.Join(sub, ".claude", "settings.local.json") + " — tracked by git",
		"admission:  work.env admitted by root " + root,
		"admission:  estate acme unanchored",
		"account:    work (pin at " + root + ")",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("which %s: missing %q in\n%s", sub, want, out)
		}
	}

	// Outside the root: the rooted account store is not admitted; a rootless
	// one is reported as such.
	outside := filepath.Join(home, "projects", "outside")
	if err := claudeacct.SetBinding(outside, "work"); err != nil {
		t.Fatal(err)
	}
	if out, _ := which(outside); !strings.Contains(out, "admission:  not admitted by work.env roots") {
		t.Errorf("which %s:\n%s", outside, out)
	}
	loner := filepath.Join(home, "projects", "loner")
	if err := claudeacct.SetBinding(loner, "solo"); err != nil {
		t.Fatal(err)
	}
	if out, errOut := which(loner); !strings.Contains(out, "admission:  solo.env rootless") || errOut != "" {
		t.Errorf("which %s: stderr %q\n%s", loner, errOut, out)
	}
	for _, leak := range []string{"WORK_ONE", "ACME_ONE", "SOLO_ONE"} {
		if strings.Contains(out, leak) {
			t.Fatalf("which printed a variable name %s", leak)
		}
	}
}

// `use` with a key whose store is rooted elsewhere WRITES the payer (it is
// gated by Lock 1 only) and prints exactly one note naming the line to add.
func TestUseUnadmittedRootedKeyWritesAndNotes(t *testing.T) {
	home := fakeHome(t, "default", "work")
	ae := filepath.Join(home, "projects", "ae")
	if err := os.MkdirAll(ae, 0o755); err != nil {
		t.Fatal(err)
	}
	storesAt(t, map[string]string{"work": "# swarmery-root: " + ae + "\nWORK_ONE=1\n"})
	dir := filepath.Join(home, "projects", "other")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := accountUse([]string{"work", "--path", dir}, &out, io.Discard, nil); err != nil {
		t.Fatalf("use: %v", err)
	}
	if got := claudeacct.Binding(dir); got != "work" {
		t.Fatalf("binding = %q, want work — the payer must be written", got)
	}
	want := dir + " pays with work but receives 0 of work.env's credentials; to release them add: " +
		claudeacct.RootLineFor(dir)
	if strings.Count(out.String(), "pays with work") != 1 || !strings.Contains(out.String(), want) {
		t.Fatalf("use output:\n%s\nwant one line %q", out.String(), want)
	}

	// Inside the root: no note.
	out.Reset()
	if err := accountUse([]string{"work", "--path", ae}, &out, io.Discard, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "receives 0") {
		t.Fatalf("an admitted path got the note:\n%s", out.String())
	}
}

// Every writer refuses a binding file git tracks, and leaves it alone.
func TestUseAndEstateRefuseTrackedBinding(t *testing.T) {
	fakeHome(t, "default", "work")
	repo := t.TempDir()
	gitCLI(t, repo, "init", "-q", ".")
	writeSettings(t, repo, `{"swarmery":{"claudeAccount":"default"}}`)
	gitCLI(t, repo, "add", "-f", "--", ".claude/settings.local.json")
	gitCLI(t, repo, "commit", "-qm", "commit a binding")
	before, _ := os.ReadFile(settingsPath(repo))

	for name, run := range map[string]func() error{
		"use":          func() error { return accountUse([]string{"work", "--path", repo}, io.Discard, io.Discard, nil) },
		"clear":        func() error { return accountClear([]string{"--path", repo}, io.Discard) },
		"estate use":   func() error { return accountEstate([]string{"use", "acme", "--path", repo}, io.Discard, io.Discard) },
		"estate clear": func() error { return accountEstate([]string{"clear", "--path", repo}, io.Discard, io.Discard) },
	} {
		err := run()
		if err == nil {
			t.Errorf("%s on a committed binding succeeded", name)
			continue
		}
		if !strings.Contains(err.Error(), "git") {
			t.Errorf("%s: %v — want the provenance reason", name, err)
		}
		if after, _ := os.ReadFile(settingsPath(repo)); !bytes.Equal(after, before) {
			t.Fatalf("%s changed the committed file", name)
		}
	}
}

// `estate use` refuses a root whose .claude/settings.json git tracks — that
// file is what the estate hands every descendant — unless the argv says so.
func TestEstateUseRefusesTrackedSettings(t *testing.T) {
	fakeHome(t, "default")
	root := t.TempDir()
	gitCLI(t, root, "init", "-q", ".")
	if err := os.MkdirAll(filepath.Join(root, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".claude", "settings.json"), []byte(`{"enabledPlugins":{}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCLI(t, root, "add", "-f", "--", ".claude/settings.json")
	gitCLI(t, root, "commit", "-qm", "commit the project settings")

	err := accountEstate([]string{"use", "acme", "--path", root}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--allow-tracked-settings") {
		t.Fatalf("estate use on tracked settings: err = %v, want a refusal naming --allow-tracked-settings", err)
	}
	if key, _ := claudeacct.Estate(root); key != "" {
		t.Fatalf("the refused declaration was written: %q", key)
	}
	var out bytes.Buffer
	if err := accountEstate([]string{"use", "acme", "--path", root, "--allow-tracked-settings"}, &out, io.Discard); err != nil {
		t.Fatalf("estate use --allow-tracked-settings: %v", err)
	}
	if key, _ := claudeacct.Estate(root); key != "acme" {
		t.Fatalf("estate after the allowed write = %q", key)
	}
	// No store yet: unanchored, and one line names the root line to add.
	if !strings.Contains(out.String(), "estate acme is unanchored") || !strings.Contains(out.String(), claudeacct.RootLineFor(root)) {
		t.Fatalf("estate use output:\n%s", out.String())
	}
}
