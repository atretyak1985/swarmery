package claudeacct

// The writer/reader agreement: a file the writer produces is always one the
// read side trusts (writeSettings refuses to rewrite an untrusted one), a write the read
// side would still ignore is reported (VerifyBinding), a failed estate write is
// undone without leaving `{}` behind (RevertEstate), and a credential store is
// described three ways — present, absent, refused — by the loader's own checks
// (CredentialStore). Every tree lives in a t.TempDir(); values are literal
// non-secrets.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A settings file the read side ignores is NEVER rewritten — by any writer:
// the account pin, the estate, a pin clear. Each refuses with
// ErrUntrustedSettings naming the path and the reason, and the file keeps its
// bytes and mode and grows no .bak. A trusted file keeps its own mode across a
// write, so the writer can never produce a file the reader ignores.
func TestWriteSettings_RefusesAnUntrustedFile(t *testing.T) {
	const body = `{"swarmery":{"claudeAccount":"old","estate":"old"},"permissions":{"allow":["CONTENT_MARKER"]}}`
	cases := []struct {
		name   string
		mode   os.FileMode
		reason string
		fake   bool
	}{
		{"0664", 0o664, "group or other", false},
		{"0666", 0o666, "group or other", false},
		{"0620", 0o620, "group or other", false},
		{"foreign owner", 0o644, "not owned by you", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proj := t.TempDir()
			path := writeSettingsFile(t, proj, body)
			if err := os.Chmod(path, tc.mode); err != nil {
				t.Fatal(err)
			}
			if tc.fake {
				fakeUID(t)
			}
			for name, write := range map[string]func() error{
				"SetBinding":  func() error { return SetBinding(proj, "work") },
				"SetEstate":   func() error { return SetEstate(proj, "acme") },
				"clear a pin": func() error { return SetBinding(proj, "") },
			} {
				err := write()
				if !errors.Is(err, ErrUntrustedSettings) || !strings.Contains(err.Error(), path) ||
					!strings.Contains(err.Error(), tc.reason) || !strings.Contains(err.Error(), "chmod go-w") {
					t.Errorf("%s: err = %v, want ErrUntrustedSettings naming %s, %q and the fix", name, err, path, tc.reason)
				}
				if err != nil && strings.Contains(err.Error(), "CONTENT_MARKER") {
					t.Errorf("%s: the refusal leaked file contents: %v", name, err)
				}
			}
			if got := string(readFile(t, path)); got != body {
				t.Errorf("a refused write changed the file: %q", got)
			}
			if m := mustMode(t, path); m != tc.mode {
				t.Errorf("a refused write changed the mode %04o → %04o", tc.mode, m)
			}
			if _, err := os.Lstat(path + ".bak"); !os.IsNotExist(err) {
				t.Errorf("a refused write left a .bak (%v)", err)
			}
		})
	}

	// A trusted file keeps its mode and the write reads back.
	for _, mode := range []os.FileMode{0o640, 0o600, 0o644} {
		proj := t.TempDir()
		path := writeSettingsFile(t, proj, `{"permissions":{}}`)
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if err := SetBinding(proj, "work"); err != nil {
			t.Fatalf("%04o: SetBinding: %v", mode, err)
		}
		if m := mustMode(t, path); m != mode {
			t.Errorf("%04o: mode after the write = %04o, want it kept", mode, m)
		}
		if err := VerifyBinding(proj, "work"); err != nil {
			t.Errorf("%04o: VerifyBinding: %v", mode, err)
		}
	}
}

// A write with nothing to change never touches — so never refuses — an
// untrusted file: clearing a binding that is not there is a no-op, not an error.
func TestWriteSettings_NoOpOverAnUntrustedFileIsNotAnError(t *testing.T) {
	proj := t.TempDir()
	path := writeSettingsFile(t, proj, `{"permissions":{}}`)
	if err := os.Chmod(path, 0o664); err != nil {
		t.Fatal(err)
	}
	if err := SetBinding(proj, ""); err != nil {
		t.Fatalf("clearing an absent binding in an untrusted file: %v", err)
	}
	if err := SetEstate(proj, ""); err != nil {
		t.Fatalf("clearing an absent estate in an untrusted file: %v", err)
	}
}

// SetBinding writes nothing when the value is already stored — so an untrusted
// file that already holds it stays untrusted, and VerifyBinding must say so by
// path and reason, never by content.
func TestVerifyBinding_NamesAnIneffectiveWrite(t *testing.T) {
	proj := t.TempDir()
	path := writeSettingsFile(t, proj, `{"swarmery":{"claudeAccount":"work"},"permissions":{"allow":["CONTENT_MARKER"]}}`)
	if err := os.Chmod(path, 0o664); err != nil {
		t.Fatal(err)
	}
	if err := SetBinding(proj, "work"); err != nil {
		t.Fatal(err)
	}
	err := VerifyBinding(proj, "work")
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "group or other") {
		t.Fatalf("VerifyBinding over a 0664 file = %v, want an error naming %s and the mode reason", err, path)
	}
	if strings.Contains(err.Error(), "CONTENT_MARKER") {
		t.Errorf("VerifyBinding leaked file contents: %v", err)
	}
	// Foreign-owned: the same, with the owner reason.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	fakeUID(t)
	if err := VerifyBinding(proj, "work"); err == nil || !strings.Contains(err.Error(), "not owned by you") {
		t.Fatalf("VerifyBinding over a foreign-owned file = %v, want the owner reason", err)
	}
}

func TestVerifyBinding_ClearAndMismatch(t *testing.T) {
	proj := t.TempDir()
	if err := VerifyBinding(proj, ""); err != nil {
		t.Fatalf("a clear with no file: %v", err)
	}
	writeSettingsFile(t, proj, `{"swarmery":{"claudeAccount":"other"}}`)
	err := VerifyBinding(proj, "work")
	if err == nil || !strings.Contains(err.Error(), `reads back as "other"`) {
		t.Fatalf("a trusted file holding another key: %v", err)
	}
}

func TestBindingFileUntrustedAndExists(t *testing.T) {
	proj := t.TempDir()
	if BindingFileExists(proj) || BindingFileUntrusted(proj) != "" {
		t.Fatal("a missing file reported as existing or untrusted")
	}
	if BindingFileExists("  ") || BindingFileUntrusted("  ") != "" {
		t.Fatal("an empty path was inspected")
	}
	path := writeSettingsFile(t, proj, `{}`)
	if !BindingFileExists(proj) || BindingFileUntrusted(proj) != "" {
		t.Fatal("a trusted file misreported")
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if why := BindingFileUntrusted(proj); !strings.Contains(why, path) || !strings.Contains(why, "0666") {
		t.Fatalf("BindingFileUntrusted = %q, want the path and the mode", why)
	}
}

// RevertEstate: a file the failed write CREATED is removed — that file only,
// not the .claude dir — and a file that existed gets its previous value back.
func TestRevertEstate(t *testing.T) {
	fresh := t.TempDir()
	if err := SetEstate(fresh, "acme"); err != nil {
		t.Fatal(err)
	}
	if err := RevertEstate(fresh, "", false); err != nil {
		t.Fatalf("RevertEstate(created): %v", err)
	}
	if _, err := os.Lstat(bindingPath(fresh)); !os.IsNotExist(err) {
		t.Fatalf("the created file survived the revert (%v) — it would be left holding {}", err)
	}
	if fi, err := os.Stat(filepath.Join(fresh, ".claude")); err != nil || !fi.IsDir() {
		t.Fatalf("the revert removed more than the file: .claude is gone (%v)", err)
	}
	if err := RevertEstate(fresh, "", false); err != nil {
		t.Fatalf("reverting an already-absent file: %v", err)
	}

	had := t.TempDir()
	writeSettingsFile(t, had, `{"swarmery":{"estate":"old","claudeAccount":"work"}}`)
	if err := SetEstate(had, "new"); err != nil {
		t.Fatal(err)
	}
	if err := RevertEstate(had, "old", true); err != nil {
		t.Fatal(err)
	}
	if key, _ := Estate(had); key != "old" || Binding(had) != "work" {
		t.Fatalf("after revert: estate %q, binding %q — want old/work", key, Binding(had))
	}
	if err := RevertEstate(" ", "", false); err == nil {
		t.Fatal("RevertEstate accepted an empty directory")
	}
}

// CredentialStore distinguishes absent, present and refused using the loader's
// own checks; the reason never names the store's path or anything in it, and
// asking logs nothing.
func TestCredentialStore_ThreeStates(t *testing.T) {
	home := fakeHome(t)
	root := filepath.Join(home, "projects", "acme")
	declare(t, root, map[string]any{"estate": "acme"})

	check := func(t *testing.T, wantState StoreState, wantReason string) {
		t.Helper()
		var state StoreState
		var why string
		logged := captureLog(t, func() { state, why = Resolve(root).CredentialStore() })
		if state != wantState || !strings.Contains(why, wantReason) {
			t.Fatalf("CredentialStore = %v %q, want %v containing %q", state, why, wantState, wantReason)
		}
		if strings.Contains(why, SecretsDir()) || strings.Contains(why, "ACME_NAME") || strings.Contains(why, "acme-value") {
			t.Fatalf("reason %q names the store path or its content", why)
		}
		if logged != "" {
			t.Fatalf("CredentialStore logged %q — it is a query, not a load", logged)
		}
		if has := Resolve(root).HasCredentialStore(); has != (wantState == StorePresent) {
			t.Fatalf("HasCredentialStore = %v for state %v", has, wantState)
		}
	}

	t.Run("no estate", func(t *testing.T) {
		if s, why := Resolve(filepath.Join(home, "elsewhere")).CredentialStore(); s != StoreAbsent || why != "" {
			t.Fatalf("no estate: %v %q", s, why)
		}
	})
	t.Run("absent", func(t *testing.T) {
		seedStores(t, map[string]string{})
		check(t, StoreAbsent, "")
	})
	t.Run("present", func(t *testing.T) {
		seedStores(t, map[string]string{"acme": "ACME_NAME=acme-value\n"})
		check(t, StorePresent, "")
	})
	t.Run("file mode", func(t *testing.T) {
		dir := seedStores(t, map[string]string{"acme": "ACME_NAME=acme-value\n"})
		if err := os.Chmod(filepath.Join(dir, "acme.env"), 0o644); err != nil {
			t.Fatal(err)
		}
		check(t, StoreRefused, "mode 0644")
	})
	t.Run("dir mode", func(t *testing.T) {
		dir := seedStores(t, map[string]string{"acme": "ACME_NAME=acme-value\n"})
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		check(t, StoreRefused, "store directory's mode 0755")
	})
	t.Run("symlink", func(t *testing.T) {
		dir := seedStores(t, map[string]string{})
		target := filepath.Join(t.TempDir(), "real.env")
		if err := os.WriteFile(target, []byte("ACME_NAME=acme-value\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, "acme.env")); err != nil {
			t.Fatal(err)
		}
		check(t, StoreRefused, "symlink")
	})
	t.Run("dangling symlink", func(t *testing.T) {
		dir := seedStores(t, map[string]string{})
		if err := os.Symlink(filepath.Join(t.TempDir(), "gone.env"), filepath.Join(dir, "acme.env")); err != nil {
			t.Fatal(err)
		}
		check(t, StoreRefused, "symlink")
	})
	t.Run("directory", func(t *testing.T) {
		dir := seedStores(t, map[string]string{})
		mkdirs(t, filepath.Join(dir, "acme.env"))
		check(t, StoreRefused, "is a directory")
	})
	t.Run("foreign owner", func(t *testing.T) {
		seedStores(t, map[string]string{"acme": "ACME_NAME=acme-value\n"})
		r := Resolve(root) // resolved as the real owner, checked as a foreign one
		fakeUID(t)
		state, why := r.CredentialStore()
		if state != StoreRefused || !strings.Contains(why, "not owned by you") {
			t.Fatalf("foreign-owned store: %v %q", state, why)
		}
	})
}

// The loader still logs a refusal (by path and mode) — the shared checks did not
// silence it — and still loads nothing.
func TestSecretEnv_RefusalStillLogged(t *testing.T) {
	dir := seedStores(t, map[string]string{"work": "LOG_NAME=log-value\n"})
	if err := os.Chmod(filepath.Join(dir, "work.env"), 0o640); err != nil {
		t.Fatal(err)
	}
	var got []string
	logged := captureLog(t, func() { got = SecretEnvForStore("work") })
	if got != nil {
		t.Fatalf("a 0640 store yielded %d variables", len(got))
	}
	if !strings.Contains(logged, filepath.Join(dir, "work.env")) || !strings.Contains(logged, "0640") {
		t.Fatalf("log %q does not name the file and its mode", logged)
	}
	if strings.Contains(logged, "LOG_NAME") || strings.Contains(logged, "log-value") {
		t.Fatalf("log leaked store content: %q", logged)
	}
}
