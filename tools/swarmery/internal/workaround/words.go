package workaround

import (
	"log"
	"sync"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/guards"
)

// defaultMatcher is guards.Load().WorkaroundWords compiled once per process —
// a changed SWARMERY_GUARDS applies on daemon restart (D8). A broken override is
// logged and leaves the scanner off, never silently back on the defaults the
// operator meant to replace.
var defaultMatcher = sync.OnceValue(func() *Matcher {
	c, err := guards.Load()
	if err != nil {
		log.Printf("error: workaround: %v — the workaround-words scanner is off until a restart with a valid file", err)
		return Compile(nil)
	}
	return Compile(c.WorkaroundWords)
})

// Default is the matcher the daemon scans with: the ingest tail, the link hook
// and the backfill subcommand all share it.
func Default() *Matcher { return defaultMatcher() }
