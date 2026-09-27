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

// routeRungs are the values the router's rungs offer one card's ladders. All
// three are "" unless the surface is ACTIVE — off and shadow never put a route
// value on a spawn, which is what keeps both byte-identical to pre-router
// dispatch. model is a full ID (route.ModelID), never an alias.
type routeRungs struct {
	model    string
	effort   string
	playbook string
}

// cardRoute is the router's answer for one card, computed once at admission,
// before its playbook resolves (the route rung sits in the playbook ladder).
type cardRoute struct {
	mode     route.Mode // off ⇒ nothing below is set
	ok       bool       // a decision exists: mode is shadow|active and the policy loaded
	signals  route.Signals
	decision route.Decision
	rungs    routeRungs // active only
}

// routeCard scores the card and, in active mode, turns the decision into its
// route rungs. Off consults nothing; an unloadable policy is logged and routes
// nothing (the card runs on the pre-router ladders and no row is written),
// because a routing problem must never cost the run.
func (s *Service) routeCard(c candidate) cardRoute {
	rt := cardRoute{mode: route.ModeFromEnv(routeModeEnv)}
	if rt.mode == route.ModeOff {
		return rt
	}
	policy, err := route.LoadPolicy(os.Getenv(route.EnvPolicy))
	if err != nil {
		log.Printf("error: dispatch: route policy (task %d): %v", c.ID, err)
		return rt
	}
	rt.ok = true
	rt.signals = signalsFor(c, s.DB)
	rt.decision = route.Decide(rt.signals, policy)
	if rt.mode == route.ModeActive {
		rt.rungs = activeRungs(c.ID, rt.decision)
	}
	return rt
}

// activeRungs maps a decision onto the values its rungs offer the spawn. Each
// part is checked on its own and a bad one only drops THAT rung (the next rung
// down takes over): the router is an optimisation, and a pick that cannot run
// must degrade to the pre-router choice rather than to a dead spawn.
//
// review-heavy is refused here as well as in route.Decide: it is a human
// opt-in, and this is the last line before a playbook reaches the card.
func activeRungs(taskID int64, d route.Decision) routeRungs {
	var r routeRungs
	if id, err := route.ModelID(d.Model); err == nil {
		r.model = id
	} else {
		log.Printf("warning: dispatch: task=%d route model %q unusable, skipping the route model rung: %v", taskID, d.Model, err)
	}
	if e, ok := claudeflags.NormalizeEffort(d.Effort); ok && e != "" {
		r.effort = e
	} else {
		log.Printf("warning: dispatch: task=%d route effort %q unusable, skipping the route effort rung", taskID, d.Effort)
	}
	switch pb := strings.ToLower(strings.TrimSpace(d.Playbook)); pb {
	case route.PlaybookStandard, route.PlaybookPlanFirst:
		r.playbook = pb
	case "":
	default:
		log.Printf("warning: dispatch: task=%d route playbook %q is not auto-selectable, skipping the route playbook rung", taskID, d.Playbook)
	}
	return r
}

// stageModel is the dispatch MODEL ladder, most specific first: the card's own
// override, then the recipe's declared model, then the router's pick (active
// only), then DefaultModel. It returns the winning rung beside the value so the
// route record names which one ran. runPlaybook spawns with exactly this value,
// so the record cannot drift from the spawn.
func stageModel(c candidate, pb resolvedPlaybook) (model, rung string) {
	if m := c.Model.String; m != "" {
		return m, route.RungCard
	}
	if pb.model != "" {
		return pb.model, route.RungPlaybook
	}
	if pb.route.model != "" {
		return pb.route.model, route.RungRoute
	}
	return DefaultModel, route.RungDefault
}

// stageEffort is the dispatch EFFORT ladder's top rung: the router's pick in
// active mode, else "" — which hands the choice to runEffort's
// SWARMERY_DISPATCH_EFFORT → DefaultEffort, exactly the pre-router resolution.
// The route rung sits ABOVE the env knob here (unlike phaserun's) because
// dispatch has no request or doc rung: the knob is a machine-wide default, and
// the router's per-card judgement is the more specific of the two.
func stageEffort(pb resolvedPlaybook) string { return pb.route.effort }

// recordRoute writes the card's route_decisions row: the decision beside what
// the ladders actually put on the spawn. In shadow the row says applied=0 and
// used_* equal the pre-router picks; in active it says whether any route rung
// won (applied) and names the model rung (won_rung=route when the router's
// model ran). It never changes the spawn, and a failure is logged and
// swallowed: routing is advisory.
func (s *Service) recordRoute(c candidate, pb resolvedPlaybook, rt cardRoute, sessionUUID string) {
	if !rt.ok {
		return
	}
	model, rung := stageModel(c, pb)
	effort := stageEffort(pb)
	applied := rt.mode == route.ModeActive && (rung == route.RungRoute || effort != "" || pb.routed)
	if err := route.Record(s.DB, route.Row{
		Surface:     route.SurfaceDispatch,
		Subject:     route.SubjectTask(c.ID),
		SessionUUID: sessionUUID,
		Mode:        rt.mode,
		Signals:     rt.signals,
		Decision:    rt.decision,
		Applied:     applied,
		UsedModel:   model,
		// The same resolution ClaudeRunner.Start performs for every stage.
		UsedEffort:   runEffort(RunSpec{Effort: effort}),
		UsedPlaybook: pb.name,
		WonRung:      rung,
		CreatedAt:    s.clock(),
	}); err != nil {
		log.Printf("error: dispatch: %v", err)
	}
}
