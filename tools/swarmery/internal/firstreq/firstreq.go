// Package firstreq makes a session's START cost visible: how big the very first
// request of a session in a project is, and roughly what fills it.
//
// Two numbers, kept apart on purpose:
//
//   - Measure is MEASURED: the median, over a window of sessions, of what the
//     first assistant turn's usage reported. Real accounting, read from turns.
//   - Estimate is an ESTIMATE: the CLAUDE.md files, MCP tool names/schemas and
//     skill descriptions that ride along with every first request, sized from
//     disk and the registry at ~4 bytes per token (bytesPerToken — the same crude
//     ratio internal/ctxhogs states in its caveat), with Other as the remainder.
//     A ranking aid, not a tokenizer: the UI labels it «estimate (~bytes/4)».
//
// Read-only. Nothing here edits a config file or trims anything — the card that
// renders this only shows where the start cost goes.
package firstreq

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/mcpcfg"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/projectscan"
)

// bytesPerToken is the crude bytes→tokens ratio of every estimate here. Stated
// verbatim in the UI caveat so nobody reads the breakdown as an accounting.
const bytesPerToken = 4

// WindowDays is the measurement window the API and the card use.
const WindowDays = 30

// syntheticModel is the model id Claude Code stamps on a turn it generated
// itself (an interrupted request, an API error stand-in). Such a turn carries no
// usage and is not a request, so it can never be "the first request".
const syntheticModel = "<synthetic>"

// Measured is the first-request size of a project's sessions over a window.
type Measured struct {
	// MedianTokens is the median of cache_write_5m + cache_write_1h on each
	// session's first assistant turn — what starting a session WRITES to the
	// prompt cache, the premium-priced part of the start cost. Turns ingested
	// before the TTL split (migration 0075) fall back to the flat
	// tokens_cache_write total.
	MedianTokens int64 `json:"medianTokens"`
	// MedianPromptTokens is the median of the WHOLE first prompt
	// (tokens_in + cache_read + cache_write). Most sessions open on a prefix
	// another session already cached (system prompt, built-in tools), so the
	// cache write alone undercounts what the first request carries; the
	// breakdown is a split of this number.
	MedianPromptTokens int64 `json:"medianPromptTokens"`
	// Sessions is how many sessions the medians are over.
	Sessions int `json:"sessions"`
}

// Measure returns the median first-request size of the project's sessions that
// started at or after since.
//
// The first request is the session's lowest-seq ORCHESTRATOR assistant turn
// (agent_name IS NULL — a subagent's first turn is its own request, not the
// session's), skipping synthetic turns. Sessions with fewer than two
// orchestrator turns are excluded: a lone turn is an aborted start, not a
// session. The medians are computed here, not in SQL.
func Measure(db *sql.DB, projectID int64, since time.Time) (Measured, error) {
	rows, err := db.Query(`
		SELECT COALESCE(t.tokens_in, 0), COALESCE(t.tokens_cache_read, 0),
		       t.tokens_cache_write, t.cache_write_5m_tokens, t.cache_write_1h_tokens
		  FROM sessions s
		  JOIN turns t ON t.session_id = s.id AND t.seq = (
		         SELECT MIN(f.seq) FROM turns f
		          WHERE f.session_id = s.id AND f.role = 'assistant' AND f.agent_name IS NULL
		            AND COALESCE(f.model, '') <> ?)
		 WHERE s.project_id = ? AND s.started_at >= ?
		   AND (SELECT COUNT(*) FROM turns c
		         WHERE c.session_id = s.id AND c.agent_name IS NULL) >= 2`,
		syntheticModel, projectID, since.UTC().Format("2006-01-02T15:04:05"))
	if err != nil {
		return Measured{}, err
	}
	defer rows.Close()
	var writes, prompts []int64
	for rows.Next() {
		var in, read int64
		var flat, w5m, w1h sql.NullInt64
		if err := rows.Scan(&in, &read, &flat, &w5m, &w1h); err != nil {
			return Measured{}, err
		}
		write := flat.Int64
		if w5m.Valid || w1h.Valid {
			write = w5m.Int64 + w1h.Int64
		}
		writes = append(writes, write)
		prompts = append(prompts, in+read+write)
	}
	if err := rows.Err(); err != nil {
		return Measured{}, err
	}
	return Measured{MedianTokens: median(writes), MedianPromptTokens: median(prompts), Sessions: len(writes)}, nil
}

// median of xs (mean of the two middle values for an even count); 0 when empty.
// Sorts xs in place.
func median(xs []int64) int64 {
	if len(xs) == 0 {
		return 0
	}
	sort.Slice(xs, func(i, j int) bool { return xs[i] < xs[j] })
	mid := len(xs) / 2
	if len(xs)%2 == 1 {
		return xs[mid]
	}
	return (xs[mid-1] + xs[mid]) / 2
}

// Sources locates the files a session loads at start.
type Sources struct {
	ClaudeDir   string // the user tier: <ClaudeDir>/CLAUDE.md and <ClaudeDir>/rules
	ProjectRoot string
}

// MCPTool is one MCP tool a session can see. Schema is the tool's description +
// input schema JSON when known, nil when only the name is — which is the normal
// case: no local file holds a server's tool list, only the server does.
type MCPTool struct {
	Server string
	Name   string
	Schema json.RawMessage
}

// Skill is one skill listed to the model at session start.
type Skill struct {
	Name        string
	Description string
}

// Breakdown is the estimated split of the first request. Every token field is
// an ESTIMATE at ~bytes/4; Other is the measured size minus their sum.
type Breakdown struct {
	ClaudeMD int64 `json:"claudeMd"`
	MCP      int64 `json:"mcp"`
	Skills   int64 `json:"skills"`
	Other    int64 `json:"other"`
	// Note is the caveat the UI shows beside the bar, verbatim.
	Note string `json:"note"`
	// The counts behind the numbers, so the card can say what it measured.
	ClaudeMDFiles []string `json:"claudeMdFiles"`
	MCPTools      int      `json:"mcpTools"`
	MCPSchemas    int      `json:"mcpSchemas"` // of MCPTools, how many had a schema
	SkillCount    int      `json:"skillCount"`
}

// Estimate splits measured (the first-request size it is a breakdown OF) into
// CLAUDE.md, MCP, skills and Other. Unreadable files contribute nothing.
func Estimate(src Sources, mcp []MCPTool, skills []Skill, measured int64) Breakdown {
	b := Breakdown{ClaudeMDFiles: []string{}}

	var mdBytes int64
	for _, path := range ClaudeMDFiles(src) {
		if fi, err := os.Stat(path); err == nil {
			mdBytes += fi.Size()
			b.ClaudeMDFiles = append(b.ClaudeMDFiles, path)
		}
	}
	b.ClaudeMD = mdBytes / bytesPerToken

	var mcpBytes int64
	for _, tool := range mcp {
		mcpBytes += int64(len(tool.Name) + len(tool.Schema))
		if len(tool.Schema) > 0 {
			b.MCPSchemas++
		}
	}
	b.MCPTools = len(mcp)
	b.MCP = mcpBytes / bytesPerToken

	var skillBytes int64
	for _, s := range skills {
		// Listed as "- <name>: <description>\n".
		skillBytes += int64(len(s.Name) + len(s.Description) + 4)
	}
	b.SkillCount = len(skills)
	b.Skills = skillBytes / bytesPerToken

	b.Other = max(0, measured-(b.ClaudeMD+b.MCP+b.Skills))
	b.Note = note(b)
	return b
}

func note(b Breakdown) string {
	n := "estimate (~bytes/4): CLAUDE.md files, MCP tools and skill descriptions are sized from disk and the registry; " +
		"Other is the measured first request minus their sum (system prompt, built-in tools, agent list, hook output)."
	if unknown := b.MCPTools - b.MCPSchemas; unknown > 0 {
		n += fmt.Sprintf(" %d of %d MCP tools are counted by name only — a tool's schema is readable only from its running server, so the rest of it lands in Other.",
			unknown, b.MCPTools)
	}
	return n
}

// ClaudeMDFiles lists the memory files Claude Code loads at session start for a
// project root, in load order, whether or not each exists:
//
//   - the user tier: <ClaudeDir>/CLAUDE.md and unconditional <ClaudeDir>/rules/**.md;
//   - every ancestor of the root, outermost first: CLAUDE.md and CLAUDE.local.md;
//   - the root itself: CLAUDE.md, CLAUDE.local.md, .claude/CLAUDE.md and the
//     unconditional .claude/rules/**.md.
//
// A rule whose frontmatter declares `paths:` is conditional — it loads only when
// a matching file is read, never into the first request — and is left out.
// `@imports` inside a file are not followed.
func ClaudeMDFiles(src Sources) []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	if src.ClaudeDir != "" {
		add(filepath.Join(src.ClaudeDir, "CLAUDE.md"))
		for _, p := range unconditionalRules(filepath.Join(src.ClaudeDir, "rules")) {
			add(p)
		}
	}
	if src.ProjectRoot == "" {
		return out
	}
	root := filepath.Clean(src.ProjectRoot)
	var dirs []string
	for d := root; ; d = filepath.Dir(d) {
		dirs = append(dirs, d)
		if parent := filepath.Dir(d); parent == d {
			break
		}
	}
	// The filesystem root itself is not searched; walk the rest outermost first.
	for i := len(dirs) - 2; i >= 0; i-- {
		add(filepath.Join(dirs[i], "CLAUDE.md"))
		add(filepath.Join(dirs[i], "CLAUDE.local.md"))
	}
	add(filepath.Join(root, ".claude", "CLAUDE.md"))
	for _, p := range unconditionalRules(filepath.Join(root, ".claude", "rules")) {
		add(p)
	}
	return out
}

// unconditionalRules lists the *.md under dir (recursively, sorted) whose
// frontmatter does not scope them with `paths:`.
func unconditionalRules(dir string) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // a missing rules dir is the common case
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		if raw, err := os.ReadFile(path); err == nil && !hasPathsFrontmatter(raw) {
			out = append(out, path)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// hasPathsFrontmatter reports whether a leading `---` frontmatter block carries a
// top-level `paths:` key.
func hasPathsFrontmatter(raw []byte) bool {
	sc := bufio.NewScanner(bytes.NewReader(raw))
	if !sc.Scan() || strings.TrimSpace(sc.Text()) != "---" {
		return false
	}
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "---" {
			return false
		}
		if strings.HasPrefix(line, "paths:") {
			return true
		}
	}
	return false
}

// EnabledPlugins returns the names of the plugins switched on for a project:
// the user's <ClaudeDir>/settings.json, then the repo's .claude/settings.json,
// then its .claude/settings.local.json, then any declared overlays — later
// layers win on a key, which is the order Claude Code merges them in. Keys are
// "<name>@<marketplace>"; the result is keyed by <name>, the form the registry's
// plugin_name column carries.
func EnabledPlugins(claudeDir, projectRoot string, overlays ...projectscan.SettingsOverlay) map[string]bool {
	merged := map[string]bool{}
	layer := func(s projectscan.Settings) {
		for k, v := range s.EnabledPlugins {
			merged[k] = v
		}
	}
	for _, path := range []string{
		filepath.Join(claudeDir, "settings.json"),
		filepath.Join(projectRoot, ".claude", "settings.json"),
		filepath.Join(projectRoot, ".claude", "settings.local.json"),
	} {
		if s, err := projectscan.ReadSettings(path); err == nil {
			layer(s)
		}
	}
	for _, ov := range overlays {
		layer(ov.Settings)
	}
	out := map[string]bool{}
	for key, on := range merged {
		if name, _, ok := strings.Cut(key, "@"); ok && on {
			out[name] = true
		}
	}
	return out
}

// VisibleSkills lists the skills a session in the project sees: the user's own
// global skills, the project's local ones, and the skills of every enabled
// plugin (plugins keyed as EnabledPlugins returns them).
func VisibleSkills(db *sql.DB, projectID int64, plugins map[string]bool) ([]Skill, error) {
	rows, err := db.Query(`
		SELECT name, COALESCE(description, ''), origin, COALESCE(plugin_name, '')
		  FROM skills
		 WHERE deleted = 0
		   AND ((origin = 'local' AND scope = 'global')
		     OR (scope = 'project' AND project_id = ?)
		     OR origin = 'plugin')
		 ORDER BY name`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Skill{}
	for rows.Next() {
		var s Skill
		var origin, plugin string
		if err := rows.Scan(&s.Name, &s.Description, &origin, &plugin); err != nil {
			return nil, err
		}
		if origin == "plugin" && !plugins[plugin] {
			continue
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ObservedMCPTools lists the distinct MCP tools seen in tool calls since the
// window start that a session of the project can be shown: every tool its own
// sessions called (whatever server it came from — plugin and claude.ai servers
// are in no local file), plus every tool of a server configured for it that was
// called anywhere. Names only (Schema nil): see MCPTool.
func ObservedMCPTools(db *sql.DB, projectID int64, servers []mcpcfg.ConfiguredServer, since time.Time) ([]MCPTool, error) {
	rows, err := db.Query(`
		SELECT e.tool_name, MAX(s.project_id = ?)
		  FROM events e JOIN sessions s ON s.id = e.session_id
		 WHERE e.ts >= ? AND e.tool_name LIKE 'mcp\_\_%' ESCAPE '\'
		 GROUP BY e.tool_name
		 ORDER BY e.tool_name`, projectID, since.UTC().Format("2006-01-02T15:04:05"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	prefixes := make(map[string]string, len(servers))
	for _, srv := range servers {
		prefixes[mcpcfg.ToolPrefix(srv.Name)] = srv.Name
	}
	out := []MCPTool{}
	for rows.Next() {
		var name string
		var own bool
		if err := rows.Scan(&name, &own); err != nil {
			return nil, err
		}
		server := ""
		for prefix, srv := range prefixes {
			if strings.HasPrefix(name, prefix) {
				server = srv
				break
			}
		}
		if !own && server == "" {
			continue
		}
		out = append(out, MCPTool{Server: server, Name: name})
	}
	return out, rows.Err()
}
