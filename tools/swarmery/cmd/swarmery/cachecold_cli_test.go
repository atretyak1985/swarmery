package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

func TestCacheColdBackfillIsIdempotent(t *testing.T) {
	// cacheColdConfig resolves billing kinds from $HOME — keep it off the real one.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("SWARMERY_GUARDS", "")

	path := filepath.Join(t.TempDir(), "cc.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO projects (id, path, slug, name, first_seen) VALUES (1, '/w', '-w', 'w', '2026-10-01T00:00:00Z')`,
		`INSERT INTO sessions (id, project_id, session_uuid, started_at, status) VALUES (1, 1, 's', '2026-10-20T08:00:00Z', 'completed')`,
		// 200k context, then a re-write of 170k after a 2 h pause: 0.85 of it.
		`INSERT INTO turns (session_id, seq, role, started_at, ended_at, tokens_in, tokens_cache_read, tokens_cache_write, cost_usd)
		 VALUES (1, 0, 'assistant', '2026-10-20T09:00:00Z', '2026-10-20T09:00:00Z', 10, 190000, 9990, 0.2)`,
		`INSERT INTO turns (session_id, seq, role, started_at, ended_at, tokens_in, tokens_cache_read, tokens_cache_write, cost_usd)
		 VALUES (1, 1, 'assistant', '2026-10-20T11:00:00Z', '2026-10-20T11:00:00Z', 10, 0, 170000, 1.8)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	run := func() string {
		var out bytes.Buffer
		if err := cmdCacheColdBackfill([]string{"--db", path, "--since", "3650d"}, &out); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	first := run()
	for _, want := range []string{"evaluated: 2", "cold restarts (factor 0.80): 1  ($1.80)", "rows changed: 1", "0.9      0 marks"} {
		if !strings.Contains(first, want) {
			t.Errorf("first run lacks %q:\n%s", want, first)
		}
	}
	if second := run(); !strings.Contains(second, "rows changed: 0") || !strings.Contains(second, "factor 0.80): 1") {
		t.Errorf("second run is not a no-op:\n%s", second)
	}
}

func TestParseWindow(t *testing.T) {
	for in, want := range map[string]time.Duration{"30d": 30 * 24 * time.Hour, "1d": 24 * time.Hour, "36h": 36 * time.Hour} {
		if got, err := parseWindow(in); err != nil || got != want {
			t.Errorf("parseWindow(%q) = %s, %v; want %s", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "0d", "-3d", "xd", "soon", "-1h"} {
		if _, err := parseWindow(bad); err == nil {
			t.Errorf("parseWindow(%q) accepted", bad)
		}
	}
}
