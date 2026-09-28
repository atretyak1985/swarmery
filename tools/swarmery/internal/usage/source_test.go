package usage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSourceForAccount(t *testing.T) {
	if got := SourceForAccount("default", "/h/.claude", true); got.Account != "default" || got.ConfigDir != "" ||
		!got.IgnoreConfigDirEnv {
		t.Errorf("default account = %+v, want an EMPTY ConfigDir (legacy chain) that ignores CLAUDE_CONFIG_DIR", got)
	}
	if got := SourceForAccount("work", "/h/.claude-work", false); got.Account != "work" || got.ConfigDir != "/h/.claude-work" ||
		got.IgnoreConfigDirEnv {
		t.Errorf("named account = %+v", got)
	}
}

// An inherited CLAUDE_CONFIG_DIR naming ANOTHER account's dir must not attach
// that account's credential to the default Source; ~/.claude still resolves.
func TestDefaultSourceIgnoresInheritedConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(storeDirEnv, filepath.Join(t.TempDir(), "store"))
	other := filepath.Join(home, ".claude-other")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, credentialsFile),
		[]byte(`{"claudeAiOauth":{"accessToken":"other-account"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(configDirEnv, other)
	stubKeychain(t, func(context.Context, string) *Creds { return nil })

	src := SourceForAccount("default", filepath.Join(home, ".claude"), true)
	if _, err := LoadCredsFor(context.Background(), src); err != ErrNoCreds {
		t.Errorf("default source read the inherited CLAUDE_CONFIG_DIR credential (err = %v)", err)
	}
	for _, s := range CredentialSourcesFor(src) {
		if filepath.Dir(s) == other {
			t.Errorf("sources list the inherited dir: %v", CredentialSourcesFor(src))
		}
	}
	writeCredFile(t, filepath.Join(home, ".claude"))
	if c, err := LoadCredsFor(context.Background(), src); err != nil || c.AccessToken != fakeAccess {
		t.Errorf("default source did not read ~/.claude (err = %v)", err)
	}
	// The zero Source (the dashboard's) keeps the legacy behaviour.
	if c, err := LoadCredsFor(context.Background(), Source{Account: "default"}); err != nil || c.AccessToken != "other-account" {
		t.Errorf("legacy chain changed (err = %v)", err)
	}
}
