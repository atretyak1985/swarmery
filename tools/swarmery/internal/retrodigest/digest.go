// Package retrodigest renders a retro report as a deterministic markdown
// digest — the evidence text the system-improver agent reasons over, and the
// text an accepted analysis carries into Planning Mode as its idea.
//
// The package is deliberately storage-free: it takes plain structs (filled in
// by internal/api from the same DTOs /retro already serves) and returns
// markdown. That keeps the formatting under cheap unit tests instead of
// behind a seeded database.
//
// Two invariants the callers depend on:
//
//   - Determinism. The same Report renders byte-identically every time: every
//     collection is sorted with a total order before rendering, no map is
//     iterated without sorting its keys, and no clock is read — the window is
//     a field, not time.Now().
//   - Citations. Every evidence line ends in at least one marker of the form
//     [E:<kind>:<id>], kind ∈ {agent, rec, error_group, session, task,
//     lesson, phase}. The improver agent may only cite ids it finds here, and
//     internal/retroanalysis validates its output against that vocabulary.
package retrodigest

import (
	"fmt"
	"sort"
	"strings"
)

// Kind is the citation-marker vocabulary. An id that is not one of these is
// not citable, and an analysis quoting one is rejected downstream.
const (
	KindAgent      = "agent"
	KindRec        = "rec"
	KindErrorGroup = "error_group"
	KindSession    = "session"
	KindTask       = "task"
	KindLesson     = "lesson"
	// KindPhase cites one plan phase (epic_phases.id) by its forecast-vs-actual
	// surprise score (learning loop phase 13).
	KindPhase = "phase"
)

// Report is the storage-free shape of one /retro window. Field names mirror
// the DTOs in internal/api/retro.go; pointer fields stay pointers so "no data"
// renders as "n/a" rather than a misleading zero.
type Report struct {
	From   string // YYYY-MM-DD, inclusive
	To     string // YYYY-MM-DD, inclusive
	Scope  string // project slug; "" = whole fleet
	Approx bool   // the window overlaps rolled-up (pruned) days

	Main            Main
	Agents          []Agent
	Friction        Friction
	Lessons         []Lesson
	Tasks           []Task
	Recommendations []Recommendation
	// Surprises are the window's most surprising phase runs — where a run
	// landed furthest from its forecast (internal/surprise). The caller passes
	// the top ones already; the digest sorts them.
	Surprises []Surprise
	// Labels are D2's session-label tallies (internal/decide, phase 9). Read
	// when present: an empty slice adds no section, so a fleet without the
	// classifier gets the same digest byte for byte.
	Labels []LabelCount

	// Partial names the sections whose query failed upstream. They render as
	// an explicit warning so the reader never mistakes a failed section for
	// an empty one.
	Partial []string
}

// Main is the orchestrator's own totals (it has no subagent_start of its own,
// so it has no run count).
type Main struct {
	CostUSD   float64
	TokensOut int64
	Errors    int64
}

// Agent is one scorecard row. ErrorRate is the behaviour-failed-run share, the
// same grain the advisor's R2 fires on.
type Agent struct {
	Name           string
	Runs           int64
	Sessions       int64
	CostUSD        float64
	TokensOut      int64
	Errors         int64
	ErrorRate      float64
	P95Ms          *int64
	SuccessRate    *float64
	ReDispatchRate *float64
	PrevErrorRate  float64
	PrevRuns       int64
	Improvable     bool
}

// impact ranks agents for the digest: the estimated number of behaviour-failed
// runs (rate × runs) is what an improvement would actually buy back, so it
// beats a raw run count at putting the worst offender first.
func (a Agent) impact() float64 { return a.ErrorRate * float64(a.Runs) }

// Friction is the friction board: denied tools, top error groups, approval waits.
type Friction struct {
	DeniedTools []DeniedTool
	ErrorGroups []ErrorGroup
	Approvals   Approvals
}

// DeniedTool is one tool with at least one denial in the window. HasRule says
// whether an enabled approval rule already covers it.
type DeniedTool struct {
	Tool    string
	Calls   int64
	Denied  int64
	HasRule bool
}

// ErrorGroup is one folded error signature plus the sessions it fired in.
type ErrorGroup struct {
	Key      string
	Example  string
	Count    int64
	LastTs   string
	Sessions []string
}

// Approvals is the permission-request wait summary. Pending is "pending now"
// and deliberately not window-filtered.
type Approvals struct {
	Resolved      int64
	Pending       int64
	AvgResolveSec *float64
	WaitTotalMin  float64
}

// Lesson is one parsed lessons-learned entry. TaskExternalID+Seq is its
// stable identity, and its citation id.
type Lesson struct {
	TaskExternalID string
	TaskTitle      string
	Date           string
	Seq            int64
	Title          string
	Action         string
	Body           string
}

// Task is one workspace task's estimation accuracy and orchestration churn.
type Task struct {
	ExternalID     string
	Title          string
	EstimatedHours *float64
	ActualHours    *float64
	VariancePct    *float64
	Loops          int64
	Delegations    int64
	VerdictOK      int64
	VerdictRedisp  int64
}

// Recommendation is one advisor row. DedupKey is rule:target — the identity
// migration 0019 dedupes on, and the digest's secondary sort key.
type Recommendation struct {
	ID         int64
	Rule       string
	TargetKind string
	Target     string
	DedupKey   string
	Title      string
	Detail     string
	Status     string
	// Sessions are the evidence session uuids, already extracted from the
	// advisor's evidence JSON by the caller.
	Sessions []string
}

// Surprise is one scored phase run: how far it landed from its forecast.
// PhaseID is its citation id.
type Surprise struct {
	PhaseID int64
	Plan    string // the plan's title
	Phase   string // the phase's name
	Index   float64
	Top     string // the component that contributed most; "" when none did
	Summary string // the scorer's one-sentence account
}

// LabelCount is one (field, value) tally of classifier session labels.
type LabelCount struct {
	Field string
	Value string
	Count int64
}

// section is one rendered block: a head that always survives, an item list
// that can be trimmed, and the priority that decides who is dropped first when
// even fair shares do not fit.
type section struct {
	name string
	// prio: lower is dropped first.
	prio int
	// head is the heading plus any per-section preamble. A section renders at
	// all only if its head fits.
	head string
	// items are the evidence lines, each newline-terminated, in render order.
	items []string
	// empty is what the section says when it has no items — an honest "nothing
	// happened" rather than a bare heading.
	empty string
}

// Section priorities. Recommendations are the advisor's already-reasoned
// conclusions and agents are the raw health signal, so they survive longest;
// the estimation table is the first thing an improver can do without.
const (
	// Surprises go first under pressure: each is a single-run signal, where
	// every other section aggregates many runs.
	prioSurprises = 0
	prioTasks     = 1
	prioLessons   = 2
	prioFriction  = 3
	prioAgents    = 4
	prioRecs      = 5
)

// truncMarker is appended verbatim when whole sections were dropped; %d is the
// number omitted. Callers grep for the "digest truncated" prefix.
const truncMarker = "\n_(digest truncated: %d sections omitted)_\n"

// hardCutMarker terminates a digest whose header alone overflows the limit —
// the degenerate case a caller should never hit, kept honest rather than
// silently over-budget.
const hardCutMarker = "\n_(digest truncated)_\n"

// size is the section's full rendered length.
func (sec section) size() int {
	n := len(sec.head)
	if len(sec.items) == 0 {
		return n + len(sec.empty)
	}
	for _, it := range sec.items {
		n += len(it)
	}
	return n
}

// omissionNote is the line a trimmed section ends with. Silence here would be
// the worst outcome available: a reader would take a truncated list for the
// whole list and conclude the rest of the fleet is fine.
func omissionNote(name string, n int) string {
	return fmt.Sprintf("_(%d more %s omitted)_\n", n, name)
}

// render emits the section within budget bytes, dropping trailing items if it
// must. Reports whether anything was left out, and false-ok when not even the
// head fits (the caller then drops the section whole).
func (sec section) render(budget int) (out string, trimmed bool, ok bool) {
	if budget < len(sec.head) {
		return "", true, false
	}
	if len(sec.items) == 0 {
		if budget < len(sec.head)+len(sec.empty) {
			return sec.head, true, true
		}
		return sec.head + sec.empty, false, true
	}
	var b strings.Builder
	b.WriteString(sec.head)
	used := len(sec.head)
	for i, it := range sec.items {
		// Reserve room for the note the moment dropping becomes possible.
		reserve := 0
		if i < len(sec.items)-1 {
			reserve = len(omissionNote(sec.name, len(sec.items)-i))
		}
		if used+len(it)+reserve > budget {
			note := omissionNote(sec.name, len(sec.items)-i)
			if used+len(note) <= budget {
				b.WriteString(note)
			}
			return b.String(), true, true
		}
		b.WriteString(it)
		used += len(it)
	}
	return b.String(), false, true
}

// fairShares splits budget across sections so that no single section can starve
// the rest.
//
// This is the whole reason the allocation is not "render everything, then drop
// from the bottom": on a real fleet the advisor emits dozens of
// recommendations, and rendered in full they consume the entire budget — the
// improver would then never see the friction board, the lessons or the
// estimation table AT ALL, on every window, silently. A section that fits under
// its share releases the surplus to the ones that do not, so short sections
// stay whole and only genuinely long ones get trimmed.
func fairShares(secs []section, budget int) map[string]int {
	out := make(map[string]int, len(secs))
	remaining := budget
	pending := make([]section, 0, len(secs))
	pending = append(pending, secs...)
	for len(pending) > 0 {
		share := remaining / len(pending)
		next := pending[:0:0]
		settled := false
		for _, sec := range pending {
			if sec.size() <= share {
				out[sec.name] = sec.size()
				remaining -= sec.size()
				settled = true
				continue
			}
			next = append(next, sec)
		}
		if !settled {
			// Everyone left wants more than an equal share: split what is left.
			share = remaining / len(next)
			for _, sec := range next {
				out[sec.name] = share
			}
			return out
		}
		pending = next
	}
	return out
}

// Build renders the report as markdown no longer than limit BYTES, and reports
// whether anything was dropped.
//
// Bytes, not runes, because the consumers measure bytes: len(idea) against
// maxPlanningIdeaLen in internal/api/planning.go, and the model's context
// budget. A rune-based cap would silently overshoot on Cyrillic prose.
//
// Three stages, in order of how much they cost the reader:
//  1. everything fits — render it all;
//  2. fair shares — every section keeps its heading and as many items as its
//     share allows, each trimmed section saying how many it dropped;
//  3. whole sections, least important first — only when a section cannot even
//     render its heading.
//
// The header (window, scope, partial-section warning) is never dropped: it is
// what tells the reader which slice of reality they are looking at.
func Build(r Report, limit int) (string, bool) {
	header := buildHeader(r)
	sections := []section{
		buildRecommendations(r.Recommendations),
		buildAgents(r.Main, r.Agents),
		buildFriction(r.Friction),
		buildLessons(r.Lessons),
		buildTasks(r.Tasks),
		buildSurprises(r.Surprises),
	}
	if len(r.Labels) > 0 {
		sections = append(sections, buildLabels(r.Labels))
	}

	full := header
	for _, sec := range sections {
		out, _, _ := sec.render(sec.size())
		full += out
	}
	if len(full) <= limit {
		return full, false
	}

	// Stage 2: fair shares over what is left after the header.
	kept := sections
	for dropped := 0; ; dropped++ {
		reserve := 0
		if dropped > 0 {
			reserve = len(fmt.Sprintf(truncMarker, dropped))
		}
		avail := limit - len(header) - reserve
		if avail > 0 && len(kept) > 0 {
			shares := fairShares(kept, avail)
			var b strings.Builder
			b.WriteString(header)
			allFit := true
			for _, sec := range kept {
				out, _, ok := sec.render(shares[sec.name])
				if !ok {
					allFit = false
					break
				}
				b.WriteString(out)
			}
			if allFit {
				if dropped > 0 {
					b.WriteString(fmt.Sprintf(truncMarker, dropped))
				}
				if out := b.String(); len(out) <= limit {
					return out, true
				}
			}
		}
		// Stage 3: drop the least important remaining section and retry.
		if len(kept) == 0 {
			break
		}
		kept = withoutLeastImportant(kept)
	}
	// Even the header overflows: cut hard rather than hand a caller a payload
	// its own limit check will reject.
	return hardCut(header, limit), true
}

// withoutLeastImportant removes the lowest-priority section (name as the
// tie-break, so the order is total even if two ever share a priority).
func withoutLeastImportant(secs []section) []section {
	if len(secs) == 0 {
		return secs
	}
	worst := 0
	for i, sec := range secs {
		if sec.prio < secs[worst].prio ||
			(sec.prio == secs[worst].prio && sec.name < secs[worst].name) {
			worst = i
		}
	}
	out := make([]section, 0, len(secs)-1)
	out = append(out, secs[:worst]...)
	return append(out, secs[worst+1:]...)
}

// hardCut trims s to at most limit bytes without splitting a UTF-8 rune,
// leaving room for the marker when the limit allows one at all.
func hardCut(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s) <= limit {
		return s
	}
	body := limit
	if limit > len(hardCutMarker) {
		body = limit - len(hardCutMarker)
	}
	cut := s[:body]
	// Back off to a rune boundary: continuation bytes are 0b10xxxxxx.
	for len(cut) > 0 && cut[len(cut)-1]&0xC0 == 0x80 {
		cut = cut[:len(cut)-1]
	}
	if len(cut) > 0 && cut[len(cut)-1]&0x80 != 0 {
		cut = cut[:len(cut)-1] // a lead byte whose continuations were cut off
	}
	if body == limit {
		return cut
	}
	return cut + hardCutMarker
}

// cite renders one citation marker.
func cite(kind, id string) string { return "[E:" + kind + ":" + id + "]" }

func buildHeader(r Report) string {
	scope := "whole fleet"
	if r.Scope != "" {
		scope = "project " + r.Scope
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Retro digest %s → %s (%s)\n\n", r.From, r.To, scope)
	b.WriteString("Every evidence line ends in one or more `[E:kind:id]` citation markers. " +
		"Cite only ids that appear below — inventing one invalidates the analysis.\n")
	if r.Approx {
		b.WriteString("\n> The window overlaps rolled-up days: per-event detail there was pruned, so counts are approximate.\n")
	}
	if len(r.Partial) > 0 {
		p := append([]string(nil), r.Partial...)
		sort.Strings(p)
		fmt.Fprintf(&b, "\n> Sections that failed to load and are EMPTY here, not zero: %s.\n", strings.Join(p, ", "))
	}
	return b.String()
}

func buildAgents(main Main, agents []Agent) section {
	sec := section{name: "agents", prio: prioAgents,
		empty: "No agent ran in this window.\n"}
	sec.head = fmt.Sprintf("\n## Agents\n\nOrchestrator (main): $%.2f, %d tokens out, %d errors.\n\n",
		main.CostUSD, main.TokensOut, main.Errors)

	rows := append([]Agent(nil), agents...)
	sort.Slice(rows, func(i, j int) bool {
		if ii, jj := rows[i].impact(), rows[j].impact(); ii != jj {
			return ii > jj
		}
		if rows[i].Runs != rows[j].Runs {
			return rows[i].Runs > rows[j].Runs
		}
		return rows[i].Name < rows[j].Name
	})
	for _, a := range rows {
		var b strings.Builder
		fmt.Fprintf(&b, "- `%s` — %d runs in %d sessions, error rate %s (prev %s over %d runs), %d errors, $%.2f",
			a.Name, a.Runs, a.Sessions, pct(a.ErrorRate), pct(a.PrevErrorRate), a.PrevRuns, a.Errors, a.CostUSD)
		if a.SuccessRate != nil {
			fmt.Fprintf(&b, ", success %s", pct(*a.SuccessRate))
		}
		if a.ReDispatchRate != nil {
			fmt.Fprintf(&b, ", re-dispatch %s", pct(*a.ReDispatchRate))
		}
		if a.P95Ms != nil {
			fmt.Fprintf(&b, ", p95 %ds", *a.P95Ms/1000)
		}
		if !a.Improvable {
			b.WriteString(", no editable definition file")
		}
		fmt.Fprintf(&b, " %s\n", cite(KindAgent, a.Name))
		sec.items = append(sec.items, b.String())
	}
	return sec
}

func buildRecommendations(recs []Recommendation) section {
	sec := section{name: "recommendations", prio: prioRecs,
		head:  "\n## Advisor recommendations\n\n",
		empty: "The rule engine produced no open recommendation for this window.\n"}

	rows := append([]Recommendation(nil), recs...)
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Rule != rows[j].Rule {
			return rows[i].Rule < rows[j].Rule
		}
		if rows[i].DedupKey != rows[j].DedupKey {
			return rows[i].DedupKey < rows[j].DedupKey
		}
		return rows[i].ID < rows[j].ID
	})
	for _, rc := range rows {
		var b strings.Builder
		fmt.Fprintf(&b, "- **%s** [%s] `%s` (%s) — %s %s",
			rc.Rule, rc.Status, rc.Target, rc.TargetKind, rc.Title, cite(KindRec, itoa(rc.ID)))
		if rc.Detail != "" {
			fmt.Fprintf(&b, "\n  - rationale: %s %s", oneLine(rc.Detail), cite(KindRec, itoa(rc.ID)))
		}
		if s := citeSessions(rc.Sessions); s != "" {
			fmt.Fprintf(&b, "\n  - evidence sessions: %s", s)
		}
		b.WriteString("\n")
		sec.items = append(sec.items, b.String())
	}
	return sec
}

// buildFriction renders three sub-blocks as ONE trimmable list. The approvals
// summary is two lines and goes in the head, so it survives any trim: it is the
// only figure here that describes a human being kept waiting.
func buildFriction(f Friction) section {
	sec := section{name: "friction lines", prio: prioFriction,
		empty: "Nothing in this window was denied and no error fired.\n"}

	var h strings.Builder
	h.WriteString("\n## Friction\n\n")
	fmt.Fprintf(&h, "Approvals: %d resolved, %d pending now, %.1f minutes of total wait",
		f.Approvals.Resolved, f.Approvals.Pending, f.Approvals.WaitTotalMin)
	if f.Approvals.AvgResolveSec != nil {
		fmt.Fprintf(&h, ", %.0fs average", *f.Approvals.AvgResolveSec)
	}
	h.WriteString("\n\n")
	sec.head = h.String()

	denied := append([]DeniedTool(nil), f.DeniedTools...)
	sort.Slice(denied, func(i, j int) bool {
		if denied[i].Denied != denied[j].Denied {
			return denied[i].Denied > denied[j].Denied
		}
		return denied[i].Tool < denied[j].Tool
	})
	for _, d := range denied {
		rule := "no approval rule covers it"
		if d.HasRule {
			rule = "already covered by an approval rule"
		}
		sec.items = append(sec.items, fmt.Sprintf("- denied tool `%s` — %d of %d calls denied, %s\n",
			d.Tool, d.Denied, d.Calls, rule))
	}

	groups := append([]ErrorGroup(nil), f.ErrorGroups...)
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Count != groups[j].Count {
			return groups[i].Count > groups[j].Count
		}
		return groups[i].Key < groups[j].Key
	})
	for _, g := range groups {
		var b strings.Builder
		fmt.Fprintf(&b, "- error group `%s` ×%d, last %s — %s %s",
			g.Key, g.Count, g.LastTs, oneLine(g.Example), cite(KindErrorGroup, g.Key))
		if s := citeSessions(g.Sessions); s != "" {
			fmt.Fprintf(&b, "\n  - sessions: %s", s)
		}
		b.WriteString("\n")
		sec.items = append(sec.items, b.String())
	}
	return sec
}

func buildLessons(lessons []Lesson) section {
	sec := section{name: "lessons", prio: prioLessons,
		head:  "\n## Lessons learned\n\n",
		empty: "No task in this window recorded a lesson.\n"}

	rows := append([]Lesson(nil), lessons...)
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Date != rows[j].Date {
			return rows[i].Date > rows[j].Date
		}
		if rows[i].TaskExternalID != rows[j].TaskExternalID {
			return rows[i].TaskExternalID < rows[j].TaskExternalID
		}
		return rows[i].Seq < rows[j].Seq
	})
	for _, l := range rows {
		id := l.TaskExternalID + "#" + itoa(l.Seq)
		var b strings.Builder
		fmt.Fprintf(&b, "- %s (%s) — %s", l.Date, l.TaskExternalID, oneLine(l.Title))
		if l.Action != "" {
			fmt.Fprintf(&b, " → %s", oneLine(l.Action))
		}
		fmt.Fprintf(&b, " %s\n", cite(KindLesson, id))
		sec.items = append(sec.items, b.String())
	}
	return sec
}

func buildTasks(tasks []Task) section {
	sec := section{name: "tasks", prio: prioTasks,
		head:  "\n## Estimation accuracy and churn\n\n",
		empty: "No workspace task in this window carried a parsed artifact.\n"}

	rows := append([]Task(nil), tasks...)
	sort.Slice(rows, func(i, j int) bool {
		// Worst overrun first; unestimated tasks sort last.
		iv, jv := absVariance(rows[i].VariancePct), absVariance(rows[j].VariancePct)
		if iv != jv {
			return iv > jv
		}
		return rows[i].ExternalID < rows[j].ExternalID
	})
	for _, t := range rows {
		var b strings.Builder
		fmt.Fprintf(&b, "- `%s` — %s", t.ExternalID, oneLine(t.Title))
		if t.EstimatedHours != nil && t.ActualHours != nil {
			fmt.Fprintf(&b, ", estimated %.1fh vs actual %.1fh", *t.EstimatedHours, *t.ActualHours)
		}
		if t.VariancePct != nil {
			fmt.Fprintf(&b, " (%+.0f%%)", *t.VariancePct)
		}
		fmt.Fprintf(&b, ", %d loops, %d delegations (%d ok / %d re-dispatched) %s\n",
			t.Loops, t.Delegations, t.VerdictOK, t.VerdictRedisp, cite(KindTask, t.ExternalID))
		sec.items = append(sec.items, b.String())
	}
	return sec
}

func buildSurprises(surprises []Surprise) section {
	sec := section{name: "surprises", prio: prioSurprises,
		head:  "\n## Forecast surprises\n\n",
		empty: "No phase run in this window was scored against a forecast.\n"}

	rows := append([]Surprise(nil), surprises...)
	sort.Slice(rows, func(i, j int) bool {
		// Most surprising first; the phase id is the total-order tie-break.
		if rows[i].Index != rows[j].Index {
			return rows[i].Index > rows[j].Index
		}
		return rows[i].PhaseID < rows[j].PhaseID
	})
	for _, s := range rows {
		top := s.Top
		if top == "" {
			top = "none"
		}
		sec.items = append(sec.items, fmt.Sprintf("- %s / %s — surprise %.2f (top: %s): %s %s\n",
			oneLine(s.Plan), oneLine(s.Phase), s.Index, top, oneLine(s.Summary),
			cite(KindPhase, itoa(s.PhaseID))))
	}
	return sec
}

// citeSessions renders a sorted, deduped, capped list of session citations.
// The cap keeps one noisy error group from crowding out whole sections.
const maxSessionCites = 5

// buildLabels renders the classifier's session-label tallies. Advisory: the
// labels come from a small local model, and the heading says so.
func buildLabels(labels []LabelCount) section {
	sec := section{
		name:  "labels",
		prio:  prioSurprises,
		head:  "\n## Session labels (local classifier, advisory)\n\n",
		empty: "_(no labelled sessions)_\n",
	}
	for _, l := range labels {
		sec.items = append(sec.items, fmt.Sprintf("- %s = %s: %d sessions\n", l.Field, oneLine(l.Value), l.Count))
	}
	return sec
}

func citeSessions(uuids []string) string {
	if len(uuids) == 0 {
		return ""
	}
	seen := map[string]bool{}
	uniq := make([]string, 0, len(uuids))
	for _, u := range uuids {
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		uniq = append(uniq, u)
	}
	if len(uniq) == 0 {
		return ""
	}
	sort.Strings(uniq)
	more := 0
	if len(uniq) > maxSessionCites {
		more = len(uniq) - maxSessionCites
		uniq = uniq[:maxSessionCites]
	}
	parts := make([]string, 0, len(uniq))
	for _, u := range uniq {
		parts = append(parts, cite(KindSession, u))
	}
	out := strings.Join(parts, " ")
	if more > 0 {
		out += fmt.Sprintf(" (+%d more)", more)
	}
	return out
}

// oneLine flattens prose onto a single markdown line: newlines and runs of
// whitespace collapse, so a multi-paragraph detail can never break the list.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func pct(v float64) string { return fmt.Sprintf("%.0f%%", v*100) }

func itoa(v int64) string { return fmt.Sprintf("%d", v) }

// absVariance ranks tasks by overrun magnitude; a task with no variance
// recorded sorts last rather than as a perfect estimate.
func absVariance(v *float64) float64 {
	if v == nil {
		return -1
	}
	if *v < 0 {
		return -*v
	}
	return *v
}
