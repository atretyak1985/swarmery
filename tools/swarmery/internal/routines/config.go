package routines

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// TickInterval is the scheduler poll cadence. A routine with `* * * * *` fires
// within one tick of its slot (acceptance criterion).
const TickInterval = 60 * time.Second

// MaxConcurrent caps concurrent routine runs across all routines (Fusion's
// semaphore) so a burst can't starve the dispatcher's own lanes.
const MaxConcurrent = 2

// MaxRunHistory is how many routine_runs rows are kept per routine; older rows
// are pruned after each insert.
const MaxRunHistory = 50

// cronParser is the standard 5-field parser (minute hour dom month dow), the
// shape the phase doc mandates and the one the web validation mirrors.
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// Enabled reports the kill-switch state: SWARMERY_ROUTINES=0/false/off disables
// the scheduler entirely. Default (unset) is enabled. Mirrors dispatchEnabled /
// autoProvisionEnabled parsing exactly.
func Enabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("SWARMERY_ROUTINES")))
	return v != "0" && v != "false" && v != "off"
}

// ParseCron validates a cron expression and returns the parsed schedule. Blank
// input is an error here (callers that allow "no schedule" check for blank
// first). Exposed so the API can validate before persisting.
func ParseCron(expr string) (cron.Schedule, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, fmt.Errorf("empty cron expression")
	}
	sched, err := cronParser.Parse(expr)
	if err != nil {
		return nil, fmt.Errorf("invalid cron expression %q: %w", expr, err)
	}
	return sched, nil
}

// NextRun computes the next fire time strictly after `from` for a cron
// expression. Blank/invalid → ok=false (a routine with no schedule has no next
// run — manual/webhook only).
func NextRun(expr string, from time.Time) (time.Time, bool) {
	sched, err := ParseCron(expr)
	if err != nil {
		return time.Time{}, false
	}
	return sched.Next(from), true
}
