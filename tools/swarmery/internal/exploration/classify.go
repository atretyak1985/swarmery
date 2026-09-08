// Package exploration answers one question about an agent's tool calls: what
// share of them is spent rediscovering the repository rather than changing it.
//
// The classifier is deliberately coarse — four kinds, no scoring — because the
// number is only useful if it is stable across weeks and readable without a
// legend. Two rules are load-bearing and easy to get wrong:
//
//   - Discovery through an MCP server (serena, graphify, graft) counts as
//     Explore, not Other. Moving grep behind a tool is not a saving, and a
//     metric that rewarded it would report a win for a no-op. A real win shows
//     up as fewer calls overall and a lower Explore share.
//   - A shell command that EDITS counts as Run, not Explore, even when its
//     binary is normally a reader: `sed -i` and `find … -exec rm` mutate the
//     tree, and filing them under Explore would understate edit work while
//     inflating the very number this package exists to drive down.
package exploration

import (
	"path"
	"strings"
)

// Kind is the coarse bucket a single tool call falls into.
type Kind string

const (
	// Explore is a read/search call: rediscovering what the repo contains.
	Explore Kind = "explore"
	// Edit is a write to a file in the repo.
	Edit Kind = "edit"
	// Run is a shell command that is not a read/search (builds, tests, git,
	// package managers, and edit-by-shell like `sed -i`).
	Run Kind = "run"
	// Other is everything else — Task/Agent dispatch, TodoWrite, web fetches.
	Other Kind = "other"
)

// exploreCmds are the shell binaries whose default mode is reading or
// searching. Matched on the command's basename, so `/usr/bin/grep` counts.
var exploreCmds = map[string]bool{
	"grep": true, "rg": true, "ugrep": true, "cat": true, "sed": true,
	"head": true, "tail": true, "find": true, "ls": true, "tree": true,
	"wc": true, "bat": true, "fd": true,
}

// discoveryServers are MCP servers whose tools are repo discovery by another
// name. Matched as a substring of the server segment of an `mcp__server__tool`
// name, so plugin-qualified servers ("plugin_lsp-pack_serena") match too.
var discoveryServers = []string{"serena", "graphify", "graft"}

// Classify buckets one tool call. command is the Bash input command
// (json_extract(payload, '$.input.command')) and is ignored for every other
// tool.
func Classify(toolName, command string) Kind {
	name := strings.ToLower(strings.TrimSpace(toolName))
	switch name {
	case "read", "grep", "glob":
		return Explore
	case "edit", "write", "multiedit", "notebookedit":
		return Edit
	case "bash":
		return classifyCommand(command)
	}
	if isDiscoveryTool(name) {
		return Explore
	}
	return Other
}

// isDiscoveryTool reports whether an (already lowercased) tool name belongs to
// a discovery MCP server. Bare "serena"/"graphify"/"graft" match too — some
// transcripts record the server, not the qualified tool.
func isDiscoveryTool(name string) bool {
	server := name
	if rest, ok := strings.CutPrefix(name, "mcp__"); ok {
		server = rest
		if i := strings.Index(rest, "__"); i >= 0 {
			server = rest[:i]
		}
	}
	for _, s := range discoveryServers {
		if strings.Contains(server, s) {
			return true
		}
	}
	return false
}

// classifyCommand resolves a Bash command to Explore or Run: strip the leading
// `cd … &&` / `env` / `VAR=value` / `(` noise, take the first real token, and
// look its basename up in exploreCmds — minus the two edit-by-shell escapes.
// An empty or cd-only command is Run: it changed no file, but it read none
// either, and counting it as exploration would flatter the metric.
func classifyCommand(command string) Kind {
	toks := stripPrefixes(tokenize(command))
	if len(toks) == 0 {
		return Run
	}
	cmd := baseCmd(toks[0])
	if !exploreCmds[cmd] {
		return Run
	}
	if mutatesTree(cmd, toks[1:]) {
		return Run
	}
	return Explore
}

// baseCmd reduces a command word to its binary name ("/usr/bin/grep" → "grep")
// so an absolute or relative invocation classifies like the bare one.
func baseCmd(tok string) string {
	return path.Base(tok)
}

// mutatesTree spots the reader binaries used as writers: `sed -i` (in-place
// substitution) and `find … -exec/-delete` (running or deleting what it found).
func mutatesTree(cmd string, args []string) bool {
	switch cmd {
	case "sed":
		for _, a := range args {
			if a == "--in-place" || strings.HasPrefix(a, "--in-place=") {
				return true
			}
			// -i, -i.bak, and clustered short flags like -ri.
			if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") &&
				strings.ContainsRune(flagCluster(a), 'i') {
				return true
			}
		}
	case "find":
		for _, a := range args {
			switch a {
			case "-exec", "-execdir", "-delete", "-ok", "-okdir":
				return true
			}
		}
	}
	return false
}

// flagCluster returns the letters of a short-flag token up to the first
// non-letter, so "-ri" yields "ri" while "-i.bak" yields "i" and the ".bak"
// suffix is not mistaken for more flags.
func flagCluster(a string) string {
	s := strings.TrimPrefix(a, "-")
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return s[:i]
		}
	}
	return s
}

// separators are the unquoted operators that end one command in a shell line.
var separators = map[string]bool{"&&": true, "||": true, ";": true, ";;": true, "|": true, "&": true}

// stripPrefixes drops the leading noise that hides the real command: grouping
// parens/braces, `env`, `VAR=value` assignments, and a `cd <path> &&` hop
// (whose whole clause is discarded — the interesting command is after the
// separator). Returns nil when nothing but a `cd` was there.
func stripPrefixes(toks []string) []string {
	for len(toks) > 0 {
		t := toks[0]
		switch {
		case t == "(" || t == "{" || t == "!":
			toks = toks[1:]
		case t == "env":
			toks = toks[1:]
		case isAssignment(t):
			toks = toks[1:]
		case t == "cd":
			i := 1
			for i < len(toks) && !separators[toks[i]] {
				i++
			}
			if i >= len(toks) {
				return nil // the command was only a cd
			}
			toks = toks[i+1:]
		default:
			return toks
		}
	}
	return toks
}

// isAssignment reports whether a token is a `NAME=value` environment prefix.
func isAssignment(t string) bool {
	i := strings.IndexByte(t, '=')
	if i <= 0 {
		return false
	}
	for j := 0; j < i; j++ {
		c := t[j]
		letter := c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		digit := j > 0 && c >= '0' && c <= '9'
		if !letter && !digit {
			return false
		}
	}
	return true
}

// tokenize splits a shell command into the words the classifier needs: quotes
// and backslashes are honoured (so `sed -i '' …` keeps its empty argument and
// `-exec rm {} \;` ends in a bare `;`), and the operators ; & | ( ) { } come
// out as their own tokens. It is NOT a shell parser — it expands nothing — it
// only has to find the first word of a command.
func tokenize(s string) []string {
	var out []string
	var cur strings.Builder
	started := false
	flush := func() {
		if started || cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
			started = false
		}
	}
	var quote byte // 0, '\'' or '"'
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote = c
			started = true
		case c == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
			started = true
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			flush()
		case c == ';' || c == '&' || c == '|':
			flush()
			j := i
			for j+1 < len(s) && s[j+1] == c {
				j++
			}
			out = append(out, s[i:j+1])
			i = j
		case c == '(' || c == ')' || c == '{' || c == '}':
			flush()
			out = append(out, string(c))
		default:
			cur.WriteByte(c)
			started = true
		}
	}
	flush()
	return out
}
