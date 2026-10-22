package mcpcfg

// File-based view of the MCP servers configured for a project.
//
// Reader.List shells to `claude mcp list`, which HEALTH-CHECKS every server —
// it spawns each stdio server and dials each remote one. That is the right cost
// for the Connectors page (the operator asked for live status) and the wrong one
// for anything periodic: the sysscan linter runs on every registry change, and
// starting every MCP server on each pass would be a side effect nobody asked for.
//
// ReadConfigured reads the same declarations the CLI does, straight from the
// files that hold them, and never connects to anything:
//
//	user    <global config>.mcpServers
//	local   <global config>.projects[<root>].mcpServers
//	project <root>/.mcp.json mcpServers
//
// It deliberately does NOT report plugin-provided servers (they come and go with
// the plugin toggle, which has its own surface) or claude.ai connectors (they
// are account-side, not in any local file). Every read is tolerant: a missing or
// malformed file contributes nothing, never an error.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
)

// ConfiguredServer is one MCP server a config FILE declares for a project.
type ConfiguredServer struct {
	Name  string `json:"name"`
	Scope Scope  `json:"scope"` // user | local | project
	// Source is the file that declares the server — display-only.
	Source string `json:"source"`
}

// GlobalConfigPath returns the Claude Code global config file for claudeDir.
// With CLAUDE_CONFIG_DIR set the file lives INSIDE that dir; for the stock
// ~/.claude it is the sibling ~/.claude.json. The in-dir file wins when both
// exist, which is the CLI's own precedence for a relocated config dir.
func GlobalConfigPath(claudeDir string) string {
	inside := filepath.Join(claudeDir, ".claude.json")
	if _, err := os.Stat(inside); err == nil {
		return inside
	}
	return filepath.Join(filepath.Dir(claudeDir), ".claude.json")
}

// globalConfig is the subset of the global config file this reader needs.
type globalConfig struct {
	MCPServers map[string]json.RawMessage `json:"mcpServers"`
	Projects   map[string]struct {
		MCPServers             map[string]json.RawMessage `json:"mcpServers"`
		DisabledMCPServers     []string                   `json:"disabledMcpServers"`
		DisabledMCPJSONServers []string                   `json:"disabledMcpjsonServers"`
	} `json:"projects"`
}

// projectMCPFile is <root>/.mcp.json.
type projectMCPFile struct {
	MCPServers map[string]json.RawMessage `json:"mcpServers"`
}

// ReadConfigured returns the servers configured for projectRoot, minus the ones
// the project switched off (disabledMcpServers for any scope,
// disabledMcpjsonServers for .mcp.json servers). Sorted by name, then scope, so
// callers and tests get a stable order. A name declared in two scopes is
// reported once per scope: each declaration is a separate thing to remove.
func ReadConfigured(claudeDir, projectRoot string) []ConfiguredServer {
	out := []ConfiguredServer{}
	globalPath := GlobalConfigPath(claudeDir)

	var g globalConfig
	if raw, err := os.ReadFile(globalPath); err == nil {
		_ = json.Unmarshal(raw, &g) // malformed → zero value → nothing declared
	}
	proj := g.Projects[projectRoot]
	disabled := proj.DisabledMCPServers

	for name := range g.MCPServers {
		if !slices.Contains(disabled, name) {
			out = append(out, ConfiguredServer{Name: name, Scope: ScopeUser, Source: globalPath})
		}
	}
	for name := range proj.MCPServers {
		if !slices.Contains(disabled, name) {
			out = append(out, ConfiguredServer{Name: name, Scope: ScopeLocal, Source: globalPath})
		}
	}

	mcpJSON := filepath.Join(projectRoot, ".mcp.json")
	if raw, err := os.ReadFile(mcpJSON); err == nil {
		var pf projectMCPFile
		if json.Unmarshal(raw, &pf) == nil {
			for name := range pf.MCPServers {
				if slices.Contains(disabled, name) || slices.Contains(proj.DisabledMCPJSONServers, name) {
					continue
				}
				out = append(out, ConfiguredServer{Name: name, Scope: ScopeProject, Source: mcpJSON})
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Scope < out[j].Scope
	})
	return out
}

// ToolPrefix is the prefix Claude Code gives every tool of an MCP server in a
// transcript: `mcp__<server>__`, with each character outside [A-Za-z0-9_-]
// folded to `_` ("Atlassian Rovo" → mcp__Atlassian_Rovo__). Events record the
// tool under that name, so this is the join key between a configured server and
// its usage.
func ToolPrefix(server string) string {
	b := make([]byte, 0, len(server)+7)
	b = append(b, "mcp__"...)
	for _, r := range server { // one `_` per character, not per UTF-8 byte
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b = append(b, byte(r))
		default:
			b = append(b, '_')
		}
	}
	return string(append(b, "__"...))
}
