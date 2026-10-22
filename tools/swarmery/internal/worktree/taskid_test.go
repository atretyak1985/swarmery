package worktree

import (
	"errors"
	"strings"
	"testing"
)

// badTaskIDs are ids that must never become a path component under the worktree
// root: each either names a directory other than its own leaf (an empty id, ".",
// "..", a separator, a NUL) or is long enough to be nobody's generated id.
var badTaskIDs = map[string]string{
	"parent traversal": "../x",
	"bare parent":      "..",
	"bare dot":         ".",
	"separator":        "a/b",
	"empty":            "",
	"over-long":        strings.Repeat("a", 200),
	"nul byte":         "a\x00b",
	"backslash":        `a\b`,
}

// TestTaskIDValidationRejectsUnsafeIDs: Path and every Acquire entry point refuse
// an unsafe task id before touching git or the filesystem.
func TestTaskIDValidationRejectsUnsafeIDs(t *testing.T) {
	for name, id := range badTaskIDs {
		t.Run(name, func(t *testing.T) {
			g := baseStub()
			m := newMgr(t, g)
			if _, err := m.Path("proj", id); !errors.Is(err, ErrInvalidTaskID) {
				t.Errorf("Path(%q) = %v, want ErrInvalidTaskID", id, err)
			}
			if _, err := m.Acquire("/tmp/repo", "proj", id); !errors.Is(err, ErrInvalidTaskID) {
				t.Errorf("Acquire(%q) = %v, want ErrInvalidTaskID", id, err)
			}
			if _, err := m.AcquireAt("/tmp/repo", "proj", id, "main"); !errors.Is(err, ErrInvalidTaskID) {
				t.Errorf("AcquireAt(%q) = %v, want ErrInvalidTaskID", id, err)
			}
			if _, err := m.AcquireExisting("/tmp/repo", "proj", id); !errors.Is(err, ErrInvalidTaskID) {
				t.Errorf("AcquireExisting(%q) = %v, want ErrInvalidTaskID", id, err)
			}
			if len(g.calls) != 0 {
				t.Errorf("git was invoked for an unsafe id: %v", g.calls)
			}
		})
	}
}

// TestTaskIDValidationAcceptsGeneratedIDs: every shape the daemon's own id
// generators produce (board cards, phase runs, plan runs) still passes.
func TestTaskIDValidationAcceptsGeneratedIDs(t *testing.T) {
	for _, id := range []string{"T-abc123", "T-0z9y8x", "phase-714", "plan-430", "a.b_c-1", strings.Repeat("a", 128)} {
		m := newMgr(t, baseStub())
		if _, err := m.Path("proj", id); err != nil {
			t.Errorf("Path(%q) = %v, want accepted", id, err)
		}
	}
}
