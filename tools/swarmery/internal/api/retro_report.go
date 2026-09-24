package api

// GET /api/retro/report — the whole /retro page in one call, plus the
// deterministic markdown digest built from it.
//
// Why one endpoint when five already exist: the improver loop needs the WHOLE
// window as a single consistent snapshot. Five round trips can straddle an
// ingest tick and produce a report whose scorecards and recommendations
// disagree, and the digest they feed would then cite numbers no single moment
// ever had. The five separate endpoints stay — the UI still fetches them
// piecemeal — and this handler reuses their build functions verbatim rather
// than re-deriving any SQL.
//
// Partial rather than 500: a report is evidence, and evidence that loses one
// section is still worth reading. A failing section is logged, returned empty,
// and NAMED in `partial` so both the UI and the digest can say "this section
// failed" instead of silently showing zero.

import (
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/decide"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/retrodigest"
)

// reportDigestLimit is the digest budget for the IMPROVER PROMPT — the same
// 30KB evidence cap internal/improve/bundle.go uses. It is deliberately NOT
// the planner's 8000-byte idea budget: the analysis the improver writes is
// what has to fit there, not the evidence it read.
const reportDigestLimit = 30 * 1024

type retroReportDTO struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Scope  string `json:"scope"`
	Approx bool   `json:"approx"`

	Agents          retroAgentsDTO     `json:"agents"`
	Friction        frictionDTO        `json:"friction"`
	Lessons         retroLessonsDTO    `json:"lessons"`
	Tasks           retroTasksDTO      `json:"tasks"`
	Recommendations recommendationsDTO `json:"recommendations"`
	// Surprises: the window's phase runs that landed furthest from their
	// forecast (learning loop phase 13), cited in the digest as [E:phase:<id>].
	Surprises retroSurprisesDTO `json:"surprises"`
	// Labels: D2 session-label tallies (phase 9), fleet-wide windows only;
	// omitted when nothing is labelled.
	Labels []decide.LabelCount `json:"labels,omitempty"`

	// Partial is true when at least one section failed; PartialSections names
	// them. An empty section with partial=false is genuinely empty.
	Partial         bool     `json:"partial"`
	PartialSections []string `json:"partialSections"`
}

type retroReportResponse struct {
	Report          retroReportDTO `json:"report"`
	Digest          string         `json:"digest"`
	DigestTruncated bool           `json:"digestTruncated"`
	DigestSHA256    string         `json:"digestSha256"`
}

func (h *Handler) retroReport(w http.ResponseWriter, r *http.Request) {
	dr, err := parseRange(r)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	pf, pargs := scopeFilter(r)
	project := r.URL.Query().Get("project")

	rep := h.buildRetroReport(dr, pf, pargs, project)
	digest, truncated := retrodigest.Build(reportToDigest(rep), reportDigestLimit)
	writeJSON(w, retroReportResponse{
		Report:          rep,
		Digest:          digest,
		DigestTruncated: truncated,
		DigestSHA256:    sha256Hex([]byte(digest)),
	}, nil)
}

// buildRetroReport assembles all five sections for one resolved window/scope.
// It never fails: a section whose query errors is logged, left empty and named
// in PartialSections.
func (h *Handler) buildRetroReport(dr dateRange, pf string, pargs []any, project string) retroReportDTO {
	out := retroReportDTO{
		From: dr.days[0], To: dr.days[len(dr.days)-1], Scope: project,
		Lessons:         retroLessonsDTO{Lessons: []retroLessonDTO{}},
		Tasks:           retroTasksDTO{Tasks: []retroTaskDTO{}},
		Recommendations: recommendationsDTO{Recommendations: []recommendationDTO{}},
		Surprises:       retroSurprisesDTO{Surprises: []retroSurpriseDTO{}},
		Friction: frictionDTO{
			DeniedTools: []frictionDeniedDTO{},
			ErrorGroups: []frictionErrGroupDTO{},
		},
		PartialSections: []string{},
	}
	fail := func(section string, err error) {
		log.Printf("retro report: %s section failed: %v", section, err)
		out.PartialSections = append(out.PartialSections, section)
		out.Partial = true
	}

	if agents, err := h.buildRetroAgents(dr, pf, pargs); err != nil {
		fail("agents", err)
	} else {
		out.Agents = agents
		out.Approx = agents.Approx
	}
	if friction, err := h.buildRetroFriction(dr, pf, pargs, project); err != nil {
		fail("friction", err)
	} else {
		out.Friction = friction
		out.Approx = out.Approx || friction.Approx
	}
	if lessons, err := h.buildRetroLessons(dr, pf, pargs); err != nil {
		fail("lessons", err)
	} else {
		out.Lessons = lessons
	}
	if tasks, err := h.buildRetroTasks(dr, pf, pargs); err != nil {
		fail("tasks", err)
	} else {
		out.Tasks = tasks
	}
	// The rail's default status set, and the same evidence-attribution scope
	// the UI uses — a project-scoped report must not quote another project's
	// recommendations as its own evidence.
	if recs, err := h.buildRetroRecommendations(defaultRecStatuses(), project); err != nil {
		fail("recommendations", err)
	} else {
		out.Recommendations = recs
	}
	if surprises, err := h.buildRetroSurprises(dr, pf, pargs); err != nil {
		fail("surprises", err)
	} else {
		out.Surprises = surprises
	}
	// Session labels are fleet-wide: a project-scoped report must not quote
	// other projects' sessions, and the labels table has no project column.
	if project == "" {
		if labels, err := decide.LabelCounts(h.DB, out.From, out.To); err != nil {
			fail("labels", err)
		} else {
			out.Labels = labels
		}
	}
	sort.Strings(out.PartialSections)
	return out
}

// defaultRecStatuses is defaultRecStatusFilter as the slice the build function
// takes. Derived from the const so the report and the rail can never drift
// into different status sets.
func defaultRecStatuses() []string {
	parts := strings.Split(defaultRecStatusFilter, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// reportToDigest projects the HTTP DTOs onto retrodigest's storage-free
// structs. The mapping is deliberately explicit: the digest is a stable
// contract the improver agent cites against, so a new DTO field must be an
// intentional addition here rather than an automatic one.
func reportToDigest(rep retroReportDTO) retrodigest.Report {
	out := retrodigest.Report{
		From: rep.From, To: rep.To, Scope: rep.Scope, Approx: rep.Approx,
		Partial: rep.PartialSections,
		Main: retrodigest.Main{
			CostUSD:   rep.Agents.Main.CostUSD,
			TokensOut: rep.Agents.Main.TokensOut,
			Errors:    rep.Agents.Main.Errors,
		},
	}
	prev := map[string]retroPrevDTO{}
	for _, a := range rep.Agents.Agents {
		prev[a.Agent] = a.Prev
	}
	for _, a := range rep.Agents.Agents {
		out.Agents = append(out.Agents, retrodigest.Agent{
			Name: a.Agent, Runs: a.Runs, Sessions: a.Sessions, CostUSD: a.CostUSD,
			TokensOut: a.TokensOut, Errors: a.Errors, ErrorRate: a.ErrorRate,
			P95Ms: a.P95Ms, SuccessRate: a.SuccessRate, ReDispatchRate: a.ReDispatchRate,
			PrevErrorRate: a.Prev.ErrorRate, PrevRuns: a.Prev.Runs,
			Improvable: a.Improvable,
		})
	}
	for _, d := range rep.Friction.DeniedTools {
		out.Friction.DeniedTools = append(out.Friction.DeniedTools, retrodigest.DeniedTool{
			Tool: d.Tool, Calls: d.Calls, Denied: d.Denied, HasRule: d.HasRule,
		})
	}
	for _, g := range rep.Friction.ErrorGroups {
		out.Friction.ErrorGroups = append(out.Friction.ErrorGroups, retrodigest.ErrorGroup{
			Key: g.Key, Example: g.Example, Count: g.Count, LastTs: g.LastTs,
			Sessions: g.Sessions,
		})
	}
	out.Friction.Approvals = retrodigest.Approvals{
		Resolved:      rep.Friction.Approvals.Resolved,
		Pending:       rep.Friction.Approvals.Pending,
		AvgResolveSec: rep.Friction.Approvals.AvgResolveSec,
		WaitTotalMin:  rep.Friction.Approvals.WaitTotalMin,
	}
	for _, l := range rep.Lessons.Lessons {
		out.Lessons = append(out.Lessons, retrodigest.Lesson{
			TaskExternalID: l.TaskExternalID, TaskTitle: l.TaskTitle, Date: l.Date,
			Seq: l.Seq, Title: l.Title, Action: derefStr(l.Action), Body: derefStr(l.Body),
		})
	}
	for _, t := range rep.Tasks.Tasks {
		out.Tasks = append(out.Tasks, retrodigest.Task{
			ExternalID: t.ExternalID, Title: t.Title,
			EstimatedHours: t.EstimatedHours, ActualHours: t.ActualHours,
			VariancePct: t.VariancePct, Loops: t.Loops, Delegations: t.Delegations,
			VerdictOK: t.Verdicts.OK, VerdictRedisp: t.Verdicts.Redispatch,
		})
	}
	for _, su := range rep.Surprises.Surprises {
		out.Surprises = append(out.Surprises, retrodigest.Surprise{
			PhaseID: su.PhaseID, Plan: su.Plan, Phase: su.Phase,
			Index: su.Index, Top: su.Top, Summary: su.Summary,
		})
	}
	for _, l := range rep.Labels {
		out.Labels = append(out.Labels, retrodigest.LabelCount{Field: l.Field, Value: l.Value, Count: l.Count})
	}
	for _, rc := range rep.Recommendations.Recommendations {
		out.Recommendations = append(out.Recommendations, retrodigest.Recommendation{
			ID: rc.ID, Rule: rc.Rule, TargetKind: rc.TargetKind, Target: rc.Target,
			// dedup_key is rule:target by construction (migration 0019); the
			// column is not exposed in the DTO, so rebuild it here.
			DedupKey: rc.Rule + ":" + rc.Target,
			Title:    rc.Title, Detail: rc.Detail, Status: rc.Status,
			Sessions: evidenceSessions(rc.Evidence),
		})
	}
	return out
}

// evidenceSessions pulls the session uuids out of an advisor evidence blob.
// Fleet-level rules (R5 process, R6 config) carry none, and unparseable
// evidence yields none rather than an error — a missing citation is a smaller
// problem than a dropped section.
func evidenceSessions(evidence json.RawMessage) []string {
	if len(evidence) == 0 {
		return nil
	}
	var parsed struct {
		SessionIDs []string `json:"session_ids"`
	}
	if err := json.Unmarshal(evidence, &parsed); err != nil {
		return nil
	}
	return parsed.SessionIDs
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
