package claudeacct

// Tests for the per-account secret store.
//
// Every test points SWARMERY_SECRETS_DIR at a t.TempDir(): none of them may read
// the operator's real ~/.swarmery/secrets, and none of them writes a real
// credential — the values below are literal non-secrets.

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// seedStore writes one account's store file with the given mode and returns its
// path.
func seedStore(t *testing.T, account, body string, mode os.FileMode) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(secretsDirEnv, dir)
	path := filepath.Join(dir, account+".env")
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	// WriteFile's mode is masked by the umask, so state it outright — the mode
	// IS the subject of half these tests.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod store: %v", err)
	}
	return path
}

// captureLog collects what the package logs during fn.
func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(prevOut); log.SetFlags(prevFlags) })
	fn()
	return buf.String()
}

// THE mode-refusal test. A store file any other local user can read must yield
// NO value at all — not a warning-plus-the-value, which is how this kind of
// check usually rots. The log must name the path and the mode so the operator
// can fix it, and must contain neither the variable name nor its value.
func TestSecretEnvForAccount_RefusesGroupOrOtherReadableStore(t *testing.T) {
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604, 0o660, 0o666, 0o601} {
		t.Run(mode.String(), func(t *testing.T) {
			path := seedStore(t, "work", "SECRET_NAME=not-a-secret\n", mode)

			var got []string
			logged := captureLog(t, func() { got = SecretEnvForAccount("work") })

			if got != nil {
				t.Fatalf("mode %04o yielded %d variables, want NONE — a store readable "+
					"beyond its owner must be refused outright", mode.Perm(), len(got))
			}
			if !strings.Contains(logged, path) {
				t.Errorf("refusal log %q does not name the path; the operator cannot fix what is not named", logged)
			}
			if !strings.Contains(logged, mode.Perm().String()) && !strings.Contains(logged, "mode") {
				t.Errorf("refusal log %q does not report the mode", logged)
			}
			if strings.Contains(logged, "SECRET_NAME") || strings.Contains(logged, "not-a-secret") {
				t.Errorf("refusal log leaked store content: %q", logged)
			}
		})
	}
}

// The mirror of the above: 0600 is accepted.
func TestSecretEnvForAccount_AcceptsOwnerOnlyStore(t *testing.T) {
	seedStore(t, "work", "SECRET_NAME=not-a-secret\n", secretsFileMode)
	if got := SecretEnvForAccount("work"); !reflect.DeepEqual(got, []string{"SECRET_NAME=not-a-secret"}) {
		t.Fatalf("SecretEnvForAccount = %v, want the single pair", got)
	}
}

// nil, not an error, for every "there is nothing here" shape. This is the
// normal case for almost every project on a machine.
func TestSecretEnvForAccount_NilWhenThereIsNothingToLoad(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(secretsDirEnv, dir)

	cases := []struct{ name, key string }{
		{"missing file", "work"},
		{"default account", "default"},
		{"empty key", ""},
		{"whitespace key", "   "},
		{"key with a separator", "a/b"},
		{"key with a parent ref", ".."},
		{"dotfile key", ".hidden"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SecretEnvForAccount(tc.key); got != nil {
				t.Fatalf("SecretEnvForAccount(%q) = %v, want nil", tc.key, got)
			}
		})
	}
}

// An unsafe key must not even be turned into a path — the escape is the point,
// not the missing file.
func TestSecretsPath_RefusesUnsafeKeys(t *testing.T) {
	t.Setenv(secretsDirEnv, t.TempDir())
	for _, key := range []string{"", "default", ".", "..", "../other", "a/b", `a\b`, ".hidden", "a b"} {
		if got := SecretsPath(key); got != "" {
			t.Errorf("SecretsPath(%q) = %q, want \"\"", key, got)
		}
	}
	if got := SecretsPath("work"); got == "" {
		t.Error(`SecretsPath("work") = "", want a path`)
	}
}

// The parser's whole surface, in the shapes a real .env file arrives in.
func TestParseSecretEnv(t *testing.T) {
	body := strings.Join([]string{
		"# a comment",
		"",
		"   ",
		"PLAIN=value",
		"export EXPORTED=value",
		`DQUOTED="value with spaces"`,
		"SQUOTED='value'",
		`UNMATCHED="value`,
		"EMPTY=",
		"WITH_EQUALS=a=b=c",
		"  SPACED  =  value  ",
		"#COMMENTED=value",
		"nokeyvalue",
		"1BAD=value",
		"BAD NAME=value",
		"=novalue",
	}, "\n")

	var got []string
	logged := captureLog(t, func() { got = parseSecretEnv("/store.env", strings.NewReader(body)) })

	want := []string{
		"PLAIN=value",
		"EXPORTED=value",
		"DQUOTED=value with spaces",
		"SQUOTED=value",
		`UNMATCHED="value`,
		"EMPTY=",
		"WITH_EQUALS=a=b=c",
		"SPACED=value",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseSecretEnv =\n%q\nwant\n%q", got, want)
	}
	// Four malformed lines, reported by NUMBER and nothing else.
	for _, n := range []string{"line 13", "line 14", "line 15", "line 16"} {
		if !strings.Contains(logged, n) {
			t.Errorf("malformed-line log %q does not mention %q", logged, n)
		}
	}
	if strings.Contains(logged, "nokeyvalue") || strings.Contains(logged, "1BAD") ||
		strings.Contains(logged, "BAD NAME") || strings.Contains(logged, "value") {
		t.Errorf("malformed-line log leaked the line's content: %q", logged)
	}
}

// SecretEnvFor resolves the project's binding — the terminal seam's entry point.
func TestSecretEnvFor_ResolvesTheProjectBinding(t *testing.T) {
	seedStore(t, "work", "SECRET_NAME=not-a-secret\n", secretsFileMode)

	bound := t.TempDir()
	if err := SetBinding(bound, "work"); err != nil {
		t.Fatalf("SetBinding: %v", err)
	}
	if got := SecretEnvFor(bound); !reflect.DeepEqual(got, []string{"SECRET_NAME=not-a-secret"}) {
		t.Fatalf("SecretEnvFor(bound) = %v, want the store's pair", got)
	}
	// An UNBOUND project gets nothing even though a store exists on the machine.
	// This is the scoping property: secrets follow the binding, not the box.
	if got := SecretEnvFor(t.TempDir()); got != nil {
		t.Fatalf("SecretEnvFor(unbound) = %v, want nil — the store is per ACCOUNT, not machine-wide", got)
	}
}

// A store directory in place of a store file is ignored, loudly enough to see.
func TestSecretEnvForAccount_IgnoresADirectory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(secretsDirEnv, dir)
	if err := os.Mkdir(filepath.Join(dir, "work.env"), secretsDirMode); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	var got []string
	logged := captureLog(t, func() { got = SecretEnvForAccount("work") })
	if got != nil {
		t.Fatalf("SecretEnvForAccount = %v, want nil", got)
	}
	if !strings.Contains(logged, "directory") {
		t.Errorf("log = %q, want it to say the store is a directory", logged)
	}
}

// SecretsDir falls back to ~/.swarmery/secrets — a sibling of the credential
// store, not a new convention.
func TestSecretsDir_DefaultsUnderTheHomeDir(t *testing.T) {
	t.Setenv(secretsDirEnv, "")
	home := fakeHome(t)
	if got, want := SecretsDir(), filepath.Join(home, ".swarmery", "secrets"); got != want {
		t.Fatalf("SecretsDir() = %q, want %q", got, want)
	}
}
