package approvals

// Production-deploy guard. A permission request whose tool call matches one of
// the prod-deploy patterns is recorded with risk_class='prod-deploy', never
// auto-approved, handed straight back to the session's native permission
// dialog (resolved_elsewhere via 'local-only'), and refused by every remote
// approve/answer path (ErrLocalOnly → HTTP 403).
//
// The pattern list is the union of three sources:
//
//  1. DefaultProdDeployPatterns — always on; there is no switch to drop them;
//  2. daemon extras — serve --prod-deploy-patterns / SWARMERY_PROD_DEPLOY_PATTERNS;
//  3. the project's own <projectDir>/.claude/project.json key
//     approvals.prodDeployPatterns — re-read whenever the file's mtime or size
//     changes.
//
// Patterns use the approval_rules syntax (ParseRulePattern): the tool part is
// matched exactly, the argument glob case-insensitively. A project file that is
// unreadable or malformed is logged once (per file version) and contributes
// nothing — the defaults and extras still apply; the guard never fails open.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// RiskProdDeploy is permission_requests.risk_class for a production deploy
// (the empty string is an ordinary request).
const RiskProdDeploy = "prod-deploy"

// ViaLocalOnly is the resolved_via of a prod-deploy request handed back to the
// session's terminal; LocalOnlyReason is the reason stamped on it.
const (
	ViaLocalOnly    = "local-only"
	LocalOnlyReason = "production deploy — confirm it in the session's own terminal; remote approval is disabled"
)

// ErrLocalOnly: an approve (or answer) was attempted on a prod-deploy request.
// Mapped to HTTP 403 by the API layer.
var ErrLocalOnly = errors.New("production deploys must be confirmed in the session's terminal")

// DefaultProdDeployPatterns are neutral, vendor-free rule globs (same syntax as
// approval_rules, see ParseRulePattern), matched case-insensitively against the
// tool's argument string. Bare `--prod` / `--production` globs are deliberately
// absent: they match `npm ci --production`. Exported so a parity test can pin
// any other consumer of the same defaults to this list.
var DefaultProdDeployPatterns = []string{
	"Bash(*deploy*prod*)",
	"Bash(*release*prod*)",
	"Bash(terraform apply*)",
	"Bash(*terraform*apply*prod*)",
	"Bash(*kubectl*prod*)",
	"Bash(*helm*upgrade*prod*)",
	"Bash(*git push*prod*)",
}

// projectConfigKey documents the project.json location of the per-project list.
const projectConfigKey = "approvals.prodDeployPatterns"

// ProdGuard matches tool calls against the prod-deploy pattern list. Safe for
// concurrent use; the zero value is not usable — build one with NewProdGuard.
type ProdGuard struct {
	base         []RulePattern // defaults ∪ daemon extras, inner globs lower-cased
	projectCache sync.Map      // project.json path → projectPatterns
	statErrs     sync.Map      // project.json path → last logged stat failure
}

// projectPatterns is one cached read of a project's project.json, keyed by the
// file version (mtime + size) it was parsed from.
type projectPatterns struct {
	modTime  time.Time
	size     int64
	patterns []RulePattern
}

// NewProdGuard builds a guard over the defaults plus the daemon extras. Every
// extra must parse as a rule pattern; an invalid one is a startup error.
func NewProdGuard(extra []string) (*ProdGuard, error) {
	g := &ProdGuard{}
	for _, s := range DefaultProdDeployPatterns {
		p, err := parseGuardPattern(s)
		if err != nil {
			return nil, fmt.Errorf("default prod-deploy pattern %q: %w", s, err)
		}
		g.base = append(g.base, p)
	}
	for _, s := range extra {
		if strings.TrimSpace(s) == "" {
			continue
		}
		p, err := parseGuardPattern(s)
		if err != nil {
			return nil, fmt.Errorf("prod-deploy pattern: %w", err)
		}
		g.base = append(g.base, p)
	}
	return g, nil
}

// SplitPatternList splits a comma-separated pattern list (the flag/env form),
// trimming blanks.
func SplitPatternList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// parseGuardPattern parses a rule pattern and lower-cases its argument glob so
// matching is case-insensitive.
func parseGuardPattern(s string) (RulePattern, error) {
	p, err := ParseRulePattern(s)
	if err != nil {
		return RulePattern{}, err
	}
	p.Inner = strings.ToLower(p.Inner)
	return p, nil
}

// Match reports whether one tool call is a production deploy for the project
// rooted at projectDir (may be empty or a non-path such as '(unknown)': only
// the defaults and extras apply then).
func (g *ProdGuard) Match(projectDir, toolName string, in json.RawMessage) bool {
	if g == nil {
		return false
	}
	arg, hasArg := argOf(toolName, in)
	arg = strings.ToLower(arg)
	matches := func(ps []RulePattern) bool {
		for _, p := range ps {
			if p.Tool != toolName {
				continue
			}
			if !p.HasInner {
				return true
			}
			if hasArg && globMatch(p.Inner, arg) {
				return true
			}
		}
		return false
	}
	return matches(g.base) || matches(g.projectPatterns(projectDir))
}

// projectPatterns returns the project's own prod-deploy patterns, re-reading
// project.json only when its mtime or size changed. A missing file is the
// normal case and is silent; an unreadable or malformed file is logged once per
// file version and contributes no patterns.
func (g *ProdGuard) projectPatterns(projectDir string) []RulePattern {
	if projectDir == "" || !filepath.IsAbs(projectDir) {
		return nil
	}
	path := filepath.Join(projectDir, ".claude", "project.json")
	info, err := os.Stat(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			g.logStatOnce(path, err)
		}
		return nil
	}
	if v, ok := g.projectCache.Load(path); ok {
		c := v.(projectPatterns)
		if c.modTime.Equal(info.ModTime()) && c.size == info.Size() {
			return c.patterns
		}
	}
	patterns, problem := readProjectPatterns(path)
	if problem != "" {
		log.Printf("warn: approvals: %s %s: %s — prod-deploy guard keeps the defaults and daemon extras",
			path, projectConfigKey, problem)
	}
	g.projectCache.Store(path, projectPatterns{modTime: info.ModTime(), size: info.Size(), patterns: patterns})
	return patterns
}

// logStatOnce logs a stat failure once per (path, failure) so a persistently
// unreadable file does not flood the log on every permission request.
func (g *ProdGuard) logStatOnce(path string, err error) {
	problem := err.Error()
	if prev, loaded := g.statErrs.Swap(path, problem); loaded && prev.(string) == problem {
		return
	}
	log.Printf("warn: approvals: %s: stat: %s — prod-deploy guard keeps the defaults and daemon extras", path, problem)
}

// readProjectPatterns parses approvals.prodDeployPatterns out of a project.json.
// It returns the valid patterns plus a non-empty problem description when the
// file or any entry was unusable (valid entries are still kept).
func readProjectPatterns(path string) ([]RulePattern, string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Sprintf("read: %v", err)
	}
	var cfg struct {
		Approvals struct {
			ProdDeployPatterns []string `json:"prodDeployPatterns"`
		} `json:"approvals"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Sprintf("malformed: %v", err)
	}
	var out []RulePattern
	var bad []string
	for _, s := range cfg.Approvals.ProdDeployPatterns {
		p, err := parseGuardPattern(s)
		if err != nil {
			bad = append(bad, fmt.Sprintf("%q", s))
			continue
		}
		out = append(out, p)
	}
	if len(bad) > 0 {
		return out, "skipping invalid pattern(s) " + strings.Join(bad, ", ")
	}
	return out, ""
}
