// Package wsingest is the phase-3.5 workspace ingester (E-lite scope): a
// READ-ONLY periodic scanner over the agent-work.sh workspace repo
// ($AGENT_WORKSPACE_ROOT/<slug>/workspace/{working,archive}) that indexes
// task cards into the tasks table and stitches them to telemetry sessions
// (task_sessions, explicit via logs/sessions.md + heuristic via cwd/time
// overlap). It NEVER writes to the workspace — its only output is DB rows.
//
// Tolerant by contract: a broken card, missing README field, junk id in
// logs/sessions.md, or an unmapped workspace warns and degrades — one bad
// workspace never stops the scan.
package wsingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// DefaultRescanInterval is the fallback periodic rescan cadence. Workspace
// cards change on human/agent cadence, not telemetry cadence — 60s is plenty
// and fsnotify is deliberately not used (E-lite).
const DefaultRescanInterval = 60 * time.Second

// DefaultWorkspaceRoot is the machine-neutral fallback workspace repo location
// (~/swarmery-workspace) used when neither workspace-root env var nor
// --workspace-root is set. Per-user by construction — nothing host-specific is
// baked in.
func DefaultWorkspaceRoot() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "swarmery-workspace")
	}
	return "swarmery-workspace"
}

// Root resolves the workspace root: $AGENT_WORKSPACE_ROOT (the per-project
// runtime var consumer settings carry), else $SWARMERY_WORKSPACE_ROOT (the
// machine-level var scripts/init.sh reads and `swarmery install
// --workspace-root` bakes into the launchd plist), else the default.
func Root() string {
	if v := os.Getenv("AGENT_WORKSPACE_ROOT"); v != "" {
		return v
	}
	if v := os.Getenv("SWARMERY_WORKSPACE_ROOT"); v != "" {
		return v
	}
	return DefaultWorkspaceRoot()
}

// Config tunes the scanner. Zero values fall back to defaults.
type Config struct {
	WorkspaceRoot  string
	RescanInterval time.Duration
}

func (c Config) withDefaults() Config {
	if c.WorkspaceRoot == "" {
		c.WorkspaceRoot = Root()
	}
	if c.RescanInterval <= 0 {
		c.RescanInterval = DefaultRescanInterval
	}
	return c
}

// Stats counts the state after one scan pass (totals, not deltas — the scan
// is a converging upsert, so re-runs report the same numbers).
type Stats struct {
	Workspaces  int // workspace dirs indexed
	Tasks       int // workspace-sourced tasks rows
	Explicit    int // task_sessions rows with link_source='explicit'
	Heuristic   int // task_sessions rows with link_source='heuristic'
	Retros      int // task_retros rows (parsed 09-retrospective.md docs)
	Loops       int // task_loops rows (ORCHESTRATION.md re-dispatch journal)
	Delegations int // task_delegations rows (logs/agents.md ledger)
	EpicPhases  int // epic_phases rows (plan/ README table + phase docs)
	Warnings    int // tolerated parse/mapping stumbles this pass
}

func (s Stats) String() string {
	return fmt.Sprintf("workspaces=%d tasks=%d links(explicit=%d heuristic=%d) artifacts(retros=%d loops=%d delegations=%d epic_phases=%d) warnings=%d",
		s.Workspaces, s.Tasks, s.Explicit, s.Heuristic, s.Retros, s.Loops, s.Delegations, s.EpicPhases, s.Warnings)
}

// Scanner runs idempotent scan passes against one DB.
type Scanner struct {
	db  *sql.DB
	cfg Config
}

// New builds a scanner.
func New(db *sql.DB, cfg Config) *Scanner {
	return &Scanner{db: db, cfg: cfg.withDefaults()}
}

// Run scans immediately, then on every RescanInterval tick until ctx ends.
// Scan errors are logged, never fatal — the loop always keeps ticking.
func (s *Scanner) Run(ctx context.Context) error {
	scan := func() {
		stats, err := s.Scan()
		if err != nil {
			log.Printf("error: wsingest scan: %v", err)
			return
		}
		log.Printf("wsingest: %s", stats)
	}
	scan()
	ticker := time.NewTicker(s.cfg.RescanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			scan()
		}
	}
}

// ─── workspace discovery & project mapping ─────────────────────────────────

// overlayProject is the subset of overlay/project.json wsingest reads.
type overlayProject struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	CodePath    string `json:"codePath"`
}

// normPath canonicalizes a path for matching: absolute, symlinks resolved
// (best-effort — unresolvable paths keep their cleaned form), no trailing
// slash, lower-cased (macOS default FS is case-insensitive).
func normPath(p string) string {
	p = strings.TrimRight(strings.TrimSpace(p), "/")
	if p == "" {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	return strings.ToLower(filepath.Clean(p))
}

type workspace struct {
	id          int64
	slug        string
	rootPath    string
	codePath    string // "" when unmapped
	displayName string
	projectID   sql.NullInt64
}

type projectRow struct {
	id   int64
	path string
	norm string
}

// resolveCodePath maps a workspace slug to its consumer project checkout:
// overlay/project.json codePath first, then <projects.path>/.claude/project.json
// with a matching name, then a projects.path whose basename matches the slug
// (probe finding: northwind/swarmery ship empty overlay/ dirs).
func resolveCodePath(wsDir, slug string, projects []projectRow, warn func(string, ...any)) (code, display string) {
	ov := filepath.Join(wsDir, "overlay", "project.json")
	if raw, err := os.ReadFile(ov); err == nil {
		var p overlayProject
		if err := json.Unmarshal(raw, &p); err != nil {
			warn("workspace %s: unparseable %s: %v", slug, ov, err)
		} else if p.CodePath != "" {
			return p.CodePath, p.DisplayName
		}
	}
	// fallback 1: a telemetry project whose .claude/project.json names this slug
	for _, pr := range projects {
		raw, err := os.ReadFile(filepath.Join(pr.path, ".claude", "project.json"))
		if err != nil {
			continue
		}
		var p overlayProject
		if err := json.Unmarshal(raw, &p); err != nil {
			continue
		}
		if strings.EqualFold(p.Name, slug) {
			if p.CodePath != "" {
				return p.CodePath, p.DisplayName
			}
			return pr.path, p.DisplayName
		}
	}
	// fallback 2: projects.path basename ↔ slug
	for _, pr := range projects {
		if strings.EqualFold(filepath.Base(pr.path), slug) {
			return pr.path, ""
		}
	}
	warn("workspace %s: no overlay/project.json and no project fallback — unmapped (heuristic linking disabled)", slug)
	return "", ""
}

// ─── card parsing (tolerant, per agent-work.sh v5.2 conventions) ────────────

var (
	// `- **Field**: value` card lines; field names vary per card vintage.
	fieldRe = func(names ...string) *regexp.Regexp {
		return regexp.MustCompile(`(?im)\*\*(?:` + strings.Join(names, "|") + `)\*\*:\s*(.+?)\s*(?:·|$)`)
	}
	statusRe = fieldRe("Статус", "status")
	goalRe   = fieldRe("Ціль", "goal")
	// Completion dates appear mid-line (`… · **Завершено**: 2026-07-12`) or as
	// `**status**: **ARCHIVED 2026-07-01**` — search anywhere in the README.
	doneDateRe = regexp.MustCompile(`\*\*Завершено\*\*:\s*(\d{4}-\d{2}-\d{2})`)
	archDateRe = regexp.MustCompile(`ARCHIVED\s+(\d{4}-\d{2}-\d{2})`)
	titleRe    = regexp.MustCompile(`(?m)^#\s+(?:Task:\s*)?(.+?)\s*$`)
	// Task dir date prefix working/YYYY/MM/DD/<slug>.
	datePartRe = regexp.MustCompile(`^\d{4}/\d{2}/\d{2}$`)
	// Session references in logs/sessions.md: full uuid, or a ≥8-hex prefix.
	// The probe showed 20/21 legacy cells hold junk 5-digit ids — length ≥8
	// hex filters them out; only agent-work.sh/hook-written uuids match.
	uuidRe = regexp.MustCompile(`\b[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}\b`)
	hex8Re = regexp.MustCompile(`\b[0-9a-f]{8,}\b`)
)

// card is one parsed task directory.
type card struct {
	externalID string // yyyy-mm-dd-slug
	slug       string // leaf dir name
	dir        string
	archived   bool
	title      string
	goal       string
	status     string     // tasks.status value: running | done
	startedAt  time.Time  // card start date (external_id date), UTC midnight
	archivedAt *time.Time // archive zone only
}

// parseCard reads one task dir tolerantly; it never fails — missing/broken
// pieces degrade to defaults and a warning.
func parseCard(dir, zone, externalID string, warn func(string, ...any)) card {
	c := card{externalID: externalID, slug: filepath.Base(dir), dir: dir, archived: zone == "archive"}
	c.title = c.slug
	c.status = "running"

	if start, err := time.ParseInLocation("2006-01-02", externalID[:10], time.UTC); err == nil {
		c.startedAt = start
	} else {
		c.startedAt = time.Now().UTC().Truncate(24 * time.Hour)
	}

	var text string
	if raw, err := os.ReadFile(filepath.Join(dir, "README.md")); err == nil {
		text = string(raw)
	} else {
		warn("card %s: README.md missing — indexing from dir name only", externalID)
	}

	if m := titleRe.FindStringSubmatch(text); m != nil {
		c.title = m[1]
	}
	if m := goalRe.FindStringSubmatch(text); m != nil {
		c.goal = m[1]
	}
	if m := statusRe.FindStringSubmatch(text); m != nil {
		switch v := strings.ToLower(strings.TrimSpace(m[1])); {
		case strings.HasPrefix(v, "done"), strings.Contains(v, "archived"):
			c.status = "done"
		case strings.HasPrefix(v, "active"):
			c.status = "running"
		}
	}

	if c.archived {
		c.status = "done"
		var when time.Time
		if m := doneDateRe.FindStringSubmatch(text); m != nil {
			when, _ = time.ParseInLocation("2006-01-02", m[1], time.UTC)
			when = when.Add(24*time.Hour - time.Second) // end of that day
		} else if m := archDateRe.FindStringSubmatch(text); m != nil {
			when, _ = time.ParseInLocation("2006-01-02", m[1], time.UTC)
			when = when.Add(24*time.Hour - time.Second)
		} else if fi, err := os.Stat(dir); err == nil {
			when = fi.ModTime().UTC() // probe fallback: dir mtime
			warn("card %s: archived without a completion date — using dir mtime %s", externalID, when.Format("2006-01-02"))
		} else {
			when = time.Now().UTC()
		}
		c.archivedAt = &when
	}
	return c
}

// explicitRefs extracts uuid-shaped session references from logs/sessions.md
// (2nd table column, tolerant of prose around the id). Junk values — 5-digit
// ids, "phase-3", dates — never match.
func explicitRefs(dir string) []string {
	raw, err := os.ReadFile(filepath.Join(dir, "logs", "sessions.md"))
	if err != nil {
		return nil // probe finding: some cards have no sessions.md — fine
	}
	seen := map[string]bool{}
	var refs []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cols := strings.Split(strings.Trim(line, "|"), "|")
		if len(cols) < 2 {
			continue
		}
		cell := strings.ToLower(strings.TrimSpace(cols[1]))
		if cell == "" || cell == "сесія" || strings.HasPrefix(cell, "---") {
			continue
		}
		for _, tok := range append(uuidRe.FindAllString(cell, -1), hex8Re.FindAllString(cell, -1)...) {
			if !seen[tok] {
				seen[tok] = true
				refs = append(refs, tok)
			}
		}
	}
	return refs
}

// ─── the scan pass ──────────────────────────────────────────────────────────

type sessionRow struct {
	id      int64
	uuid    string
	cwdNorm string
	start   time.Time
	end     time.Time
	ok      bool // started_at parsed
}

// Scan performs one full idempotent pass: workspaces → tasks → links.
// READ-ONLY on disk; every DB write is an upsert (no dupes on re-scan).
func (s *Scanner) Scan() (Stats, error) {
	var stats Stats
	warn := func(format string, args ...any) {
		stats.Warnings++
		log.Printf("warn: wsingest: "+format, args...)
	}

	root := s.cfg.WorkspaceRoot
	entries, err := os.ReadDir(root)
	if err != nil {
		return stats, fmt.Errorf("workspace root: %w", err)
	}

	projects, err := s.loadProjects()
	if err != nil {
		return stats, err
	}
	sessions, err := s.loadSessions()
	if err != nil {
		return stats, err
	}

	now := time.Now().UTC()
	var workspaces []*workspace
	type taskCard struct {
		taskID int64
		card   card
		wsIdx  int
	}
	var cards []taskCard

	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), "_") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		slug := e.Name()
		wsDir := filepath.Join(root, slug)
		if fi, err := os.Stat(filepath.Join(wsDir, "workspace")); err != nil || !fi.IsDir() {
			continue // not a workspace namespace (stray dir)
		}

		code, display := resolveCodePath(wsDir, slug, projects, warn)
		ws := &workspace{slug: slug, rootPath: wsDir, codePath: code, displayName: display}
		if code != "" {
			cn := normPath(code)
			for _, pr := range projects {
				if pr.norm == cn {
					ws.projectID = sql.NullInt64{Int64: pr.id, Valid: true}
					break
				}
			}
		}
		if err := s.upsertWorkspace(ws, now); err != nil {
			warn("workspace %s: upsert failed: %v", slug, err)
			continue
		}
		wsIdx := len(workspaces)
		workspaces = append(workspaces, ws)

		for _, zone := range []string{"working", "archive"} {
			base := filepath.Join(wsDir, "workspace", zone)
			dirs, _ := filepath.Glob(filepath.Join(base, "[0-9][0-9][0-9][0-9]", "[0-9][0-9]", "[0-9][0-9]", "*"))
			sort.Strings(dirs)
			for _, dir := range dirs {
				if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
					continue
				}
				rel, err := filepath.Rel(base, filepath.Dir(dir))
				if err != nil || !datePartRe.MatchString(filepath.ToSlash(rel)) {
					continue
				}
				externalID := strings.ReplaceAll(filepath.ToSlash(rel), "/", "-") + "-" + filepath.Base(dir)
				c := parseCard(dir, zone, externalID, warn)
				taskID, err := s.upsertTask(ws, c, now)
				if err != nil {
					warn("card %s: upsert failed: %v", externalID, err)
					continue
				}
				cards = append(cards, taskCard{taskID: taskID, card: c, wsIdx: wsIdx})
				// phase 2: retro artifacts (09-retrospective / ORCHESTRATION /
				// agents.md ledger) — hash-gated, tolerant, never fails the scan.
				s.scanArtifacts(taskID, dir, warn)
				// fusion phase 10: a plan/ dir makes this task an epic — parse its
				// README phase table + phase docs into epic_phases (same hash-gated,
				// tolerant contract).
				s.scanEpics(taskID, dir, warn)
			}
		}
	}
	stats.Workspaces = len(workspaces)

	// Explicit links: logs/sessions.md uuid refs → task_sessions('explicit').
	explicitSessions := map[int64]bool{}
	for _, tc := range cards {
		for _, ref := range explicitRefs(tc.card.dir) {
			for _, sess := range sessions {
				if sess.uuid == ref || (len(ref) >= 8 && strings.HasPrefix(sess.uuid, ref)) {
					if err := s.upsertLink(tc.taskID, sess.id, "explicit", nil); err != nil {
						warn("explicit link %s→%s: %v", tc.card.externalID, sess.uuid, err)
						continue
					}
					explicitSessions[sess.id] = true
				}
			}
		}
	}

	// Heuristic links: cwd under the workspace code_path AND time overlap with
	// the card's [start, archived|now] window → best card per session, with
	// confidence = overlap fraction of the session duration.
	for _, sess := range sessions {
		if !sess.ok || sess.cwdNorm == "" || explicitSessions[sess.id] {
			continue
		}
		var bestTask int64
		var bestStart time.Time
		best := 0.0
		for _, tc := range cards {
			cp := normPath(workspaces[tc.wsIdx].codePath)
			if cp == "" || (sess.cwdNorm != cp && !strings.HasPrefix(sess.cwdNorm, cp+"/")) {
				continue
			}
			cardEnd := now
			if tc.card.archivedAt != nil {
				cardEnd = *tc.card.archivedAt
			}
			overlap := minTime(sess.end, cardEnd).Sub(maxTime(sess.start, tc.card.startedAt)).Seconds()
			if overlap <= 0 {
				continue
			}
			ratio := 1.0
			if dur := sess.end.Sub(sess.start).Seconds(); dur > 0 {
				ratio = overlap / dur
			}
			// Tie-break equal overlap (several open cards fully covering the
			// session) toward the most recently started card — the session
			// almost surely belongs to the newest in-flight task.
			if ratio > best || (ratio == best && tc.card.startedAt.After(bestStart)) {
				best, bestTask, bestStart = ratio, tc.taskID, tc.card.startedAt
			}
		}
		if bestTask != 0 {
			conf := best
			if err := s.upsertLink(bestTask, sess.id, "heuristic", &conf); err != nil {
				warn("heuristic link task#%d→session#%d: %v", bestTask, sess.id, err)
			}
		}
	}

	if err := s.db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE source = 'workspace'`).Scan(&stats.Tasks); err != nil {
		return stats, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM task_sessions WHERE link_source = 'explicit'`).Scan(&stats.Explicit); err != nil {
		return stats, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM task_sessions WHERE link_source = 'heuristic'`).Scan(&stats.Heuristic); err != nil {
		return stats, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM task_retros`).Scan(&stats.Retros); err != nil {
		return stats, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM task_loops`).Scan(&stats.Loops); err != nil {
		return stats, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM task_delegations`).Scan(&stats.Delegations); err != nil {
		return stats, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM epic_phases`).Scan(&stats.EpicPhases); err != nil {
		return stats, err
	}
	return stats, nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// ─── DB plumbing (all writes are converging upserts) ────────────────────────

func (s *Scanner) loadProjects() ([]projectRow, error) {
	rows, err := s.db.Query(`SELECT id, path FROM projects`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []projectRow
	for rows.Next() {
		var pr projectRow
		if err := rows.Scan(&pr.id, &pr.path); err != nil {
			return nil, err
		}
		pr.norm = normPath(pr.path)
		out = append(out, pr)
	}
	return out, rows.Err()
}

func (s *Scanner) loadSessions() ([]sessionRow, error) {
	rows, err := s.db.Query(`SELECT id, session_uuid, cwd, started_at, ended_at FROM sessions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := time.Now().UTC()
	var out []sessionRow
	for rows.Next() {
		var (
			sr           sessionRow
			cwd, endedAt sql.NullString
			startedAt    string
		)
		if err := rows.Scan(&sr.id, &sr.uuid, &cwd, &startedAt, &endedAt); err != nil {
			return nil, err
		}
		sr.uuid = strings.ToLower(sr.uuid)
		if cwd.Valid {
			sr.cwdNorm = normPath(cwd.String)
		}
		if t, err := time.Parse(time.RFC3339, startedAt); err == nil {
			sr.start, sr.ok = t.UTC(), true
		}
		sr.end = now
		if endedAt.Valid {
			if t, err := time.Parse(time.RFC3339, endedAt.String); err == nil {
				sr.end = t.UTC()
			}
		}
		if sr.end.Before(sr.start) {
			sr.end = sr.start
		}
		out = append(out, sr)
	}
	return out, rows.Err()
}

func (s *Scanner) upsertWorkspace(ws *workspace, now time.Time) error {
	_, err := s.db.Exec(`
		INSERT INTO workspaces (slug, root_path, code_path, project_id, display_name, last_scanned)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(slug) DO UPDATE SET
			root_path    = excluded.root_path,
			code_path    = excluded.code_path,
			project_id   = COALESCE(excluded.project_id, workspaces.project_id),
			display_name = COALESCE(excluded.display_name, workspaces.display_name),
			last_scanned = excluded.last_scanned`,
		ws.slug, ws.rootPath, nullStr(ws.codePath), ws.projectID, nullStr(ws.displayName), now.Format(time.RFC3339))
	if err != nil {
		return err
	}
	return s.db.QueryRow(`SELECT id, project_id FROM workspaces WHERE slug = ?`, ws.slug).
		Scan(&ws.id, &ws.projectID)
}

// taskProjectID returns the project row for a workspace's tasks, creating a
// registry entry when telemetry has never seen the project (tasks.project_id
// is NOT NULL) — a DB-only write, the workspace itself is untouched.
func (s *Scanner) taskProjectID(ws *workspace, now time.Time) (int64, error) {
	if ws.projectID.Valid {
		return ws.projectID.Int64, nil
	}
	path := ws.codePath
	if path == "" {
		path = ws.rootPath
	}
	name := ws.displayName
	if name == "" {
		name = ws.slug
	}
	if _, err := s.db.Exec(`
		INSERT INTO projects (path, slug, name, first_seen)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(path) DO NOTHING`,
		path, ws.slug, name, now.Format(time.RFC3339)); err != nil {
		return 0, err
	}
	var id int64
	if err := s.db.QueryRow(`SELECT id FROM projects WHERE path = ?`, path).Scan(&id); err != nil {
		return 0, err
	}
	ws.projectID = sql.NullInt64{Int64: id, Valid: true}
	_, err := s.db.Exec(`UPDATE workspaces SET project_id = ? WHERE id = ?`, id, ws.id)
	return id, err
}

func (s *Scanner) upsertTask(ws *workspace, c card, now time.Time) (int64, error) {
	projectID, err := s.taskProjectID(ws, now)
	if err != nil {
		return 0, err
	}
	var archivedAt, finishedAt any
	if c.archivedAt != nil {
		ts := c.archivedAt.Format(time.RFC3339)
		archivedAt, finishedAt = ts, ts
	}
	started := c.startedAt.Format(time.RFC3339)
	_, err = s.db.Exec(`
		INSERT INTO tasks (project_id, title, prompt, status, created_at, started_at, finished_at,
		                   source, external_id, workspace_id, archived_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'workspace', ?, ?, ?)
		ON CONFLICT(workspace_id, external_id) WHERE workspace_id IS NOT NULL DO UPDATE SET
			project_id  = excluded.project_id,
			title       = excluded.title,
			prompt      = excluded.prompt,
			status      = excluded.status,
			finished_at = excluded.finished_at,
			archived_at = excluded.archived_at`,
		projectID, c.title, c.goal, c.status, started, started, finishedAt,
		c.externalID, ws.id, archivedAt)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.db.QueryRow(`SELECT id FROM tasks WHERE workspace_id = ? AND external_id = ?`,
		ws.id, c.externalID).Scan(&id)
	return id, err
}

// upsertLink writes one task↔session link. Explicit always wins: a heuristic
// upsert never downgrades an existing explicit row, while an explicit upsert
// upgrades a heuristic one.
func (s *Scanner) upsertLink(taskID, sessionID int64, source string, confidence *float64) error {
	_, err := s.db.Exec(`
		INSERT INTO task_sessions (task_id, session_id, link_source, confidence)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(task_id, session_id) DO UPDATE SET
			link_source = CASE WHEN task_sessions.link_source = 'explicit'
			                   THEN 'explicit' ELSE excluded.link_source END,
			confidence  = CASE WHEN task_sessions.link_source = 'explicit'
			                   THEN task_sessions.confidence ELSE excluded.confidence END`,
		taskID, sessionID, source, confidence)
	return err
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
