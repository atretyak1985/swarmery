package dispatch

import (
	"database/sql"
	"log"
	"os"
	"path"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeflags"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/route"
)

// routeModeEnv is this surface's complexity-router knob: off | shadow | active,
// default shadow (route.ModeFromEnv).
const routeModeEnv = "SWARMERY_ROUTE_DISPATCH"

// routeHistoryWindow bounds the failure-history signal to the project's most
// recent verified cards: old enough history describes a codebase that no
// longer exists.
const routeHistoryWindow = 30

// riskRule is one row of the risky-path table: a scope entry matching it earns
// the RiskPaths signal. One table, so adding a class of risky file is one line.
type riskRule struct {
	name  string
	match func(lower, base string) bool
}

// riskRules: schema changes, auth code, API contracts and dependency manifests —
// the files where a small-looking change has a large blast radius.
var riskRules = []riskRule{
	{"migrations", func(lower, _ string) bool {
		return strings.HasPrefix(lower, "migrations/") || strings.Contains(lower, "/migrations/")
	}},
	{"sql", func(lower, _ string) bool { return strings.HasSuffix(lower, ".sql") }},
	{"auth", func(lower, _ string) bool { return strings.Contains(lower, "auth") }},
	{"api-contract", func(lower, _ string) bool {
		return strings.Contains(lower, "openapi") || strings.HasSuffix(lower, ".proto")
	}},
	{"manifest", func(_, base string) bool {
		return base == "go.mod" || base == "package.json" || base == "package-lock.json"
	}},
}

// riskyScope returns the scope entries that match any risk rule, in scope
// order, each at most once.
func riskyScope(scope []string) []string {
	var out []string
	for _, e := range scope {
		entry := strings.TrimSpace(e)
		if entry == "" {
			continue
		}
		lower := strings.ToLower(entry)
		base := path.Base(strings.TrimSuffix(lower, "/"))
		for _, r := range riskRules {
			if r.match(lower, base) {
				out = append(out, entry)
				break
			}
		}
	}
	return out
}

// scopeAreas counts the distinct directories a scope touches, truncated to
// their first two segments ("tools/swarmery/internal/x.go" → "tools/swarmery").
// A trailing slash names the directory itself; a root-level file is ".".
func scopeAreas(scope []string) int {
	seen := map[string]bool{}
	for _, e := range scope {
		entry := strings.TrimSpace(e)
		if entry == "" {
			continue
		}
		var dir string
		if strings.HasSuffix(entry, "/") {
			dir = path.Clean(entry)
		} else {
			dir = path.Dir(path.Clean(entry))
		}
		dir = strings.TrimPrefix(dir, "./")
		if segs := strings.Split(dir, "/"); len(segs) > 2 {
			dir = strings.Join(segs[:2], "/")
		}
		seen[dir] = true
	}
	return len(seen)
}

// nonBlank counts the non-empty entries of a declared list.
func nonBlank(xs []string) int {
	n := 0
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			n++
		}
	}
	return n
}

// signalsFor extracts the router's signals for a card. An empty file scope is
// UNKNOWN (-1), not zero: a card that declared nothing may touch anything. A
// history read failure is logged and reads as zero samples, which the router
// ignores — a missing measurement never becomes a bad one.
func signalsFor(c candidate, db *sql.DB) route.Signals {
	s := route.Signals{
		Surface:     route.SurfaceDispatch,
		PromptBytes: len(c.Prompt),
		Deps:        nonBlank(c.Dependencies),
		FileScope:   -1,
		Areas:       -1,
	}
	if n := nonBlank(c.FileScope); n > 0 {
		s.FileScope = n
		s.Areas = scopeAreas(c.FileScope)
		s.RiskPaths = riskyScope(c.FileScope)
	}
	fails, total, err := cardHistory(db, c.ProjectID)
	if err != nil {
		log.Printf("warning: dispatch: route history (task %d): %v", c.ID, err)
		return s
	}
	s.HistSamples = total
	if total > 0 {
		s.HistFailRate = float64(fails) / float64(total)
	}
	return s
}

// cardHistory reads the verdicts of the project's last routeHistoryWindow
// verified cards: each card's LATEST terminal verification counts once, pass or
// fail. inconclusive and error runs are unavailability, not a verdict, and do
// not count either way. The routed card itself is included — a re-dispatch of
// a card whose last attempt failed verification is exactly the history that
// should count.
func cardHistory(db *sql.DB, projectID int64) (fails, total int, err error) {
	rows, err := db.Query(`
		SELECT v.status
		  FROM verification_runs v
		  JOIN tasks t ON t.id = v.task_id
		 WHERE t.project_id = ?
		   AND v.status IN ('pass','fail')
		   AND v.id = (SELECT MAX(v2.id) FROM verification_runs v2
		                WHERE v2.task_id = v.task_id AND v2.status IN ('pass','fail'))
		 ORDER BY v.id DESC
		 LIMIT ?`, projectID, routeHistoryWindow)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			return 0, 0, err
		}
		total++
		if status == "fail" {
			fails++
		}
	}
	return fails, total, rows.Err()
}

// stageModel is the dispatch MODEL ladder, most specific first: the card's own
// override, then the recipe's declared model, then DefaultModel. It returns the
// winning rung beside the value so the route record names which one ran.
// runPlaybook spawns with exactly this value, so the record cannot drift from
// the spawn.
func stageModel(c candidate, pb resolvedPlaybook) (model, rung string) {
	if m := c.Model.String; m != "" {
		return m, route.RungCard
	}
	if pb.model != "" {
		return pb.model, route.RungPlaybook
	}
	return DefaultModel, route.RungDefault
}

// recordRoute computes the card's signals, asks the router, and writes one
// route_decisions row — in shadow, with applied=0 and the used_* columns taken
// from the ladders that actually drive the spawn. It never changes the spawn,
// and a failure anywhere here is logged and swallowed: routing is advisory.
func (s *Service) recordRoute(c candidate, pb resolvedPlaybook, sessionUUID string) {
	mode := route.ModeFromEnv(routeModeEnv)
	switch mode {
	case route.ModeOff:
		return
	case route.ModeActive:
		// Applying a pick is not wired on this surface yet; record what ran.
		log.Printf("warning: dispatch: %s=active is not implemented yet; recording in shadow", routeModeEnv)
		mode = route.ModeShadow
	}
	policy, err := route.LoadPolicy(os.Getenv(route.EnvPolicy))
	if err != nil {
		log.Printf("error: dispatch: route policy (task %d): %v", c.ID, err)
		return
	}
	sig := signalsFor(c, s.DB)
	model, rung := stageModel(c, pb)
	if err := route.Record(s.DB, route.Row{
		Surface:     route.SurfaceDispatch,
		Subject:     route.SubjectTask(c.ID),
		SessionUUID: sessionUUID,
		Mode:        mode,
		Signals:     sig,
		Decision:    route.Decide(sig, policy),
		UsedModel:   model,
		// The same resolution ClaudeRunner.Start performs for every stage.
		UsedEffort:   claudeflags.Effort(effortEnv, DefaultEffort),
		UsedPlaybook: pb.name,
		WonRung:      rung,
		CreatedAt:    s.clock(),
	}); err != nil {
		log.Printf("error: dispatch: %v", err)
	}
}
