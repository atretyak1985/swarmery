package cachecold

import (
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/guards"
)

var t0 = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

func testConfig() Config {
	return Config{ColdMinutes: guards.CacheCold{Subscription: 60, API: 5}, MinFootprint: 150_000}
}

// prevTurn is a fat turn that ended at t0 with a 200k footprint.
func prevTurn() Turn {
	return Turn{StartedAt: t0.Add(-time.Minute), EndedAt: t0, TokensIn: 10, CacheRead: 190_000, CacheWrite: 9_990}
}

func TestIsColdRestart(t *testing.T) {
	sub := testConfig().Rule(KindSubscription)
	api := testConfig().Rule(KindAPI)
	cases := []struct {
		name string
		cur  Turn
		r    Rule
		want bool
	}{
		{
			// The prompt's case: after the pause, cache_write ≥ 0.8 × footprint and
			// cache_read 0.
			name: "cold restart after a 70 min pause",
			cur:  Turn{StartedAt: t0.Add(70 * time.Minute), TokensIn: 5, CacheWrite1h: 201_000, Split: true, CacheWrite: 201_000},
			r:    sub, want: true,
		},
		{
			// A warm continuation reads the whole context and writes only the delta.
			name: "warm continuation",
			cur:  Turn{StartedAt: t0.Add(2 * time.Minute), TokensIn: 5, CacheRead: 200_000, CacheWrite: 1_500},
			r:    sub, want: false,
		},
		{
			// A warm read after a long pause is impossible on a real cache, but the
			// read bound must still refuse it.
			name: "full read after a pause is not cold",
			cur:  Turn{StartedAt: t0.Add(90 * time.Minute), TokensIn: 5, CacheRead: 200_000, CacheWrite: 1_500},
			r:    sub, want: false,
		},
		{
			// The measured shape: the shared system-prompt prefix is still served.
			name: "shared prefix read, remainder re-written",
			cur:  Turn{StartedAt: t0.Add(2 * time.Hour), TokensIn: 5, CacheRead: 28_000, CacheWrite1h: 175_000, Split: true, CacheWrite: 175_000},
			r:    sub, want: true,
		},
		{
			name: "pause under the subscription TTL",
			cur:  Turn{StartedAt: t0.Add(30 * time.Minute), TokensIn: 5, CacheWrite: 201_000},
			r:    sub, want: false,
		},
		{
			name: "api TTL is 5 min",
			cur:  Turn{StartedAt: t0.Add(6 * time.Minute), TokensIn: 5, CacheWrite5m: 201_000, Split: true, CacheWrite: 201_000},
			r:    api, want: true,
		},
		{
			// Compaction after a pause writes a much smaller context — not a
			// re-write of the old one.
			name: "compaction after a pause",
			cur:  Turn{StartedAt: t0.Add(2 * time.Hour), TokensIn: 5, CacheRead: 20_000, CacheWrite: 12_000},
			r:    sub, want: false,
		},
		{
			name: "factor 0.9 refuses a 0.85 re-write",
			cur:  Turn{StartedAt: t0.Add(2 * time.Hour), CacheWrite: 170_000},
			r:    Rule{TTL: time.Hour, Factor: 0.9}, want: false,
		},
		{
			// The split, when known, is what counts — not the flat total.
			name: "split known: 5m+1h, not the flat total",
			cur:  Turn{StartedAt: t0.Add(2 * time.Hour), CacheWrite: 201_000, CacheWrite5m: 1_000, CacheWrite1h: 1_000, Split: true},
			r:    sub, want: false,
		},
		{
			name: "no start timestamp",
			cur:  Turn{CacheWrite: 201_000},
			r:    sub, want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsColdRestart(prevTurn(), c.cur, c.r); got != c.want {
				t.Errorf("IsColdRestart = %v, want %v", got, c.want)
			}
		})
	}
}

func TestIsColdRestartSmallContextNeverMarked(t *testing.T) {
	prev := Turn{EndedAt: t0, CacheRead: 30_000, CacheWrite: 1_000}
	cur := Turn{StartedAt: t0.Add(3 * time.Hour), CacheWrite: 31_000}
	if IsColdRestart(prev, cur, testConfig().Rule(KindSubscription)) {
		t.Error("a context within the shared prefix was marked")
	}
}

func TestConfigKindAndTTL(t *testing.T) {
	c := testConfig()
	if c.Kind("anything") != KindSubscription {
		t.Error("nil KindOf must read every account as a subscription")
	}
	c.KindOf = func(a string) Kind {
		if a == "console" {
			return KindAPI
		}
		return "weird"
	}
	if c.Kind("console") != KindAPI || c.Kind("default") != KindSubscription {
		t.Errorf("Kind: console=%s default=%s", c.Kind("console"), c.Kind("default"))
	}
	if c.TTL(KindAPI) != 5*time.Minute || c.TTL(KindSubscription) != time.Hour {
		t.Errorf("TTL: api=%s sub=%s", c.TTL(KindAPI), c.TTL(KindSubscription))
	}
	if r := c.Rule(KindAPI); r.Factor != Factor || r.TTL != 5*time.Minute {
		t.Errorf("Rule(api) = %+v", r)
	}
}

func TestDefaultConfigUsesShippedGuards(t *testing.T) {
	c, err := DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	g, err := guards.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	if c.ColdMinutes != g.CacheColdMinutes {
		t.Errorf("DefaultConfig minutes %+v, guards %+v", c.ColdMinutes, g.CacheColdMinutes)
	}
}

func TestFootprintAndWritten(t *testing.T) {
	tr := Turn{TokensIn: 1, CacheRead: 2, CacheWrite: 3}
	if Footprint(tr) != 6 {
		t.Errorf("Footprint = %d", Footprint(tr))
	}
	if tr.Written() != 3 {
		t.Errorf("Written (no split) = %d", tr.Written())
	}
	tr.Split, tr.CacheWrite5m, tr.CacheWrite1h = true, 1, 1
	if tr.Written() != 2 {
		t.Errorf("Written (split) = %d", tr.Written())
	}
}
