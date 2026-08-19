package api

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/improve"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/projectscan"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/provision"
)

// Handler bundles the API dependencies.
type Handler struct {
	DB *sql.DB
	// Watching reports whether the live ingest pipeline is attached
	// (serve without --no-ingest); surfaced by GET /api/health.
	Watching bool
	// Docs is the markdown source for /api/docs — the embedded docsfs
	// snapshot in production, overridable with any fs.FS in tests.
	Docs fs.FS
	// recPatchHook, when non-nil, runs between patchRecommendation's status
	// read and its guarded status write — a test seam for the 409 conflict
	// path (nil in production).
	recPatchHook func()
	// Improve is the agent-rewriter service behind the /api/retro/proposals
	// endpoints (self-improvement phase 3).
	Improve *improve.Service
	// improveGo, when non-nil, replaces the `go fn()` dispatch of the async
	// generation pipeline — a test seam for deterministic httptest runs.
	improveGo func(func())
	// Provision owns "enable pack → install + generate" jobs behind the plugin
	// toggle (auto-provision phase 3). Attached at startup next to Improve.
	Provision *provision.Service
	// provisionGo, when non-nil, replaces the `go fn()` dispatch of the async
	// provision pipeline — a test seam mirroring improveGo.
	provisionGo func(func())
	// probes single-flights the account readiness probe (accounts.go) — one
	// `claude` per account at a time; concurrent re-checks share the result.
	probes probeFlights
}

type projectDTO struct {
	ID           int64   `json:"id"`
	Path         string  `json:"path"`
	Slug         string  `json:"slug"`
	Name         *string `json:"name"`
	FirstSeen    string  `json:"firstSeen"`
	LastActivity *string `json:"lastActivity"`
	Archived     bool    `json:"archived"`
	// Dashboard meta (migration 0015): pinned floats the project to the top of
	// the list and the global scope switcher; tags is the decoded JSON array —
	// [] when untagged, never null.
	Pinned bool     `json:"pinned"`
	Tags   []string `json:"tags"`
	// IsSystem marks the deliberate System project (path = ~/.swarmery) that
	// aggregates daemon-spawned telemetry runs; the dashboard demotes it out
	// of the main project list.
	IsSystem bool  `json:"isSystem"`
	Sessions int64 `json:"sessions"`
	// Lifetime token/cost totals across all the project's sessions (deduped
	// turns). Null while the project has no priced turns.
	Tokens  *int64   `json:"tokens"`
	CostUSD *float64 `json:"costUsd"`
	// Plugin is the swarmery-plugin view read from the project's
	// .claude/settings.json; null when the project ships no readable settings
	// (telemetry-only — discovered from ~/.claude transcripts but not onboarded).
	Plugin *projectscan.PluginState `json:"plugin"`
}

// projectDetailDTO is GET /api/projects/{id}: the enriched row plus its local
// component inventory and headline stats (recent sessions).
type projectDetailDTO struct {
	Project    projectDTO              `json:"project"`
	Components *projectscan.Components `json:"components"`
	Stats      projectStatsDTO         `json:"stats"`
}

type projectStatsDTO struct {
	Sessions       int64                     `json:"sessions"`
	Tokens         *int64                    `json:"tokens"`
	CostUSD        *float64                  `json:"costUsd"`
	FirstSeen      string                    `json:"firstSeen"`
	LastActivity   *string                   `json:"lastActivity"`
	RecentSessions []projectRecentSessionDTO `json:"recentSessions"`
}

// projectRecentSessionDTO is the thin session projection shown on the project
// detail page — enough to link to /sessions/{id} without the full sessionDTO.
type projectRecentSessionDTO struct {
	ID          int64    `json:"id"`
	SessionUUID string   `json:"sessionUuid"`
	Title       *string  `json:"title"`
	Status      string   `json:"status"`
	StartedAt   string   `json:"startedAt"`
	Model       *string  `json:"model"`
	Tokens      *int64   `json:"tokens"`
	CostUSD     *float64 `json:"costUsd"`
}

type sessionDTO struct {
	ID          int64   `json:"id"`
	ProjectID   int64   `json:"projectId"`
	ProjectSlug string  `json:"projectSlug"`
	ProjectName *string `json:"projectName"`
	SessionUUID string  `json:"sessionUuid"`
	Model       *string `json:"model"`
	GitBranch   *string `json:"gitBranch"`
	CWD         *string `json:"cwd"`
	Status      string  `json:"status"`
	StartedAt   string  `json:"startedAt"`
	EndedAt     *string `json:"endedAt"`
	Title       *string `json:"title"`
	Source      string  `json:"source"`
	// Account is the Claude Code subscription this session ran under
	// (migration 0047), derived at ingest from the config dir of its
	// transcript root. "" — pre-0047 rows and every hook-minted session —
	// means the stock account and is displayed as ingest.DefaultAccount.
	Account string `json:"account"`
	// Parity contract: per-session aggregates over deduped turns.
	// tokens = SUM(tokens_in + tokens_out), null while the session has no
	// turns; costUsd = SUM(cost_usd), null while no turn is priced.
	Tokens  *int64   `json:"tokens"`
	CostUSD *float64 `json:"costUsd"`
	// ContextTokens is the LAST assistant turn's input footprint
	// (tokens_in + cache_read + cache_write) — a proxy for how full the model's
	// context window is right now. A large value on a long-lived session is the
	// fat-session cost driver: every continuation re-reads that whole context.
	// Null until the session has a priced assistant turn.
	ContextTokens *int64 `json:"contextTokens"`
	// phase 3.5: workspaces — best task link (explicit beats heuristic,
	// then highest confidence); all null while the session is unlinked.
	TaskID         *int64   `json:"taskId"`
	TaskExternalID *string  `json:"taskExternalId"`
	TaskLinkSource *string  `json:"taskLinkSource"` // explicit | heuristic
	TaskConfidence *float64 `json:"taskConfidence"`
	// process liveness (migration 0009): proc_state and pid, null when untracked.
	ProcState *string `json:"procState"`
	ProcPID   *int64  `json:"procPid"`
	// Manual verdict (migration 0014): success | fail | abandoned; null = not judged.
	Outcome *string `json:"outcome"`
	// why: a one-line intent summary derived from the first user turn's prose
	// (additive optional — absent until the session has a user turn with text).
	Why *string `json:"why,omitempty"`
	// ResumeInFlight is true while a dashboard-initiated headless resume
	// (`claude -r -p`) is running for this session — the composer shows Stop.
	// In-memory only (not a DB column); recomputed on each read.
	ResumeInFlight bool `json:"resumeInFlight"`
	// ResumeStartedAt is the RFC3339 start time of that resume run, so the UI
	// can tick a live "Working… (Ns)" timer. Absent when nothing is in flight.
	ResumeStartedAt *string `json:"resumeStartedAt,omitempty"`
	// Handoff is the latest daemon-generated continuation brief for this session
	// (fat-session wave, migration 0039), or null when none exists. The card
	// shows a "Handoff" chip and the detail rail fetches the markdown via
	// GET /api/sessions/{id}/handoff.
	Handoff *handoffDTO `json:"handoff"`
	// PlanGroup names the plan run that spawned this session — the plan-run
	// controller itself, or one phase of it. Resolved from the run branch the
	// daemon stamps (sessions.git_branch), falling back to the run worktree in
	// sessions.cwd for subagents. Null for an ordinary interactive session.
	PlanGroup *sessionPlanDTO `json:"planGroup"`
}

// handoffDTO is the latest handoffs row projected onto a session, without the
// (potentially large) markdown body — the body is fetched lazily via
// GET /api/sessions/{id}/handoff.
type handoffDTO struct {
	Path          string `json:"path"`
	CreatedAt     string `json:"createdAt"`
	ContextTokens int64  `json:"contextTokens"`
}

type turnDTO struct {
	ID               int64    `json:"id"`
	Seq              int64    `json:"seq"`
	Role             string   `json:"role"`
	MessageID        *string  `json:"messageId"`
	Model            *string  `json:"model"`
	StartedAt        string   `json:"startedAt"`
	EndedAt          *string  `json:"endedAt"`
	TokensIn         *int64   `json:"tokensIn"`
	TokensOut        *int64   `json:"tokensOut"`
	TokensCacheRead  *int64   `json:"tokensCacheRead"`
	TokensCacheWrite *int64   `json:"tokensCacheWrite"`
	CostUSD          *float64 `json:"costUsd"`
	Text             *string  `json:"text"`
}

type eventDTO struct {
	ID            int64           `json:"id"`
	TurnID        *int64          `json:"turnId"`
	TS            string          `json:"ts"`
	Type          string          `json:"type"`
	ToolName      *string         `json:"toolName"`
	ParentEventID *int64          `json:"parentEventId"`
	Status        *string         `json:"status"`
	DurationMs    *int64          `json:"durationMs"`
	Payload       json.RawMessage `json:"payload"`
}

type fileChangeDTO struct {
	ID         int64   `json:"id"`
	EventID    int64   `json:"eventId"`
	FilePath   string  `json:"filePath"`
	ChangeType string  `json:"changeType"`
	Additions  *int64  `json:"additions"`
	Deletions  *int64  `json:"deletions"`
	Diff       *string `json:"diff"`
	OutOfScope bool    `json:"outOfScope"`
}

type sessionDetailDTO struct {
	sessionDTO
	Turns       []turnDTO       `json:"turns"`
	Events      []eventDTO      `json:"events"`
	FileChanges []fileChangeDTO `json:"fileChanges"`
	// recovered: count of tool errors in this session that a later same-tool
	// success cleared — the "auto-recovered" count in the detail header.
	// Always present (0 when nothing errored or nothing recovered).
	Recovered int64 `json:"recovered"`
}

// projectSelect projects the registry row plus per-project session/token/cost
// aggregates in ONE grouped pass (no per-row subqueries). Token totals sum the
// deduped turns of every session — the same "true cost" contract as the
// sessions list. {{WHERE}} is replaced by the archived filter, {{EXTRA}} by any
// additional predicate (e.g. a single-id lookup for the detail endpoint).
const projectSelect = `
	SELECT p.id, p.path, p.slug, p.name, p.first_seen, p.last_activity, p.archived,
	       p.pinned, p.tags,
	       COUNT(DISTINCT s.id) AS sessions,
	       SUM(CASE WHEN t.id IS NOT NULL
	                THEN COALESCE(t.tokens_in, 0) + COALESCE(t.tokens_out, 0) END) AS tokens,
	       SUM(t.cost_usd) AS cost_usd
	FROM projects p
	LEFT JOIN sessions s ON s.project_id = p.id
	LEFT JOIN turns t ON t.session_id = s.id
	{{WHERE}}
	GROUP BY p.id`

// scanProject reads one projectSelect row and folds in the plugin state read
// live from the project's .claude/settings.json (advisory, never fatal).
func scanProject(rows *sql.Rows, roots []string) (projectDTO, error) {
	var p projectDTO
	var archived, pinned int
	var tagsRaw string
	var tokens sql.NullInt64
	var cost sql.NullFloat64
	if err := rows.Scan(&p.ID, &p.Path, &p.Slug, &p.Name, &p.FirstSeen,
		&p.LastActivity, &archived, &pinned, &tagsRaw, &p.Sessions, &tokens, &cost); err != nil {
		return p, err
	}
	p.Archived = archived != 0
	p.Pinned = pinned != 0
	// tags is trusted JSON (only PATCH writes it) but a corrupt row must not
	// break the list — degrade to [].
	p.Tags = []string{}
	if err := json.Unmarshal([]byte(tagsRaw), &p.Tags); err != nil || p.Tags == nil {
		p.Tags = []string{}
	}
	if tokens.Valid {
		p.Tokens = &tokens.Int64
	}
	if cost.Valid {
		p.CostUSD = &cost.Float64
	}
	if sys := ingest.SystemDir(); sys != "" && p.Path == sys {
		p.IsSystem = true
	}
	// A single project's unreadable settings must not fail the list — PluginState
	// already collapses those cases to (nil, nil). Declared settings overlays are
	// folded in so a project whose plugin set is injected by a launcher (rather
	// than committed to its repo) still lists as managed.
	if st, err := projectscan.ReadPluginState(p.Path, roots, overlaysFor(p.Path)...); err == nil {
		p.Plugin = st
	}
	return p, nil
}

// GET /api/projects — the registry list. Archived projects are excluded unless
// ?include=archived, so the default dashboard view hides soft-removed projects.
func (h *Handler) listProjects(w http.ResponseWriter, r *http.Request) {
	where := "WHERE p.archived = 0"
	if r.URL.Query().Get("include") == "archived" {
		where = ""
	}
	query := strings.Replace(projectSelect, "{{WHERE}}", where, 1) +
		" ORDER BY p.pinned DESC, p.last_activity DESC"

	rows, err := h.DB.Query(query)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	roots := onboardCfg.Roots
	projects := []projectDTO{}
	for rows.Next() {
		p, err := scanProject(rows, roots)
		if err != nil {
			writeErr(w, err)
			return
		}
		projects = append(projects, p)
	}
	writeJSON(w, projects, rows.Err())
}

// GET /api/projects/{id} — one project enriched with its local component
// inventory and headline stats (recent sessions).
func (h *Handler) getProject(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, `{"error":"invalid project id"}`, http.StatusBadRequest)
		return
	}

	// The row: reuse projectSelect with an id predicate. archived rows are
	// reachable by direct id (the list hides them, detail still resolves them).
	query := strings.Replace(projectSelect, "{{WHERE}}", "WHERE p.id = ?", 1)
	rows, err := h.DB.Query(query, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			writeErr(w, err)
			return
		}
		http.Error(w, `{"error":"project not found"}`, http.StatusNotFound)
		return
	}
	proj, err := scanProject(rows, onboardCfg.Roots)
	if err != nil {
		writeErr(w, err)
		return
	}
	rows.Close()

	components, _ := projectscan.ReadComponents(proj.Path) // never errors
	recent, err := h.recentSessions(id)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, projectDetailDTO{
		Project:    proj,
		Components: components,
		Stats: projectStatsDTO{
			Sessions:       proj.Sessions,
			Tokens:         proj.Tokens,
			CostUSD:        proj.CostUSD,
			FirstSeen:      proj.FirstSeen,
			LastActivity:   proj.LastActivity,
			RecentSessions: recent,
		},
	}, nil)
}

// recentProjectSessionsLimit caps the detail page's session list.
const recentProjectSessionsLimit = 10

// recentSessions returns the project's most recent non-hidden sessions with
// their per-session token/cost totals (one grouped pass, no N+1).
func (h *Handler) recentSessions(projectID int64) ([]projectRecentSessionDTO, error) {
	rows, err := h.DB.Query(`
		SELECT s.id, s.session_uuid, COALESCE(s.custom_title, s.title), s.status, s.started_at, s.model,
		       SUM(CASE WHEN t.id IS NOT NULL
		                THEN COALESCE(t.tokens_in, 0) + COALESCE(t.tokens_out, 0) END) AS tokens,
		       SUM(t.cost_usd) AS cost_usd
		FROM sessions s
		LEFT JOIN turns t ON t.session_id = s.id
		WHERE s.project_id = ? AND s.hidden = 0
		GROUP BY s.id
		ORDER BY s.started_at DESC
		LIMIT ?`, projectID, recentProjectSessionsLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []projectRecentSessionDTO{}
	for rows.Next() {
		var s projectRecentSessionDTO
		var tokens sql.NullInt64
		var cost sql.NullFloat64
		if err := rows.Scan(&s.ID, &s.SessionUUID, &s.Title, &s.Status,
			&s.StartedAt, &s.Model, &tokens, &cost); err != nil {
			return nil, err
		}
		if tokens.Valid {
			s.Tokens = &tokens.Int64
		}
		if cost.Valid {
			s.CostUSD = &cost.Float64
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// sessionSelect is the shared session projection: entity columns plus the
// per-session token/cost aggregates (parity contract) and the owning plan run
// (session_plan_group.go), resolved here so the list endpoint needs no
// follow-up query.
//
// The three `turns`-derived values (totals, context occupancy, "why") are
// CORRELATED scalar subqueries, not derived-table LEFT JOINs. That distinction
// is the whole performance contract of this query: SQLite cannot push a
// predicate into an uncorrelated derived table, so the earlier
// `LEFT JOIN (SELECT … FROM turns GROUP BY session_id)` shape forced
// MATERIALIZE of a FULL scan of `turns` — three times — on every single call,
// no matter how narrow the outer WHERE or how small the LIMIT. `turns` carries
// the fat `text` column, so each of those scans reads the entire table off
// disk. Correlated subqueries instead ride sqlite_autoindex_turns_1
// (session_id, seq) and touch only the rows of the sessions actually selected.
//
// sessionSelect is used for SINGLE-ROW lookups (getSession, ws.sessionByID);
// listSessions instead wraps the same pieces in a keyset-limited page CTE, so
// the subqueries run for the returned page only, never for every candidate row.
//
// `tl` and `ho` stay derived joins on purpose: task_sessions and handoffs are
// small (hundreds of rows), so materializing them is cheap, and each supplies
// several columns — which a scalar subquery cannot.
var sessionSelect = `SELECT ` + sessionCols + sessionFrom

// sessionCols is the projection shared by every session query. Column order is
// the contract with scanSession's dest slice — keep the two in lockstep.
const sessionCols = `
	       s.id, s.project_id, p.slug, p.name, s.session_uuid, s.model, s.git_branch, s.cwd,
	       s.status, s.started_at, s.ended_at, COALESCE(s.custom_title, s.title), s.source,
	       s.account,
	       -- Session totals are the TRUE cost: every turn including subagents
	       -- (phase 2). The Chat tab still shows the orchestrator turns only, but
	       -- the card's cost/tokens reflect the whole session — consistent with
	       -- the overview/today and analytics aggregates. SUM over the empty set
	       -- is NULL, which reproduces the old LEFT JOIN's "no turns" miss.
	       (SELECT SUM(COALESCE(t.tokens_in, 0) + COALESCE(t.tokens_out, 0))
	          FROM turns t WHERE t.session_id = s.id),
	       (SELECT SUM(t.cost_usd) FROM turns t WHERE t.session_id = s.id),
	       -- Context occupancy: the LAST assistant turn's input footprint. Usage
	       -- lives on assistant turns; the newest one's input tokens ≈ how full
	       -- the context window is.
	       (SELECT COALESCE(t.tokens_in, 0) + COALESCE(t.tokens_cache_read, 0) + COALESCE(t.tokens_cache_write, 0)
	          FROM turns t
	         WHERE t.session_id = s.id AND t.role = 'assistant'
	           AND (t.tokens_in IS NOT NULL OR t.tokens_cache_read IS NOT NULL OR t.tokens_cache_write IS NOT NULL)
	         ORDER BY t.seq DESC LIMIT 1),
	       tl.task_id, tl.external_id, tl.link_source, tl.confidence,
	       s.proc_state, s.pid, s.outcome,
	       -- "why": the first user turn's prose.
	       (SELECT t.text FROM turns t
	         WHERE t.session_id = s.id AND t.role = 'user'
	           AND t.text IS NOT NULL AND TRIM(t.text) != ''
	         ORDER BY t.seq LIMIT 1),
	       ho.path, ho.created_at, ho.context_tokens` + sessionPlanGroupCols

// sessionFrom is the FROM/JOIN tail shared by sessionSelect and the page CTE
// built in listSessions, so both resolve rows — and the ?planTask predicate's
// plan_task/phase_task aliases — on identical terms.
var sessionFrom = `
	FROM sessions s
	JOIN projects p ON p.id = s.project_id
	LEFT JOIN (
		-- phase 3.5: one best task link per session, picked in a single
		-- window pass (explicit first, then highest confidence) — no N+1.
		SELECT ts.session_id, ts.task_id, t.external_id, ts.link_source, ts.confidence,
		       ROW_NUMBER() OVER (
		           PARTITION BY ts.session_id
		           ORDER BY (ts.link_source = 'explicit') DESC, ts.confidence DESC
		       ) AS rn
		FROM task_sessions ts
		JOIN tasks t ON t.id = ts.task_id
	) tl ON tl.session_id = s.id AND tl.rn = 1
	LEFT JOIN (
		-- fat-session wave (migration 0039): the latest daemon-generated handoff
		-- brief per session, picked in one window pass — no N+1.
		SELECT session_id, path, created_at, context_tokens,
		       ROW_NUMBER() OVER (PARTITION BY session_id ORDER BY created_at DESC, id DESC) AS rn
		FROM handoffs
	) ho ON ho.session_id = s.id AND ho.rn = 1` + sessionPlanGroupJoins

// sessionsPageDTO is the GET /api/sessions envelope (ops-hygiene wave):
// keyset pagination over (started_at DESC, id DESC). nextCursor is null on
// the last page.
type sessionsPageDTO struct {
	Sessions   []sessionDTO `json:"sessions"`
	NextCursor *string      `json:"nextCursor"`
}

const (
	defaultSessionsLimit = 100
	maxSessionsLimit     = 500
)

// encodeSessionCursor packs the keyset position (started_at, id) of the last
// returned row into an opaque URL-safe token.
func encodeSessionCursor(startedAt string, id int64) string {
	return base64.URLEncoding.EncodeToString([]byte(startedAt + "|" + strconv.FormatInt(id, 10)))
}

// decodeSessionCursor is the inverse of encodeSessionCursor. Any malformed
// token is a client error (400), never a 500.
//
// An EMPTY started_at is a legal position, not a malformed token: sessions.
// started_at is NOT NULL but may be '' (a row minted before any record
// carried a timestamp), and '' sorts last under `ORDER BY started_at DESC`,
// so it is exactly the position a last-page cursor lands on. Rejecting it
// here turned "load more" into a hard 400 whenever such a row fell on a page
// boundary — the cursor must round-trip every value the ORDER BY can produce.
func decodeSessionCursor(cursor string) (startedAt string, id int64, err error) {
	raw, err := base64.URLEncoding.DecodeString(cursor)
	if err != nil {
		return "", 0, fmt.Errorf("invalid cursor")
	}
	startedAt, idStr, ok := strings.Cut(string(raw), "|")
	if !ok {
		return "", 0, fmt.Errorf("invalid cursor")
	}
	id, err = strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return "", 0, fmt.Errorf("invalid cursor")
	}
	return startedAt, id, nil
}

// GET /api/sessions?project=<slug|id>&status=<status>&account=<key>&planTask=<id>&limit=<n>&cursor=<opaque>
//
// Keyset pagination: rows are ordered by (started_at DESC, id DESC); the
// cursor is the opaque position of the last row of the previous page. The
// response is ALWAYS the {sessions, nextCursor} envelope (default limit 100,
// max 500); nextCursor is null on the last page.
//
// &planTask=<workspace task id> narrows the list to the sessions one plan run
// spawned — see the predicate below.
func (h *Handler) listSessions(w http.ResponseWriter, r *http.Request) {
	limit := defaultSessionsLimit
	if q := r.URL.Query().Get("limit"); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n < 1 {
			http.Error(w, `{"error":"invalid limit"}`, http.StatusBadRequest)
			return
		}
		if n > maxSessionsLimit {
			n = maxSessionsLimit
		}
		limit = n
	}

	// Soft-hidden sessions (DELETE /api/sessions/{id}) never appear in the list;
	// they remain reachable by direct id (getSession) so the hide is reversible.
	// Sessions of ARCHIVED projects are excluded too — archiving a project hides
	// it everywhere (list/analytics/overview), while its rows stay reachable by
	// direct id and reappear if the project is restored.
	//
	// `where` accumulates the filters for the PAGE CTE only — the cheap pass that
	// picks which session rows this page returns. The expensive per-session
	// columns (sessionCols) are then projected for those rows alone, so a 50-row
	// page never pays the aggregate cost of the other ~1.7k sessions.
	where := ` WHERE s.hidden = 0 AND p.archived = 0`
	args := []any{}
	if project := r.URL.Query().Get("project"); project != "" {
		where += projectScopePredicate
		args = append(args, scopeArgs(project)...)
	}
	if status := r.URL.Query().Get("status"); status != "" {
		where += ` AND s.status = ?`
		args = append(args, status)
	}
	// Subscription filter (migration 0047): exact match on sessions.account,
	// with ONE synonym — 'default' also matches '', because '' is how the
	// stock account is stored for every row minted before the column existed
	// and for every hook-sourced session, which has no config dir to derive
	// from. Both spellings mean "the default subscription", so a single filter
	// value has to reach both or the UI's `default` chip would hide most of
	// its own rows.
	if account := r.URL.Query().Get("account"); account != "" {
		if account == ingest.DefaultAccount {
			where += ` AND (s.account = '' OR s.account = ?)`
		} else {
			where += ` AND s.account = ?`
		}
		args = append(args, account)
	}
	// ?planTask=<workspace task id> — only the sessions ONE plan produced: its
	// controller (swarm/plan-<taskId>), every phase run of it, and the subagents
	// located by worktree cwd. The predicate reads the same LEFT JOINs the list
	// already carries (session_plan_group.go), so the plan panel and the Sessions
	// page can never disagree about who belongs to a plan.
	if planTask := r.URL.Query().Get("planTask"); planTask != "" {
		id, err := strconv.ParseInt(planTask, 10, 64)
		if err != nil || id < 1 {
			http.Error(w, `{"error":"invalid planTask"}`, http.StatusBadRequest)
			return
		}
		where += ` AND (plan_task.id = ? OR phase_task.id = ?)`
		args = append(args, id, id)
	}
	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		startedAt, id, err := decodeSessionCursor(cursor)
		if err != nil {
			http.Error(w, `{"error":"invalid cursor"}`, http.StatusBadRequest)
			return
		}
		where += ` AND (s.started_at < ? OR (s.started_at = ? AND s.id < ?))`
		args = append(args, startedAt, startedAt, id)
	}
	// limit+1 probes for a next page without a COUNT query.
	//
	// Two stages, both keyed on the same (started_at DESC, id DESC) order:
	//   page — the filters above over sessions/projects (+ the plan-group joins
	//          the ?planTask predicate needs), yielding at most limit+1 ids;
	//   outer — sessionCols for exactly those ids.
	// Keeping the ORDER BY on the outer query too is what makes the page order
	// survive the id round-trip: `IN (SELECT …)` is an unordered set.
	const orderBy = ` ORDER BY s.started_at DESC, s.id DESC`
	query := `WITH page AS (SELECT s.id AS sid` + sessionFrom + where + orderBy + ` LIMIT ?)
		SELECT ` + sessionCols + sessionFrom + ` WHERE s.id IN (SELECT sid FROM page)` + orderBy
	args = append(args, limit+1)

	rows, err := h.DB.Query(query, args...)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()

	sessions := []sessionDTO{}
	for rows.Next() {
		var s sessionDTO
		if err := scanSession(rows.Scan, &s); err != nil {
			writeErr(w, err)
			return
		}
		sessions = append(sessions, s)
	}
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}

	page := sessionsPageDTO{Sessions: sessions}
	if len(sessions) > limit {
		page.Sessions = sessions[:limit]
		last := page.Sessions[limit-1]
		c := encodeSessionCursor(last.StartedAt, last.ID)
		page.NextCursor = &c
	}
	writeJSON(w, page, nil)
}

// GET /api/sessions/{id} — id is the numeric row id or the session UUID.
func (h *Handler) getSession(w http.ResponseWriter, r *http.Request) {
	idArg := r.PathValue("id")
	where := `s.session_uuid = ?`
	if _, err := strconv.ParseInt(idArg, 10, 64); err == nil {
		where = `s.id = ?`
	}

	var d sessionDetailDTO
	err := scanSession(h.DB.QueryRow(sessionSelect+` WHERE `+where, idArg).Scan, &d.sessionDTO)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	setResumeState(&d.sessionDTO)

	d.Turns = []turnDTO{}
	// Chat/transcript is the ORCHESTRATOR conversation only: subagent turns
	// (agent_name set, no prose — phase 2) exist for aggregate analytics and
	// the session's total cost, but would render as empty rows here. Subagent
	// activity is surfaced via the subagent_start/stop events in the timeline.
	rows, err := h.DB.Query(`
		SELECT id, seq, role, message_id, model, started_at, ended_at,
		       tokens_in, tokens_out, tokens_cache_read, tokens_cache_write, cost_usd, text
		FROM turns WHERE session_id = ? AND agent_name IS NULL ORDER BY seq`, d.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	for rows.Next() {
		var t turnDTO
		if err := rows.Scan(&t.ID, &t.Seq, &t.Role, &t.MessageID, &t.Model, &t.StartedAt, &t.EndedAt,
			&t.TokensIn, &t.TokensOut, &t.TokensCacheRead, &t.TokensCacheWrite, &t.CostUSD, &t.Text); err != nil {
			rows.Close()
			writeErr(w, err)
			return
		}
		d.Turns = append(d.Turns, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}

	d.Events = []eventDTO{}
	rows, err = h.DB.Query(`
		SELECT id, turn_id, ts, type, tool_name, parent_event_id, status, duration_ms, payload
		FROM events WHERE session_id = ? ORDER BY ts, id`, d.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	for rows.Next() {
		var e eventDTO
		var payload sql.NullString
		if err := rows.Scan(&e.ID, &e.TurnID, &e.TS, &e.Type, &e.ToolName,
			&e.ParentEventID, &e.Status, &e.DurationMs, &payload); err != nil {
			rows.Close()
			writeErr(w, err)
			return
		}
		if payload.Valid {
			e.Payload = json.RawMessage(payload.String)
		}
		d.Events = append(d.Events, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}

	d.FileChanges = []fileChangeDTO{}
	rows, err = h.DB.Query(`
		SELECT id, event_id, file_path, change_type, additions, deletions, diff, out_of_scope
		FROM file_changes WHERE session_id = ? ORDER BY id`, d.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	for rows.Next() {
		var fc fileChangeDTO
		var oos int
		if err := rows.Scan(&fc.ID, &fc.EventID, &fc.FilePath, &fc.ChangeType,
			&fc.Additions, &fc.Deletions, &fc.Diff, &oos); err != nil {
			rows.Close()
			writeErr(w, err)
			return
		}
		fc.OutOfScope = oos != 0
		d.FileChanges = append(d.FileChanges, fc)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}

	// recovered: tool errors this session later cleared with a same-tool
	// success (the "auto-recovered" header stat). A best-effort heuristic —
	// each errored tool that has any later ok call on the same tool counts once.
	if err := h.DB.QueryRow(`
		SELECT COUNT(*) FROM events e
		WHERE e.session_id = ? AND e.status = 'error' AND e.tool_name IS NOT NULL
		  AND EXISTS (
		      SELECT 1 FROM events e2
		      WHERE e2.session_id = e.session_id AND e2.tool_name = e.tool_name
		        AND e2.status = 'ok' AND (e2.ts > e.ts OR (e2.ts = e.ts AND e2.id > e.id))
		  )`, d.ID).Scan(&d.Recovered); err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, d, nil)
}

func scanSession(scan func(...any) error, s *sessionDTO) error {
	var whyRaw sql.NullString
	var hoPath, hoCreatedAt sql.NullString
	var hoContextTokens sql.NullInt64
	// Plan-group tail (sessionPlanGroupCols), scanned as one unit so the column
	// order lives next to the projection that produced it.
	var group sessionPlanGroupScan
	dest := append([]any{&s.ID, &s.ProjectID, &s.ProjectSlug, &s.ProjectName, &s.SessionUUID, &s.Model,
		&s.GitBranch, &s.CWD, &s.Status, &s.StartedAt, &s.EndedAt, &s.Title, &s.Source,
		&s.Account,
		&s.Tokens, &s.CostUSD, &s.ContextTokens,
		&s.TaskID, &s.TaskExternalID, &s.TaskLinkSource, &s.TaskConfidence,
		&s.ProcState, &s.ProcPID, &s.Outcome,
		&whyRaw,
		&hoPath, &hoCreatedAt, &hoContextTokens}, group.dest()...)
	if err := scan(dest...); err != nil {
		return err
	}
	s.PlanGroup = group.dto()
	if whyRaw.Valid {
		if w := summarizeWhy(whyRaw.String); w != "" {
			s.Why = &w
		}
	}
	if hoPath.Valid {
		s.Handoff = &handoffDTO{
			Path:          hoPath.String,
			CreatedAt:     hoCreatedAt.String,
			ContextTokens: hoContextTokens.Int64,
		}
	}
	return nil
}

// summarizeWhy condenses a first user turn into a one-line intent: the first
// non-empty line, inner whitespace collapsed, capped at whyMaxLen with an
// ellipsis. Returns "" when nothing usable remains.
func summarizeWhy(text string) string {
	line := ""
	for _, raw := range strings.Split(text, "\n") {
		if t := strings.TrimSpace(raw); t != "" {
			line = t
			break
		}
	}
	line = strings.Join(strings.Fields(line), " ")
	if line == "" {
		return ""
	}
	runes := []rune(line)
	if len(runes) > whyMaxLen {
		return strings.TrimSpace(string(runes[:whyMaxLen])) + "…"
	}
	return line
}

const whyMaxLen = 160

func writeJSON(w http.ResponseWriter, v any, err error) {
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("warn: encode response: %v", err)
	}
}

// writeClientErr replies {"error": msg} with the given 4xx status. Unlike the
// hand-written `{"error":"…"}` literals it JSON-encodes msg, so messages built
// from user input cannot break the JSON framing.
func writeClientErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(map[string]string{"error": msg}); err != nil {
		log.Printf("warn: encode response: %v", err)
	}
}

func writeErr(w http.ResponseWriter, err error) {
	log.Printf("error: api: %v", err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
