package sysscan

// Harness lint (harness² parity): two rules that judge what a skill or a
// project carries rather than how it is written.
//
//   - skill_missing_helper (warn): a SKILL.md references a helper file — a
//     resources/ doc, a scripts/ or bin/ executable, a template, a
//     ${CLAUDE_PLUGIN_ROOT}/… path, a relative markdown link — that does not
//     exist inside the skill's plugin. The skill will instruct the model to open
//     or run a file that is not there.
//   - always_on_budget (info / warn): the size of what every session of a
//     project loads before its first prompt crosses a budget. The size is
//     firstreq.Measure's measured median when the project has sessions in the
//     window, and firstreq.Estimate's estimated parts when it has none — one
//     estimator (internal/firstreq), never a second one here.
//
// Deterministic and offline: no LLM, no network. Like every linter rule, the
// only write surface is config_lint_findings, through findings.Sync in Lint.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/firstreq"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/mcpcfg"
)

const (
	RuleSkillMissingHelper = "skill_missing_helper" // warn: SKILL.md references a helper file absent from its plugin
	RuleAlwaysOnBudget     = "always_on_budget"     // info above the info budget, warn above the warn budget

	// Always-on budget defaults, in tokens of the WHOLE first prompt (the
	// measured median's MedianPromptTokens). Chosen from the live medians on
	// 2026-11-02: 11 measured projects sit at 34k–65k (this repo 57k over 228
	// sessions), and the sized parts — CLAUDE.md, MCP, skills — explain only
	// ~12–24k of that; the system prompt and built-in tools are the floor. Info
	// past 50k flags the heavier third; warn past 80k is a clear outlier.
	DefaultAlwaysOnInfoTokens = 50000
	DefaultAlwaysOnWarnTokens = 80000

	EnvAlwaysOnInfoTokens = "SWARMERY_ALWAYS_ON_INFO_TOKENS"
	EnvAlwaysOnWarnTokens = "SWARMERY_ALWAYS_ON_WARN_TOKENS"

	// alwaysOnTopN is how many contributors an always_on_budget message lists.
	alwaysOnTopN = 5
)

// helperPrefixes are the backticked path shapes treated as a helper reference.
var helperPrefixes = []string{"./", "scripts/", "resources/", "bin/", "templates/", pluginRootVar + "/"}

// bundledDirs are the first path segments that only ever mean "a file shipped
// with this skill": a backticked resources/… or templates/… path is judged even
// when the directory itself is absent — the harness² case of a helper that did
// not travel with its skill.
var bundledDirs = map[string]bool{"resources": true, "templates": true}

const pluginRootVar = "${CLAUDE_PLUGIN_ROOT}"

var (
	mdLinkRe     = regexp.MustCompile(`\[[^\]\n]*\]\(([^)\s]+)(?:\s+"[^"\n]*")?\)`)
	inlineCodeRe = regexp.MustCompile("`([^`\n]+)`")
	pluginRootRe = regexp.MustCompile(`\$\{CLAUDE_PLUGIN_ROOT\}/[A-Za-z0-9_.\-/]+`)
	fenceRe      = regexp.MustCompile("^\\s*(```|~~~)")
	schemeRe     = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.\-]*:`)
)

// HelperRef is one file reference a SKILL.md makes.
type HelperRef struct {
	Path string
	// Strict refs are unambiguous — a markdown link, a ${CLAUDE_PLUGIN_ROOT}/…
	// path, a backticked resources/ or templates/ path — and are always judged.
	// A loose ref (a backticked ./x, scripts/x or bin/x) may just as well name a
	// file of the repo the skill runs IN (`scripts/scan-flavor.sh`), so it is
	// judged only when the skill ships a directory of that name (MissingHelpers).
	Strict bool
}

// HelperRefs extracts the file references a SKILL.md body makes, in first-seen
// order and deduplicated. Prose contributes markdown links and backticked
// paths with a helperPrefixes prefix; a fenced code block contributes only
// ${CLAUDE_PLUGIN_ROOT}/… paths, the one shape an example command cannot mean
// any other way. URLs, absolute paths, directory references and placeholders
// are dropped (helperCandidate).
func HelperRefs(body []byte) []HelperRef {
	var out []HelperRef
	seen := map[string]int{}
	add := func(raw string, strict bool) {
		ref, ok := helperCandidate(raw)
		if !ok {
			return
		}
		if strings.HasPrefix(ref, pluginRootVar+"/") || bundledDirs[firstSegment(ref)] {
			strict = true
		}
		if i, dup := seen[ref]; dup {
			out[i].Strict = out[i].Strict || strict
			return
		}
		seen[ref] = len(out)
		out = append(out, HelperRef{Path: ref, Strict: strict})
	}
	inFence := false
	for _, line := range strings.Split(string(body), "\n") {
		if fenceRe.MatchString(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			for _, m := range pluginRootRe.FindAllString(line, -1) {
				add(m, true)
			}
			continue
		}
		for _, m := range mdLinkRe.FindAllStringSubmatch(line, -1) {
			add(m[1], true)
		}
		for _, m := range inlineCodeRe.FindAllStringSubmatch(line, -1) {
			code := strings.TrimSpace(m[1])
			if f := strings.Fields(code); len(f) > 0 && hasHelperPrefix(f[0]) {
				add(f[0], false)
			} else if i := strings.Index(code, pluginRootVar+"/"); i >= 0 {
				// `bash ${CLAUDE_PLUGIN_ROOT}/bin/x.sh` — the path is not the first word.
				if f := strings.Fields(code[i:]); len(f) > 0 {
					add(f[0], true)
				}
			}
		}
	}
	return out
}

func hasHelperPrefix(s string) bool {
	for _, p := range helperPrefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// firstSegment is a relative path's first segment, a leading ./ skipped.
func firstSegment(ref string) string {
	seg, _, _ := strings.Cut(strings.TrimPrefix(ref, "./"), "/")
	return seg
}

// helperCandidate normalises one raw reference and reports whether it is a
// local file reference worth resolving: anchors and queries are cut, trailing
// prose punctuation trimmed; URLs and any other scheme (data:, mailto:),
// absolute and home paths, pure anchors, directory references (a trailing /)
// and placeholders (<…>, {…}, globs, other $VARS, ellipses) are rejected.
// ${CLAUDE_PLUGIN_ROOT} is the one variable kept.
func helperCandidate(raw string) (string, bool) {
	ref := strings.TrimSpace(raw)
	ref = strings.TrimRight(ref, ".,;:)'\"")
	if i := strings.IndexAny(ref, "#?"); i >= 0 {
		ref = ref[:i]
	}
	if ref == "" || schemeRe.MatchString(ref) || strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, "~") ||
		strings.HasSuffix(ref, "/") {
		return "", false
	}
	rest := strings.TrimPrefix(ref, pluginRootVar)
	if rest == "" || strings.ContainsAny(rest, "<>{}*$|") || strings.Contains(rest, "...") || strings.Contains(rest, "…") {
		return "", false
	}
	return ref, true
}

// HelperScope is where one skill's references may resolve.
type HelperScope struct {
	SkillDir    string // the skill directory (holds SKILL.md)
	PluginRoot  string // a plugin skill's plugin root; "" otherwise
	ProjectRoot string // a project-scope skill's project root; "" otherwise
}

// roots are the directories a relative reference is tried against, nearest
// first, and the boundary no resolved path may escape: the plugin root for a
// plugin skill, the project root for a project skill, the skill dir otherwise.
func (sc HelperScope) roots() (roots []string, boundary string) {
	roots = []string{sc.SkillDir}
	boundary = sc.SkillDir
	if sc.PluginRoot != "" {
		roots = append(roots, sc.PluginRoot)
		boundary = sc.PluginRoot
	} else if sc.ProjectRoot != "" {
		roots = append(roots, sc.ProjectRoot)
		boundary = sc.ProjectRoot
	}
	return roots, boundary
}

// MissingHelpers returns the references (HelperRefs) of one SKILL.md that
// resolve to nothing inside the skill's scope. A reference resolves when the
// path exists under any of the scope's roots without escaping its boundary;
// ${CLAUDE_PLUGIN_ROOT}/… resolves against the plugin root only, and is not
// judged in a non-plugin skill (the variable is undefined there). A loose ref
// is judged only when a directory named like its first segment exists under one
// of the roots — the skill ships scripts/, so a missing scripts/x.sh is a miss.
func MissingHelpers(sc HelperScope, body []byte) []string {
	roots, boundary := sc.roots()
	var missing []string
	for _, ref := range HelperRefs(body) {
		var cands []string
		if rel, ok := strings.CutPrefix(ref.Path, pluginRootVar+"/"); ok {
			if sc.PluginRoot == "" {
				continue
			}
			cands = []string{filepath.Join(sc.PluginRoot, rel)}
		} else {
			if !ref.Strict && !shipsDir(sc.SkillDir, firstSegment(ref.Path)) {
				continue
			}
			for _, r := range roots {
				cands = append(cands, filepath.Join(r, ref.Path))
			}
		}
		if !anyExists(boundary, cands) {
			missing = append(missing, ref.Path)
		}
	}
	return missing
}

// shipsDir reports whether seg names a directory inside the skill dir.
func shipsDir(skillDir, seg string) bool {
	fi, err := os.Stat(filepath.Join(skillDir, seg))
	return err == nil && fi.IsDir()
}

// anyExists reports whether some candidate exists inside boundary.
func anyExists(boundary string, cands []string) bool {
	for _, c := range cands {
		if !within(boundary, c) {
			continue
		}
		if _, err := os.Stat(c); err == nil {
			return true
		}
	}
	return false
}

// within reports whether path lies at or under root (both cleaned).
func within(root, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// lintSkillMissingHelpers evaluates skill_missing_helper over every live skill's
// CURRENT stored SKILL.md (the linter never re-reads skill content from disk),
// checking existence on disk only after the cursor is closed (single-connection
// pool — see lintDocs). A plugin skill lives at <pluginRoot>/skills/<name>
// (the scanner globs skills/*/ only), so its plugin root is two levels up.
func (s *Scanner) lintSkillMissingHelpers(byRule map[string][]lintFinding) error {
	rows, err := s.db.Query(
		`SELECT k.id, k.dir_path, COALESCE(k.origin, ''), COALESCE(p.path, ''), COALESCE(v.content, '')
		 FROM skills k
		 LEFT JOIN skill_versions v ON v.id = k.current_version_id
		 LEFT JOIN projects p ON p.id = k.project_id AND k.scope = 'project'
		 WHERE k.deleted = 0 ORDER BY k.id`)
	if err != nil {
		return err
	}
	type skillRow struct {
		id                   int64
		dir, origin, project string
		content              string
	}
	var skills []skillRow
	for rows.Next() {
		var r skillRow
		if err := rows.Scan(&r.id, &r.dir, &r.origin, &r.project, &r.content); err != nil {
			rows.Close()
			return err
		}
		skills = append(skills, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range skills {
		_, body, err := splitFrontmatter([]byte(r.content))
		if err != nil {
			continue // unparseable — the parse_error finding owns this item
		}
		sc := HelperScope{SkillDir: r.dir}
		if r.origin == "plugin" {
			sc.PluginRoot = filepath.Dir(filepath.Dir(r.dir))
		} else if filepath.IsAbs(r.project) {
			sc.ProjectRoot = r.project
		}
		missing := MissingHelpers(sc, body)
		if len(missing) == 0 {
			continue
		}
		where := "the skill directory"
		switch {
		case sc.PluginRoot != "":
			where = "the plugin"
		case sc.ProjectRoot != "":
			where = "the project"
		}
		byRule[RuleSkillMissingHelper] = append(byRule[RuleSkillMissingHelper], lintFinding{
			target:   fmt.Sprintf("skill:%d", r.id),
			severity: "warn",
			message: fmt.Sprintf("%s: references %d helper file(s) not present in %s: %s — the skill will send the model to a file that is not there",
				filepath.Join(r.dir, "SKILL.md"), len(missing), where, strings.Join(missing, ", ")),
		})
	}
	return nil
}

// AlwaysOnTarget is the always_on_budget lint target of a project. The `|path`
// tail is what the System insights start-cost list resolves a project from.
func AlwaysOnTarget(projectPath string) string { return "always_on|" + projectPath }

// lintAlwaysOnBudget evaluates always_on_budget per live project. The size is
// the measured median whole first prompt (firstreq.Measure, WindowDays) when the
// project has sessions in the window; otherwise firstreq.Estimate's estimated
// parts, labelled "estimate". The message names the top contributors
// (firstreq.Contributors — Estimate's own sizing) and points at the project's
// First request card, where the full split renders.
//
// The plugin set is read from the user and repo settings layers only: the
// daemon-side overlays the API layers on top (settings_overlays.go) are not
// visible to the scanner, so an overlay-only plugin's skills are not counted.
func (s *Scanner) lintAlwaysOnBudget(byRule map[string][]lintFinding) error {
	projects, err := s.loadProjects()
	if err != nil {
		return err
	}
	infoAt, warnAt := s.cfg.AlwaysOnInfoTokens, s.cfg.AlwaysOnWarnTokens
	since := time.Now().Add(-firstreq.WindowDays * 24 * time.Hour)
	claudeDir := s.cfg.ClaudeDir
	for _, pr := range projects {
		measured, err := firstreq.Measure(s.db, pr.id, since)
		if err != nil {
			return err
		}
		servers := mcpcfg.ReadConfigured(claudeDir, pr.path)
		tools, err := firstreq.ObservedMCPTools(s.db, pr.id, servers, since)
		if err != nil {
			return err
		}
		skills, err := firstreq.VisibleSkills(s.db, pr.id, firstreq.EnabledPlugins(claudeDir, pr.path))
		if err != nil {
			return err
		}
		src := firstreq.Sources{ClaudeDir: claudeDir, ProjectRoot: pr.path}
		est := firstreq.Estimate(src, tools, skills, measured.MedianPromptTokens)
		size := measured.MedianPromptTokens
		basis := fmt.Sprintf("measured median first request over %d session(s) in %d days; CLAUDE.md, MCP and skills explain ~%d of it, the rest is the system prompt, built-in tools and hook output",
			measured.Sessions, firstreq.WindowDays, est.EstimatedTotal())
		if measured.Sessions == 0 {
			size = est.EstimatedTotal()
			basis = "estimate (~bytes/4) of CLAUDE.md, MCP tools and skill listings — no sessions in the window to measure"
		}
		severity := ""
		budget := 0
		switch {
		case size > int64(warnAt):
			severity, budget = "warn", warnAt
		case size > int64(infoAt):
			severity, budget = "info", infoAt
		default:
			continue
		}
		byRule[RuleAlwaysOnBudget] = append(byRule[RuleAlwaysOnBudget], lintFinding{
			target:   AlwaysOnTarget(pr.path),
			severity: severity,
			message: fmt.Sprintf("%s: always-on context ~%d tokens (%s) exceeds the %d-token budget; top contributors: %s — see the project's First request card",
				pr.path, size, basis, budget, formatContributors(firstreq.Contributors(src, tools, skills, alwaysOnTopN))),
		})
	}
	return nil
}

// formatContributors renders the ranked contributors for a finding message.
// CLAUDE.md files are shown by base name under their parent directory so the
// message stays readable; the full path is on the First request card.
func formatContributors(cs []firstreq.Contributor) string {
	if len(cs) == 0 {
		return "none sized"
	}
	var b bytes.Buffer
	for i, c := range cs {
		if i > 0 {
			b.WriteString(", ")
		}
		name := c.Name
		if c.Kind == "claude_md" {
			name = filepath.Join(filepath.Base(filepath.Dir(c.Name)), filepath.Base(c.Name))
		}
		fmt.Fprintf(&b, "%s %s ~%d", strings.ReplaceAll(c.Kind, "_", " "), name, c.Tokens)
	}
	return b.String()
}
