package verify

import (
	"crypto/rand"
	"fmt"
)

// newUUID returns a random RFC-4122 v4 UUID string — the --session-id passed to
// the headless verifier run (the explicit link). Uses crypto/rand directly (the
// codebase's convention — see dispatch.newUUID, api/tasks_board.go) rather than
// promoting the indirect google/uuid dep.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand.Read never fails on supported platforms; degrade to a
		// zero-filled variant-tagged uuid rather than crash a single goroutine.
		b = [16]byte{}
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
