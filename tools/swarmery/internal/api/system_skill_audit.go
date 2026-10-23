package api

// verify-not-trust phase 18: the skill audit in the System hub.
//
//   - systemInsightsDTO.SkillAudit — the ACTIVE skill_desc_similar and
//     session_skill_load_high findings (sysscan/skillaudit.go), reframed like
//     the dead list: one source of truth, no second similarity pass here.
//     (skill_dead rides the dead list next to agent_dead.)
//   - systemInsightsDTO.SkillAuditReport — the newest weekly report the
//     skill-audit routine wrote (config/routines/skill-audit.json).
//   - GET /api/system/skill-audit/report — that report's markdown, for the
//     Insights section and the Agents-tab "extend a skill first" hint.
//
// Read-only and display-only, like every insight: nothing here deletes or
// merges a component.

import (
	"database/sql"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/sysscan"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// workspaceRoot is the workspace repo the serve path scans (its
// --workspace-root); "" falls back to the same env resolution wsingest uses.
var workspaceRoot string

// AttachWorkspaceRoot wires the daemon's workspace root into the endpoints that
// read workspace files (the latest skill-audit report).
func AttachWorkspaceRoot(root string) { workspaceRoot = root }

func skillAuditReportDir() string {
	root := workspaceRoot
	if root == "" {
		root = wsingest.Root()
	}
	return sysscan.SkillAuditReportDir(root)
}

// skillAuditFindingDTO is one active skill_desc_similar or
// session_skill_load_high finding.
type skillAuditFindingDTO struct {
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	// Skills names the pair (skill_desc_similar), ordered as the linter
	// ordered them; empty for a project finding.
	Skills []string `json:"skills"`
	// ProjectSlug is the flagged project (session_skill_load_high); null for a pair.
	ProjectSlug *string `json:"projectSlug"`
}

// skillAuditReportDTO is the newest weekly report on disk.
type skillAuditReportDTO struct {
	Date string `json:"date"` // YYYY-MM-DD from the file name
	Path string `json:"path"`
}

// skillAuditReportBodyDTO is GET /api/system/skill-audit/report.
type skillAuditReportBodyDTO struct {
	skillAuditReportDTO
	Markdown string `json:"markdown"` // redacted
}

// skillAuditFindings lists the active pair and project findings. A project
// scope keeps a pair only when BOTH skills are effective there (only then do
// they compete for the same trigger) and a load finding only for that project.
// The findings are drained before the skill names are read: the store runs one
// connection.
func skillAuditFindings(db *sql.DB, esc *effectiveScope) ([]skillAuditFindingDTO, error) {
	type row struct{ target, rule, severity, message string }
	rows, err := db.Query(`
		SELECT target, rule, severity, message FROM config_lint_findings
		WHERE resolved_at IS NULL AND rule IN (?, ?)
		ORDER BY rule, target`, sysscan.RuleSkillDescSimilar, sysscan.RuleSessionSkillLoadHigh)
	if err != nil {
		return nil, err
	}
	var found []row
	for rows.Next() {
		var f row
		if err := rows.Scan(&f.target, &f.rule, &f.severity, &f.message); err != nil {
			rows.Close()
			return nil, err
		}
		found = append(found, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []skillAuditFindingDTO{}
	if len(found) == 0 {
		return out, nil
	}

	skills, err := effectiveSkillNames(db, esc)
	if err != nil {
		return nil, err
	}
	slugs, err := projectSlugsByID(db)
	if err != nil {
		return nil, err
	}
	for _, f := range found {
		d := skillAuditFindingDTO{Rule: f.rule, Severity: f.severity, Message: f.message, Skills: []string{}}
		switch f.rule {
		case sysscan.RuleSkillDescSimilar:
			a, b, ok := pairIDs(f.target)
			na, okA := skills[a]
			nb, okB := skills[b]
			if !ok || !okA || !okB {
				continue
			}
			d.Skills = []string{na, nb}
		case sysscan.RuleSessionSkillLoadHigh:
			id, err := strconv.ParseInt(strings.TrimPrefix(f.target, sysscan.ProjectTargetPrefix), 10, 64)
			if err != nil || (esc.scoped() && id != esc.projectID) {
				continue
			}
			if slug, ok := slugs[id]; ok {
				d.ProjectSlug = &slug
			}
		}
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Rule > out[j].Rule }) // similar pairs first
	return out, nil
}

// pairIDs parses "skill_pair:<a>:<b>".
func pairIDs(target string) (int64, int64, bool) {
	rest, ok := strings.CutPrefix(target, sysscan.SkillPairTargetPrefix)
	if !ok {
		return 0, 0, false
	}
	as, bs, ok := strings.Cut(rest, ":")
	if !ok {
		return 0, 0, false
	}
	a, errA := strconv.ParseInt(as, 10, 64)
	b, errB := strconv.ParseInt(bs, 10, 64)
	return a, b, errA == nil && errB == nil
}

func effectiveSkillNames(db *sql.DB, esc *effectiveScope) (map[int64]string, error) {
	pred, args := esc.predicate("t.", true)
	rows, err := db.Query(`SELECT t.id, t.name FROM skills t WHERE t.deleted = 0`+pred, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

func projectSlugsByID(db *sql.DB) (map[int64]string, error) {
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

// latestSkillAuditReport is the newest report's date and path; nil when the
// routine has not written one yet or the dir is unreadable (best effort — an
// insights page must not fail on a workspace permission problem).
func latestSkillAuditReport() *skillAuditReportDTO {
	path, date, ok, err := sysscan.LatestSkillAuditReport(skillAuditReportDir())
	if err != nil || !ok {
		return nil
	}
	return &skillAuditReportDTO{Date: date, Path: path}
}

// GET /api/system/skill-audit/report — the newest weekly report, redacted.
// 404 when there is none yet.
func (h *Handler) systemSkillAuditReport(w http.ResponseWriter, r *http.Request) {
	rep := latestSkillAuditReport()
	if rep == nil {
		http.Error(w, "no skill-audit report yet — seed config/routines/skill-audit.json", http.StatusNotFound)
		return
	}
	raw, err := os.ReadFile(rep.Path)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, skillAuditReportBodyDTO{skillAuditReportDTO: *rep, Markdown: redact(string(raw))}, nil)
}
