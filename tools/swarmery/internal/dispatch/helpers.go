package dispatch

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// newUUID returns a random RFC-4122 v4 UUID string. Uses crypto/rand directly
// (the codebase's convention for random ids — see api/tasks_board.go,
// approvals/approvals.go) rather than promoting the indirect google/uuid dep.
// This is the --session-id passed to the headless run (the explicit link).
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand.Read never fails on supported platforms; if it somehow
		// does, a time-free deterministic fallback is worse than panicking a
		// single dispatch goroutine (which recovers) — but we degrade to a
		// zero-filled variant-tagged uuid rather than crash.
		b = [16]byte{}
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// decodeStringList parses a stored JSON string array (file_scope, dependencies)
// into []string. Empty/NULL storage ⇒ empty slice, never nil-panic downstream.
// Mirrors api.unmarshalStringList (kept local so dispatch has no api import).
func decodeStringList(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return []string{}, nil
	}
	var xs []string
	if err := json.Unmarshal([]byte(s), &xs); err != nil {
		return nil, err
	}
	if xs == nil {
		xs = []string{}
	}
	return xs, nil
}

// boolToInt maps a bool to SQLite's 0/1 integer boolean.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// itoa / itoa64 are strconv shortcuts for building error strings + scope keys.
func itoa(n int) string     { return strconv.Itoa(n) }
func itoa64(n int64) string { return strconv.FormatInt(n, 10) }
