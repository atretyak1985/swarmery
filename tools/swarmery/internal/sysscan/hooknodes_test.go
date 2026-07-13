package sysscan

// Step-10 scanner coverage: the "_swarmery_disabled_hooks" service section
// (format doc §3.3.1) is recognized and its entries index as enabled=0 rows
// whose seq continues after the event's active entries.

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const disabledFixture = `{
  "_swarmery_disabled_hooks": [
    {
      "event": "PreToolUse",
      "groupIndex": 0,
      "hook": {
        "command": "lint.sh",
        "timeout": 5,
        "type": "command"
      },
      "hookIndex": 1,
      "matcher": "Bash"
    },
    {
      "event": "Notification",
      "groupIndex": 0,
      "hook": {
        "command": "notify.sh",
        "type": "command"
      },
      "hookIndex": 0
    }
  ],
  "hooks": {
    "PreToolUse": [
      {
        "hooks": [
          {
            "command": "audit.sh",
            "type": "command"
          }
        ],
        "matcher": "Bash"
      }
    ]
  }
}
`

func TestHooksDisabledSection(t *testing.T) {
	root := t.TempDir()
	claude := filepath.Join(root, "claude")
	mustWriteFile(t, filepath.Join(claude, "settings.json"), disabledFixture)

	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	s := New(db, Config{ClaudeDir: claude, RescanInterval: time.Hour}, nil)
	st, err := s.Scan()
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if st.Hooks != 3 {
		t.Errorf("Stats.Hooks = %d, want 3 (1 active + 2 disabled)", st.Hooks)
	}

	type row struct {
		event   string
		seq     int64
		enabled int64
		matcher sql.NullString
		timeout sql.NullInt64
	}
	get := func(cmd string) row {
		t.Helper()
		var r row
		if err := db.QueryRow(
			`SELECT event, seq, enabled, matcher, timeout FROM hooks WHERE command = ?`, cmd).
			Scan(&r.event, &r.seq, &r.enabled, &r.matcher, &r.timeout); err != nil {
			t.Fatalf("row %q: %v", cmd, err)
		}
		return r
	}

	if r := get("audit.sh"); r.event != "PreToolUse" || r.seq != 0 || r.enabled != 1 {
		t.Errorf("active row wrong: %+v", r)
	}
	// Disabled entry of an event WITH actives: seq continues after them.
	if r := get("lint.sh"); r.event != "PreToolUse" || r.seq != 1 || r.enabled != 0 ||
		!r.matcher.Valid || r.matcher.String != "Bash" || !r.timeout.Valid || r.timeout.Int64 != 5 {
		t.Errorf("disabled row wrong: %+v", r)
	}
	// Disabled entry of an event with NO actives: seq starts at 0, matcher NULL.
	if r := get("notify.sh"); r.event != "Notification" || r.seq != 0 || r.enabled != 0 || r.matcher.Valid {
		t.Errorf("disabled-only-event row wrong: %+v", r)
	}

	// Idempotent rescan: hashes are stable, no delete-and-insert id churn.
	var maxID int64
	if err := db.QueryRow(`SELECT MAX(id) FROM hooks`).Scan(&maxID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Scan(); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	var maxID2 int64
	if err := db.QueryRow(`SELECT MAX(id) FROM hooks`).Scan(&maxID2); err != nil {
		t.Fatal(err)
	}
	if maxID2 != maxID {
		t.Errorf("hook row ids churned on a no-op rescan: %d → %d", maxID, maxID2)
	}
}
