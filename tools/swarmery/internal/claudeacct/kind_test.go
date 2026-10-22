package claudeacct

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// kindHome points the home seam and the env seam at a fresh fake home.
func kindHome(t *testing.T, env map[string]string) string {
	t.Helper()
	home := t.TempDir()
	prevHome, prevEnv := userHomeDir, lookupEnv
	userHomeDir = func() (string, error) { return home, nil }
	lookupEnv = func(k string) string { return env[k] }
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Cleanup(func() { userHomeDir, lookupEnv = prevHome, prevEnv })
	return home
}

func TestBillingKind(t *testing.T) {
	home := kindHome(t, nil)

	// The default account on an OAuth login: subscription.
	writeFile(t, filepath.Join(home, ".claude.json"), `{"oauthAccount":{"emailAddress":"x"},"projects":{}}`)
	if got := BillingKind(""); got != BillingSubscription {
		t.Errorf(`BillingKind("") = %s, want subscription`, got)
	}

	// A Console login stores the key in the account's profile.
	writeFile(t, filepath.Join(home, ".claude-console", ".claude.json"), `{"primaryApiKey":"sk-test"}`)
	if got := BillingKind("console"); got != BillingAPI {
		t.Errorf("BillingKind(console) = %s, want api", got)
	}

	// An env key in the account's settings.json.
	writeFile(t, filepath.Join(home, ".claude-envkey", "settings.json"), `{"env":{"ANTHROPIC_API_KEY":"sk-test"}}`)
	if got := BillingKind("envkey"); got != BillingAPI {
		t.Errorf("BillingKind(envkey) = %s, want api", got)
	}

	// An apiKeyHelper.
	writeFile(t, filepath.Join(home, ".claude-helper", "settings.json"), `{"apiKeyHelper":"/bin/echo key"}`)
	if got := BillingKind("helper"); got != BillingAPI {
		t.Errorf("BillingKind(helper) = %s, want api", got)
	}

	// A settings.json without a key, an empty key, a missing dir, a bad key.
	writeFile(t, filepath.Join(home, ".claude-plain", "settings.json"), `{"env":{"ANTHROPIC_API_KEY":"  "},"model":"opus"}`)
	for _, key := range []string{"plain", "missing", "../escape"} {
		if got := BillingKind(key); got != BillingSubscription {
			t.Errorf("BillingKind(%q) = %s, want subscription", key, got)
		}
	}
}

func TestBillingKindDaemonEnvKeyAppliesToInheritedAccountOnly(t *testing.T) {
	kindHome(t, map[string]string{"ANTHROPIC_API_KEY": "sk-test"})
	if got := BillingKind("default"); got != BillingAPI {
		t.Errorf("default account with ANTHROPIC_API_KEY in the daemon env = %s, want api", got)
	}
	if got := BillingKind("other"); got != BillingSubscription {
		t.Errorf("another account must not inherit the daemon env key: %s", got)
	}
}

func TestBillingKindReparsesChangedProfile(t *testing.T) {
	home := kindHome(t, nil)
	profile := filepath.Join(home, ".claude-flip", ".claude.json")
	writeFile(t, profile, `{"oauthAccount":{}}`)
	if got := BillingKind("flip"); got != BillingSubscription {
		t.Fatalf("before = %s", got)
	}
	writeFile(t, profile, `{"primaryApiKey":"sk-test-longer"}`)
	// Size differs already; bump mtime too so the cache cannot match.
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(profile, later, later); err != nil {
		t.Fatal(err)
	}
	if got := BillingKind("flip"); got != BillingAPI {
		t.Errorf("after the profile changed = %s, want api", got)
	}
}
