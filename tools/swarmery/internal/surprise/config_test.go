package surprise

import (
	"math"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDefaultConfig(t *testing.T) {
	c, warn := ConfigFromEnv(env(nil))
	if len(warn) != 0 {
		t.Errorf("warnings with no environment: %v", warn)
	}
	if c.NotifyAt == nil || *c.NotifyAt != DefaultNotifyAt || DefaultNotifyAt != 0.6 {
		t.Errorf("notify threshold = %v, want the documented default 0.6", c.NotifyAt)
	}
	// Auto-verify is OFF unless explicitly enabled.
	if c.AutoVerifyAt != nil {
		t.Errorf("auto-verify threshold = %v with nothing set, want off (nil)", *c.AutoVerifyAt)
	}
	sum := 0.0
	for _, name := range Components {
		w, ok := c.Weights[name]
		if !ok || w <= 0 {
			t.Errorf("component %s has no positive default weight", name)
		}
		sum += w
	}
	if math.Abs(sum-1) > 1e-9 {
		t.Errorf("default weights sum to %v, want 1", sum)
	}
	if !strings.Contains(c.String(), "autoverify=off") {
		t.Errorf("config string %q does not say auto-verify is off", c.String())
	}
}

func TestConfigFromEnv(t *testing.T) {
	c, warn := ConfigFromEnv(env(map[string]string{
		EnvWeights:      "outcome_miss=0.5, size_miss=0 ,bogus=1,test_surprise=-1,missed_areas=x",
		EnvNotify:       "0.4",
		EnvAutoVerifyAt: "0.75",
	}))
	if c.Weights[CompOutcomeMiss] != 0.5 || c.Weights[CompSizeMiss] != 0 {
		t.Errorf("weights not overridden: %v", c.Weights)
	}
	if c.Weights[CompTestSurprise] != DefaultWeights()[CompTestSurprise] ||
		c.Weights[CompMissedAreas] != DefaultWeights()[CompMissedAreas] {
		t.Errorf("an invalid override replaced a default: %v", c.Weights)
	}
	if len(warn) != 3 {
		t.Errorf("want 3 warnings (unknown, negative, unparseable), got %v", warn)
	}
	if c.NotifyAt == nil || *c.NotifyAt != 0.4 || c.AutoVerifyAt == nil || *c.AutoVerifyAt != 0.75 {
		t.Errorf("thresholds = %v / %v, want 0.4 / 0.75", c.NotifyAt, c.AutoVerifyAt)
	}

	c, warn = ConfigFromEnv(env(map[string]string{EnvNotify: "off", EnvAutoVerifyAt: "off"}))
	if c.NotifyAt != nil || c.AutoVerifyAt != nil || len(warn) != 0 {
		t.Errorf("off/off = %v / %v (warn %v), want both disabled", c.NotifyAt, c.AutoVerifyAt, warn)
	}

	c, warn = ConfigFromEnv(env(map[string]string{EnvNotify: "7", EnvAutoVerifyAt: "high"}))
	if c.NotifyAt == nil || *c.NotifyAt != DefaultNotifyAt || c.AutoVerifyAt != nil || len(warn) != 2 {
		t.Errorf("bad values = %v / %v (warn %v), want defaults kept with 2 warnings", c.NotifyAt, c.AutoVerifyAt, warn)
	}
}
