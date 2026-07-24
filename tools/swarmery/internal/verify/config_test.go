package verify

import (
	"testing"
	"time"
)

func TestConfigFromEnv_Defaults(t *testing.T) {
	// No env set → conservative defaults.
	t.Setenv("SWARMERY_AUTOVERIFY", "")
	t.Setenv("SWARMERY_VERIFY_CONCURRENCY", "")
	t.Setenv("SWARMERY_VERIFY_TIMEOUT_MIN", "")
	c := ConfigFromEnv()
	if !c.Enabled {
		t.Error("default should be enabled")
	}
	if c.Concurrency != DefaultConcurrency {
		t.Errorf("concurrency = %d, want %d", c.Concurrency, DefaultConcurrency)
	}
	if c.RunTimeout != DefaultRunTimeout {
		t.Errorf("timeout = %s, want %s", c.RunTimeout, DefaultRunTimeout)
	}
	if c.RetryBudget != DefaultRetryBudget {
		t.Errorf("budget = %d, want %d", c.RetryBudget, DefaultRetryBudget)
	}
}

func TestConfigFromEnv_KillSwitch(t *testing.T) {
	for _, v := range []string{"0", "false", "off", "OFF", "False"} {
		t.Setenv("SWARMERY_AUTOVERIFY", v)
		if ConfigFromEnv().Enabled {
			t.Errorf("SWARMERY_AUTOVERIFY=%q should disable", v)
		}
	}
	for _, v := range []string{"1", "true", "on", ""} {
		t.Setenv("SWARMERY_AUTOVERIFY", v)
		if !ConfigFromEnv().Enabled {
			t.Errorf("SWARMERY_AUTOVERIFY=%q should stay enabled", v)
		}
	}
}

func TestConfigFromEnv_Overrides(t *testing.T) {
	t.Setenv("SWARMERY_VERIFY_CONCURRENCY", "3")
	t.Setenv("SWARMERY_VERIFY_TIMEOUT_MIN", "5")
	c := ConfigFromEnv()
	if c.Concurrency != 3 {
		t.Errorf("concurrency = %d, want 3", c.Concurrency)
	}
	if c.RunTimeout != 5*time.Minute {
		t.Errorf("timeout = %s, want 5m", c.RunTimeout)
	}
}

func TestConfigFromEnv_InvalidIgnored(t *testing.T) {
	t.Setenv("SWARMERY_VERIFY_CONCURRENCY", "-2")
	t.Setenv("SWARMERY_VERIFY_TIMEOUT_MIN", "notanumber")
	c := ConfigFromEnv()
	if c.Concurrency != DefaultConcurrency {
		t.Errorf("invalid concurrency should fall back to default, got %d", c.Concurrency)
	}
	if c.RunTimeout != DefaultRunTimeout {
		t.Errorf("invalid timeout should fall back to default, got %s", c.RunTimeout)
	}
}

func TestNewService_DefaultsClamp(t *testing.T) {
	// Zero/negative config values are clamped to safe defaults.
	db := testDB(t)
	s := NewService(db, Config{Concurrency: 0, RetryBudget: 0, StaleAfter: 0}, &stubRunner{}, stubTrees{})
	if cap(s.sem) != DefaultConcurrency {
		t.Errorf("sem cap = %d, want %d", cap(s.sem), DefaultConcurrency)
	}
	if s.Cfg.RetryBudget != DefaultRetryBudget {
		t.Errorf("budget = %d, want %d", s.Cfg.RetryBudget, DefaultRetryBudget)
	}
	if s.Cfg.StaleAfter != DefaultStaleAfter {
		t.Errorf("staleAfter = %s, want %s", s.Cfg.StaleAfter, DefaultStaleAfter)
	}
}

func TestNewUUID_ShapeAndUniqueness(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		u := newUUID()
		if len(u) != 36 {
			t.Fatalf("uuid %q len = %d, want 36", u, len(u))
		}
		if u[14] != '4' {
			t.Fatalf("uuid %q not version 4 (char 14 = %c)", u, u[14])
		}
		if seen[u] {
			t.Fatalf("duplicate uuid %q", u)
		}
		seen[u] = true
	}
}
