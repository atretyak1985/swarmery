package dispatch

import (
	"testing"
	"time"
)

func TestConfigFromEnvDefaults(t *testing.T) {
	// No env set → conservative defaults, enabled.
	for _, k := range []string{"SWARMERY_DISPATCH", "SWARMERY_MAX_CONCURRENT", "SWARMERY_MAX_WORKTREES", "SWARMERY_DISPATCH_TIMEOUT_MIN"} {
		t.Setenv(k, "")
	}
	c := ConfigFromEnv()
	if c.MaxConcurrent != DefaultMaxConcurrent || c.MaxWorktrees != DefaultMaxWorktrees {
		t.Errorf("defaults: concurrent=%d worktrees=%d", c.MaxConcurrent, c.MaxWorktrees)
	}
	if c.RunTimeout != DefaultRunTimeout || c.PollInterval != DefaultPollInterval {
		t.Errorf("defaults: timeout=%s poll=%s", c.RunTimeout, c.PollInterval)
	}
	if !c.Enabled {
		t.Error("dispatcher should default to enabled")
	}
}

func TestConfigFromEnvOverrides(t *testing.T) {
	t.Setenv("SWARMERY_MAX_CONCURRENT", "5")
	t.Setenv("SWARMERY_MAX_WORKTREES", "9")
	t.Setenv("SWARMERY_DISPATCH_TIMEOUT_MIN", "10")
	c := ConfigFromEnv()
	if c.MaxConcurrent != 5 || c.MaxWorktrees != 9 {
		t.Errorf("override: concurrent=%d worktrees=%d", c.MaxConcurrent, c.MaxWorktrees)
	}
	if c.RunTimeout != 10*time.Minute {
		t.Errorf("override timeout = %s, want 10m", c.RunTimeout)
	}
}

func TestConfigFromEnvKillSwitch(t *testing.T) {
	for _, v := range []string{"0", "false", "off", "OFF", "False"} {
		t.Setenv("SWARMERY_DISPATCH", v)
		if ConfigFromEnv().Enabled {
			t.Errorf("SWARMERY_DISPATCH=%q should disable the dispatcher", v)
		}
	}
	for _, v := range []string{"1", "true", "on", ""} {
		t.Setenv("SWARMERY_DISPATCH", v)
		if !ConfigFromEnv().Enabled {
			t.Errorf("SWARMERY_DISPATCH=%q should keep the dispatcher enabled", v)
		}
	}
}

func TestConfigFromEnvIgnoresJunk(t *testing.T) {
	t.Setenv("SWARMERY_MAX_CONCURRENT", "-3")
	t.Setenv("SWARMERY_MAX_WORKTREES", "notanumber")
	c := ConfigFromEnv()
	if c.MaxConcurrent != DefaultMaxConcurrent || c.MaxWorktrees != DefaultMaxWorktrees {
		t.Errorf("junk env should fall back to defaults; got concurrent=%d worktrees=%d", c.MaxConcurrent, c.MaxWorktrees)
	}
}
