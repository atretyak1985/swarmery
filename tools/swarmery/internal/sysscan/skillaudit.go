package sysscan

// Skill audit (verify-not-trust phase 18, D16/D35): the advisory lints that keep
// the estate from growing back to 42 agents.
//
//	agent_dead               info  no subagent run of this agent in the window
//	skill_dead               info  no use of this skill in the window
//	skill_desc_similar       warn  two skill descriptions with token Jaccard >= the guards threshold
//	session_skill_load_high  warn  a project whose p95 of distinct skills per session exceeds the guards limit
//
// Attribution is by NAME. The ingester never fills events.agent_id or
// events.skill_id (internal/api/system.go usageByName says the same), so a
// FK-only join calls every component dead. A component counts as used when an
// event in the window names it — subagent_start.subagent_type for agents, the
// Skill tool's input.skill or a slash-command prompt's <command-name> for skills
// — folded to the segment after the last ':' and lower-cased, so
// "core:workspace-plans" and "workspace-plans" are one skill. The FK columns
// still count when set, so an ingest that starts filling them needs no change
// here. Name grain means same-named copies in different projects share usage.
//
// Report and advice only: nothing here deletes, merges or edits a component.

import (
	"database/sql"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Skill-audit rule names, synced by Lint alongside the registry rules.
const (
	RuleSkillDead            = "skill_dead"              // info: no use of the skill in the window (advisory, name-matched)
	RuleSkillDescSimilar     = "skill_desc_similar"      // warn: a pair of skill descriptions at or above the similarity threshold
	RuleSessionSkillLoadHigh = "session_skill_load_high" // warn: a project's p95 of distinct skills per session above the limit
)

// AuditWindowDays is the telemetry window every usage rule looks back over.
const AuditWindowDays = 30

// Target prefixes of the two rules that do not anchor on one registry row. A
// pair needs both ids in the target, or two pairs sharing a skill would
// collapse into one (target, rule) row.
const (
	SkillPairTargetPrefix = "skill_pair:"
	ProjectTargetPrefix   = "project:"
)

// The weekly report (config/routines/skill-audit.json) lands in the framework's
// OWN workspace namespace, never a consumer project's:
// <workspace root>/swarmery/workspace/retro/skill-audit-<YYYY-MM-DD>.md.
const (
	SkillAuditProject      = "swarmery"
	SkillAuditReportPrefix = "skill-audit-"
)

// SkillAuditSections are the five level-2 headings every report carries, in
// order. The routine's write step refuses a report missing any of them.
var SkillAuditSections = []string{
	"Unused agents",
	"Unused skills",
	"Similar descriptions",
	"Skills per session",
	"Before a new agent, extend one of these",
}

// SkillAuditReportDir is where the weekly reports live under a workspace root.
func SkillAuditReportDir(workspaceRoot string) string {
	return filepath.Join(workspaceRoot, SkillAuditProject, "workspace", "retro")
}

// LatestSkillAuditReport returns the newest report in dir and its date — the
// file name carries an ISO date, so the lexically last name is the newest.
// No report yet (or no dir) is ok=false, not an error.
func LatestSkillAuditReport(dir string) (path, date string, ok bool, err error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, SkillAuditReportPrefix) || !strings.HasSuffix(name, ".md") {
			continue
		}
		d := strings.TrimSuffix(strings.TrimPrefix(name, SkillAuditReportPrefix), ".md")
		if _, perr := time.Parse("2006-01-02", d); perr != nil {
			continue
		}
		if d > date {
			path, date, ok = filepath.Join(dir, name), d, true
		}
	}
	return path, date, ok, nil
}

// AuditOptions are the knobs of one audit. Zero values take the defaults:
// AuditWindowDays and time.Now(). The two thresholds have no default here —
// they come from guards.Load() (the caller resolves them), and a zero
// threshold disables its rule rather than inventing a value.
type AuditOptions struct {
	WindowDays          int
	SimilarityThreshold float64
	SessionSkillLoadP95 int
	Now                 time.Time
}

// AuditComponent is one agent or skill in the audit report.
type AuditComponent struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Scope       string  `json:"scope"`
	ProjectSlug *string `json:"projectSlug"`
	Path        string  `json:"path"`
}

// SimilarPair is two skills whose descriptions overlap at or above the
// threshold. A sorts before B by name, so a pair is reported once.
type SimilarPair struct {
	A          AuditComponent `json:"a"`
	B          AuditComponent `json:"b"`
	Similarity float64        `json:"similarity"` // token Jaccard, rounded to 2 places
}

// SkillUse is one registered skill that WAS used in the window: the distinct
// sessions that loaded it. It is the pool the report's "extend one of these"
// advice draws from.
type SkillUse struct {
	Name     string `json:"name"` // folded name (segment after the last ':')
	Sessions int    `json:"sessions"`
}

// ProjectSkillLoad is one project's row of the skills-per-session table.
// Sessions counts only sessions that loaded at least one skill in the window —
// a session that loaded none says nothing about skill-load bloat.
type ProjectSkillLoad struct {
	ProjectID int64  `json:"projectId"`
	Slug      string `json:"slug"`
	Sessions  int    `json:"sessions"`
	P95       int    `json:"p95"`
	Max       int    `json:"max"`
	OverLimit bool   `json:"overLimit"`
}

// SkillAudit is the whole report: what `swarmery skill-audit --json` prints and
// what the linter turns into findings.
type SkillAudit struct {
	GeneratedAt         string             `json:"generatedAt"`
	WindowDays          int                `json:"windowDays"`
	Since               string             `json:"since"`
	SimilarityThreshold float64            `json:"similarityThreshold"`
	SessionSkillLoadP95 int                `json:"sessionSkillLoadP95"`
	UnusedAgents        []AuditComponent   `json:"unusedAgents"`
	UnusedSkills        []AuditComponent   `json:"unusedSkills"`
	UsedSkills          []SkillUse         `json:"usedSkills"`
	SimilarPairs        []SimilarPair      `json:"similarPairs"`
	SkillLoad           []ProjectSkillLoad `json:"skillLoad"`
}

// auditStopwords are dropped before the Jaccard comparison: function words and
// the boilerplate every skill description shares ("Use this skill when …"),
// which would otherwise make unrelated skills look alike.
var auditStopwords = map[string]bool{
	"a": true, "an": true, "and": true, "any": true, "are": true, "as": true, "at": true,
	"be": true, "but": true, "by": true, "do": true, "don": true, "for": true, "from": true,
	"if": true, "in": true, "into": true, "is": true, "it": true, "its": true, "not": true,
	"of": true, "on": true, "or": true, "should": true, "skill": true, "that": true,
	"the": true, "this": true, "to": true, "use": true, "used": true, "using": true,
	"via": true, "when": true, "with": true, "you": true, "your": true,
}

// slashCommandName pulls the invoked name out of a slash-command prompt. Only a
// prompt that STARTS with the command block counts — a prompt that merely
// quotes the tag in prose is not an invocation.
var (
	slashCommandPrompt = regexp.MustCompile(`^\s*<command-(?:message|name)>`)
	slashCommandName   = regexp.MustCompile(`<command-name>/?([^<\s]+)</command-name>`)
)

// normComponent folds "core:tech-lead" and "Tech-Lead" to "tech-lead" — the
// segment after the last ':', lower-cased. Twin of internal/api normAgentType
// plus the lower-case fold internal/advisor normAgent applies.
func normComponent(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.LastIndexByte(name, ':'); i >= 0 {
		name = name[i+1:]
	}
	return strings.ToLower(name)
}

// descTokens is the lower-cased word set of a description minus stopwords and
// one-rune tokens.
func descTokens(desc string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(desc), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len([]rune(w)) < 2 || auditStopwords[w] {
			continue
		}
		out[w] = true
	}
	return out
}

// jaccard is |a∩b| / |a∪b|; two empty sets compare as 0, never as identical.
func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for w := range a {
		if b[w] {
			inter++
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}

// p95 is the nearest-rank 95th percentile of counts (sorted in place).
func p95(counts []int) int {
	if len(counts) == 0 {
		return 0
	}
	sort.Ints(counts)
	rank := int(math.Ceil(0.95 * float64(len(counts))))
	if rank < 1 {
		rank = 1
	}
	return counts[rank-1]
}

// usage is what the window's events say was used.
type usage struct {
	agentIDs   map[int64]bool
	agentNames map[string]bool
	skillIDs   map[int64]bool
	skillNames map[string]bool
	// perSession is the normalised skill names each session loaded, keyed by
	// session id; sessionProject maps the session to its project.
	perSession     map[int64]map[string]bool
	sessionProject map[int64]int64
}

// ComputeSkillAudit reads the registry and the window's events and returns the
// report. Read-only: it issues SELECTs alone, so it is safe against a live
// daemon's database or a read-only copy. Each query is drained and closed
// before the next one starts — the store runs a single connection.
func ComputeSkillAudit(db *sql.DB, opts AuditOptions) (SkillAudit, error) {
	if opts.WindowDays <= 0 {
		opts.WindowDays = AuditWindowDays
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	since := opts.Now.UTC().AddDate(0, 0, -opts.WindowDays).Format(time.RFC3339)
	rep := SkillAudit{
		GeneratedAt:         opts.Now.UTC().Format(time.RFC3339),
		WindowDays:          opts.WindowDays,
		Since:               since,
		SimilarityThreshold: opts.SimilarityThreshold,
		SessionSkillLoadP95: opts.SessionSkillLoadP95,
		UnusedAgents:        []AuditComponent{},
		UnusedSkills:        []AuditComponent{},
		UsedSkills:          []SkillUse{},
		SimilarPairs:        []SimilarPair{},
		SkillLoad:           []ProjectSkillLoad{},
	}

	u, err := readUsage(db, since)
	if err != nil {
		return rep, err
	}
	agents, err := readComponents(db, "agents", "file_path")
	if err != nil {
		return rep, err
	}
	skills, err := readComponents(db, "skills", "dir_path")
	if err != nil {
		return rep, err
	}
	// Slash prompts name commands as well as skills; only names that ARE a
	// registered skill count towards a session's skill load.
	skillNameSet := map[string]bool{}
	for _, s := range skills {
		skillNameSet[normComponent(s.Name)] = true
	}

	for _, a := range agents {
		if !u.agentIDs[a.ID] && !u.agentNames[normComponent(a.Name)] {
			rep.UnusedAgents = append(rep.UnusedAgents, a.AuditComponent)
		}
	}
	for _, s := range skills {
		if !u.skillIDs[s.ID] && !u.skillNames[normComponent(s.Name)] {
			rep.UnusedSkills = append(rep.UnusedSkills, s.AuditComponent)
		}
	}
	rep.UsedSkills = usedSkills(u, skillNameSet)
	if opts.SimilarityThreshold > 0 {
		rep.SimilarPairs = similarPairs(skills, opts.SimilarityThreshold)
	}
	rep.SkillLoad, err = skillLoad(db, u, skillNameSet, opts.SessionSkillLoadP95)
	return rep, err
}

// readUsage folds the window's run events into used ids, used names and the
// per-session skill sets. json_valid guards json_extract: one malformed payload
// must not fail the whole audit. A user prompt is read only when it opens with
// a command block, and only its first 512 bytes — prompts can be tens of KB and
// this runs on every lint pass.
func readUsage(db *sql.DB, since string) (usage, error) {
	u := usage{
		agentIDs: map[int64]bool{}, agentNames: map[string]bool{},
		skillIDs: map[int64]bool{}, skillNames: map[string]bool{},
		perSession: map[int64]map[string]bool{}, sessionProject: map[int64]int64{},
	}
	rows, err := db.Query(`
		SELECT type, agent_id, skill_id, session_id, project_id, name FROM (
		  SELECT e.type, e.agent_id, e.skill_id, e.session_id, s.project_id,
		         CASE WHEN json_valid(e.payload) THEN
		           CASE e.type
		             WHEN 'subagent_start' THEN json_extract(e.payload, '$.subagent_type')
		             WHEN 'skill_use'      THEN json_extract(e.payload, '$.input.skill')
		             WHEN 'user_prompt'    THEN substr(ltrim(json_extract(e.payload, '$.content'), char(32, 9, 10, 13)), 1, 512)
		           END
		         END AS name
		    FROM events e JOIN sessions s ON s.id = e.session_id
		   WHERE e.type IN ('subagent_start', 'skill_use', 'user_prompt') AND e.ts >= ?
		) WHERE type <> 'user_prompt' OR name LIKE '<command-%'`, since)
	if err != nil {
		return u, err
	}
	defer rows.Close()
	for rows.Next() {
		var typ string
		var agentID, skillID sql.NullInt64
		var sessionID, projectID int64
		var text sql.NullString
		if err := rows.Scan(&typ, &agentID, &skillID, &sessionID, &projectID, &text); err != nil {
			return u, err
		}
		switch typ {
		case "subagent_start":
			if agentID.Valid {
				u.agentIDs[agentID.Int64] = true
			}
			if n := normComponent(text.String); n != "" {
				u.agentNames[n] = true
			}
		case "skill_use":
			if skillID.Valid {
				u.skillIDs[skillID.Int64] = true
			}
			if n := normComponent(text.String); n != "" {
				u.skillNames[n] = true
				u.load(sessionID, projectID, n)
			}
		case "user_prompt":
			if !slashCommandPrompt.MatchString(text.String) {
				continue
			}
			m := slashCommandName.FindStringSubmatch(text.String)
			if m == nil {
				continue
			}
			if n := normComponent(m[1]); n != "" {
				// Recorded as a candidate: skillLoad keeps only registered skills.
				u.skillNames[n] = true
				u.load(sessionID, projectID, "/"+n)
			}
		}
	}
	return u, rows.Err()
}

// load records that a session loaded a skill. Slash names carry a leading "/"
// until skillLoad checks them against the registry.
func (u usage) load(sessionID, projectID int64, name string) {
	set := u.perSession[sessionID]
	if set == nil {
		set = map[string]bool{}
		u.perSession[sessionID] = set
		u.sessionProject[sessionID] = projectID
	}
	set[name] = true
}

// component is a registry row plus the description the similarity rule reads.
type component struct {
	AuditComponent
	desc string
}

func readComponents(db *sql.DB, table, pathCol string) ([]component, error) {
	rows, err := db.Query(`
		SELECT t.id, t.name, t.scope, p.slug, t.` + pathCol + `, COALESCE(t.description, '')
		  FROM ` + table + ` t LEFT JOIN projects p ON p.id = t.project_id
		 WHERE t.deleted = 0 ORDER BY t.name, t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []component
	for rows.Next() {
		var c component
		if err := rows.Scan(&c.ID, &c.Name, &c.Scope, &c.ProjectSlug, &c.Path, &c.desc); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// similarPairs compares every two live skills (already ordered by name, id).
// Copies of ONE skill — same folded name in another scope or plugin — are not
// a pair: the promotion and stale-override insights own those. A folded name
// pair is reported once, on its first (name, id)-ordered occurrence.
func similarPairs(skills []component, threshold float64) []SimilarPair {
	tokens := make([]map[string]bool, len(skills))
	for i, s := range skills {
		tokens[i] = descTokens(s.desc)
	}
	seen := map[[2]string]bool{}
	out := []SimilarPair{}
	for i := 0; i < len(skills); i++ {
		ni := normComponent(skills[i].Name)
		for j := i + 1; j < len(skills); j++ {
			nj := normComponent(skills[j].Name)
			if ni == nj {
				continue
			}
			key := [2]string{ni, nj}
			if nj < ni {
				key = [2]string{nj, ni}
			}
			if seen[key] {
				continue
			}
			sim := jaccard(tokens[i], tokens[j])
			if sim < threshold {
				continue
			}
			seen[key] = true
			out = append(out, SimilarPair{
				A: skills[i].AuditComponent, B: skills[j].AuditComponent,
				Similarity: math.Round(sim*100) / 100,
			})
		}
	}
	return out
}

// sessionSkills is the registered skills one session loaded: Skill tool names
// as recorded, slash names ("/x") only when x is a registered skill.
func sessionSkills(set map[string]bool, skillNames map[string]bool) map[string]bool {
	out := make(map[string]bool, len(set))
	for name := range set {
		if strings.HasPrefix(name, "/") {
			if bare := name[1:]; skillNames[bare] {
				out[bare] = true
			}
			continue
		}
		out[name] = true
	}
	return out
}

// usedSkills counts, per registered skill, the distinct sessions that loaded
// it — most-used first, then by name.
func usedSkills(u usage, skillNames map[string]bool) []SkillUse {
	per := map[string]int{}
	for _, set := range u.perSession {
		for name := range sessionSkills(set, skillNames) {
			if skillNames[name] {
				per[name]++
			}
		}
	}
	out := make([]SkillUse, 0, len(per))
	for name, n := range per {
		out = append(out, SkillUse{Name: name, Sessions: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Sessions != out[j].Sessions {
			return out[i].Sessions > out[j].Sessions
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// skillLoad builds the per-project p95 table, one row per project that had at
// least one skill-loading session in the window, ordered by p95 then slug.
func skillLoad(db *sql.DB, u usage, skillNames map[string]bool, limit int) ([]ProjectSkillLoad, error) {
	perProject := map[int64][]int{}
	for sid, set := range u.perSession {
		// Skill tool names count as recorded (a plugin skill the registry has
		// not scanned is still a loaded skill); a slash name counts only when
		// it is a registered skill — otherwise it was a command.
		if n := len(sessionSkills(set, skillNames)); n > 0 {
			perProject[u.sessionProject[sid]] = append(perProject[u.sessionProject[sid]], n)
		}
	}
	slugs, err := projectSlugs(db)
	if err != nil {
		return nil, err
	}
	out := []ProjectSkillLoad{}
	for pid, counts := range perProject {
		row := ProjectSkillLoad{ProjectID: pid, Slug: slugs[pid], Sessions: len(counts), P95: p95(counts)}
		row.Max = counts[len(counts)-1] // p95 sorted them
		row.OverLimit = limit > 0 && row.P95 > limit
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].P95 != out[j].P95 {
			return out[i].P95 > out[j].P95
		}
		return out[i].Slug < out[j].Slug
	})
	return out, nil
}

func projectSlugs(db *sql.DB) (map[int64]string, error) {
	rows, err := db.Query(`SELECT id, slug FROM projects`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var slug string
		if err := rows.Scan(&id, &slug); err != nil {
			return nil, err
		}
		out[id] = slug
	}
	return out, rows.Err()
}

// auditFindings turns a report into lint findings, per rule. A rule whose
// threshold is unset (zero) is absent from the map, so Lint leaves its rows
// untouched instead of resolving them on a pass that did not evaluate it.
func auditFindings(rep SkillAudit) map[string][]lintFinding {
	window := rep.WindowDays
	byRule := map[string][]lintFinding{
		RuleAgentDead: {},
		RuleSkillDead: {},
	}
	for _, a := range rep.UnusedAgents {
		byRule[RuleAgentDead] = append(byRule[RuleAgentDead], lintFinding{
			target:   fmt.Sprintf("agent:%d", a.ID),
			severity: "info",
			message: fmt.Sprintf("agent %q: no subagent run named it in the last %d days (matched by name — events.agent_id is not populated; advisory)",
				a.Name, window),
		})
	}
	for _, s := range rep.UnusedSkills {
		byRule[RuleSkillDead] = append(byRule[RuleSkillDead], lintFinding{
			target:   fmt.Sprintf("skill:%d", s.ID),
			severity: "info",
			message: fmt.Sprintf("skill %q: no Skill tool call or slash command named it in the last %d days (matched by name — events.skill_id is not populated; advisory)",
				s.Name, window),
		})
	}
	if rep.SimilarityThreshold > 0 {
		byRule[RuleSkillDescSimilar] = []lintFinding{}
		for _, p := range rep.SimilarPairs {
			byRule[RuleSkillDescSimilar] = append(byRule[RuleSkillDescSimilar], lintFinding{
				target:   fmt.Sprintf("%s%d:%d", SkillPairTargetPrefix, p.A.ID, p.B.ID),
				severity: "warn",
				message: fmt.Sprintf("skills %q and %q: descriptions overlap %.2f (token Jaccard, threshold %.2f) — extend one instead of keeping both",
					p.A.Name, p.B.Name, p.Similarity, rep.SimilarityThreshold),
			})
		}
	}
	if rep.SessionSkillLoadP95 > 0 {
		byRule[RuleSessionSkillLoadHigh] = []lintFinding{}
		for _, l := range rep.SkillLoad {
			if !l.OverLimit {
				continue
			}
			byRule[RuleSessionSkillLoadHigh] = append(byRule[RuleSessionSkillLoadHigh], lintFinding{
				target:   fmt.Sprintf("%s%d", ProjectTargetPrefix, l.ProjectID),
				severity: "warn",
				message: fmt.Sprintf("project %q: p95 of %d distinct skills per session over the last %d days (%d skill-loading sessions, max %d) exceeds the limit of %d",
					l.Slug, l.P95, window, l.Sessions, l.Max, rep.SessionSkillLoadP95),
			})
		}
	}
	return byRule
}
