// Package agentsync generates project-local agent overrides that narrow a
// single frontmatter key of an upstream plugin agent.
//
// Claude Code — not swarmery — resolves `isolation:` out of an agent file's
// frontmatter, and the only supported way to override a plugin agent is a file
// carrying the same `name:` in the project's own `.claude/agents/`. That
// override replaces the agent WHOLE, never per key, so "narrow one key without
// forking" is unreachable declaratively. This package makes the fork
// *generated* instead of handwritten: a byte-for-byte copy of the upstream file
// with exactly one frontmatter key changed, stamped with the upstream's content
// hash so drift becomes detectable (`swarmery agents sync --check`).
//
// The project declares the narrowing in its own `.claude/settings.json`:
//
//	"swarmery": { "agents": { "implementation-agent": { "isolation": "none" } } }
//
// Nothing else about the agent may be overridden here. An override that names
// an unknown isolation value is an error with an explanation, never a silent
// no-op — a silently ignored isolation override is indistinguishable from a
// working one until an agent writes to the wrong tree.
package agentsync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/marketplace"
)

// DefaultMarketplace is the marketplace whose packs ship the agents a project
// may narrow. Overridable so a fork can point at its own catalog.
const DefaultMarketplace = "swarmery"

// Generated frontmatter keys. They are the file's provenance: `source_sha` is
// what `--check` compares, the rest is for the human who opens the file.
const (
	keyGeneratedBy = "generated_by"
	keySource      = "source"
	keySourceSHA   = "source_sha"
	keyGeneratedAt = "generated_at"

	// generatorID is the value of generated_by — also the marker that tells a
	// reader (and Check) the file is ours and not a handwritten fork.
	generatorID = "swarmery agents sync"
)

// isolationKey is the one frontmatter key a project may override.
const isolationKey = "isolation"

// validIsolation lists every accepted value of swarmery.agents.<name>.isolation.
// Kept in sync with Claude Code's own vocabulary for the key.
var validIsolation = []string{"none", "worktree"}

// AgentOverride is one project-declared narrowing, already validated.
type AgentOverride struct {
	Name      string
	Isolation string
}

// projectSettings is the subset of .claude/settings.json this package reads.
// Every other key is ignored and left untouched — this package never writes
// settings.json.
type projectSettings struct {
	Swarmery struct {
		Agents map[string]rawOverride `json:"agents"`
	} `json:"swarmery"`
}

// rawOverride keeps Isolation a pointer so "key absent" is distinguishable from
// "key present and empty"; both are errors, but they deserve different words.
type rawOverride struct {
	Isolation *string `json:"isolation"`
}

// SettingsPath is the project file the overrides are declared in.
func SettingsPath(projectDir string) string {
	return filepath.Join(projectDir, ".claude", "settings.json")
}

// OverridePath is where the generated agent file for name lands.
func OverridePath(projectDir, name string) string {
	return filepath.Join(projectDir, ".claude", "agents", name+".md")
}

// ReadOverrides parses swarmery.agents out of the project's settings.json,
// sorted by agent name so output and diffs are stable.
//
// A project with no settings.json, no `swarmery` block, or no `agents` block
// declares nothing: that is not an error, it is the normal case for the other
// projects on the machine, and it must produce no files.
func ReadOverrides(projectDir string) ([]AgentOverride, error) {
	path := SettingsPath(projectDir)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var ps projectSettings
	if err := json.Unmarshal(data, &ps); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(ps.Swarmery.Agents) == 0 {
		return nil, nil
	}
	out := make([]AgentOverride, 0, len(ps.Swarmery.Agents))
	for name, ro := range ps.Swarmery.Agents {
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("%s: swarmery.agents has an entry with an empty agent name", path)
		}
		if ro.Isolation == nil {
			return nil, fmt.Errorf(
				"%s: swarmery.agents.%s declares no %q — it is the only key this override supports",
				path, name, isolationKey)
		}
		if !isValidIsolation(*ro.Isolation) {
			return nil, fmt.Errorf(
				"%s: swarmery.agents.%s.%s = %q is not a valid isolation; allowed: %s",
				path, name, isolationKey, *ro.Isolation, strings.Join(validIsolation, ", "))
		}
		out = append(out, AgentOverride{Name: name, Isolation: *ro.Isolation})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func isValidIsolation(v string) bool {
	for _, ok := range validIsolation {
		if v == ok {
			return true
		}
	}
	return false
}

// Source is one upstream agent file inside a marketplace clone.
type Source struct {
	// Path is the absolute path the bytes were read from.
	Path string
	// Rel is the same file relative to the marketplace root
	// (e.g. "plugins/core/agents/implementation-agent.md") — stable across
	// machines, which is why it, and not Path, is stamped into the output.
	Rel string
	// Data is the file verbatim.
	Data []byte
	// SHA is ShortSHA(Data): what --check compares.
	SHA string
}

// ShortSHA is the provenance stamp: the first 12 hex characters of the file's
// sha256. Twelve characters is the same budget the repo's `docs.source_sha`
// frontmatter already spends, and collisions at that width need ~16M files.
func ShortSHA(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:12]
}

// Locator finds upstream agent files in a marketplace clone.
type Locator struct {
	// ClaudeDir anchors the plugin install (default ~/.claude).
	ClaudeDir string
	// Marketplace is the marketplace name (default DefaultMarketplace).
	Marketplace string
}

// Find resolves the upstream file for an agent by scanning every pack in the
// marketplace catalog for `<pack>/agents/<name>.md`.
//
// The catalog — not the directory listing — is the source of truth for which
// packs exist, so a stale directory left behind by an uninstalled pack cannot
// become somebody's upstream.
func (l Locator) Find(name string) (Source, error) {
	cat, err := marketplace.Read(l.claudeDir(), l.marketplace())
	if err != nil {
		return Source{}, fmt.Errorf("read marketplace %q: %w", l.marketplace(), err)
	}
	for _, p := range cat.Plugins {
		rel := filepath.Join(filepath.Clean(p.Source), "agents", name+".md")
		if strings.HasPrefix(rel, "..") {
			continue // a manifest source escaping the clone is not ours to read
		}
		abs := filepath.Join(cat.Root, rel)
		data, err := os.ReadFile(abs)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return Source{}, fmt.Errorf("read %s: %w", abs, err)
		}
		if err := assertAgentName(abs, data, name); err != nil {
			return Source{}, err
		}
		return Source{Path: abs, Rel: filepath.ToSlash(rel), Data: data, SHA: ShortSHA(data)}, nil
	}
	return Source{}, fmt.Errorf(
		"agent %q not found in any pack of marketplace %q (%s) — is the pack installed?",
		name, l.marketplace(), cat.Root)
}

func (l Locator) claudeDir() string {
	if l.ClaudeDir != "" {
		return l.ClaudeDir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}

func (l Locator) marketplace() string {
	if l.Marketplace != "" {
		return l.Marketplace
	}
	return DefaultMarketplace
}

// assertAgentName guards the one assumption the whole override rests on: Claude
// Code matches overrides by the frontmatter `name:`, not by filename. A pack
// whose file name has drifted from its `name:` would generate an override that
// silently overrides nothing.
func assertAgentName(path string, data []byte, want string) error {
	front, _, err := splitFrontmatter(data)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	got, ok := lookupKey(front, "name")
	if !ok {
		return fmt.Errorf("%s: upstream agent has no `name:` in its frontmatter", path)
	}
	if got != want {
		return fmt.Errorf("%s: upstream agent declares name %q but the file is %s.md — an override keyed on %q would match nothing",
			path, got, want, want)
	}
	return nil
}

// Render returns the generated override for src with isolation set to iso.
//
// Everything outside the frontmatter's isolation line, the appended provenance
// block, and the leading warning comment is copied byte for byte: the override
// has to keep behaving like the agent it narrows.
func Render(src Source, iso string, day time.Time) ([]byte, error) {
	if !isValidIsolation(iso) {
		return nil, fmt.Errorf("isolation %q is not valid; allowed: %s", iso, strings.Join(validIsolation, ", "))
	}
	front, body, err := splitFrontmatter(src.Data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", src.Path, err)
	}
	if _, ok := lookupKey(front, keyGeneratedBy); ok {
		return nil, fmt.Errorf("%s: upstream file is itself generated (%s: present) — generating from it would compound the fork",
			src.Path, keyGeneratedBy)
	}
	front = setKey(front, isolationKey, iso)
	front = append(front,
		keyGeneratedBy+": "+generatorID,
		keySource+": "+src.Rel,
		keySourceSHA+": "+src.SHA,
		keyGeneratedAt+": "+day.Format("2006-01-02"),
	)

	var b strings.Builder
	b.WriteString("---\n")
	for _, line := range front {
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("---\n")
	b.WriteString(warningLine(src.Rel))
	b.WriteString("\n")
	// Exactly one blank line between the warning and the body — most agent
	// bodies already open with one, and doubling it would show up in the
	// rendered markdown of every generated file.
	if !strings.HasPrefix(body, "\n") {
		b.WriteString("\n")
	}
	b.WriteString(body)
	return []byte(b.String()), nil
}

// warningLine is the first thing a human sees when opening the file. It names
// the upstream path rather than a machine-specific checkout, so the sentence
// stays true on every machine that has the marketplace.
func warningLine(rel string) string {
	return "<!-- ЗГЕНЕРОВАНО `" + generatorID + "`. Не редагувати руками: зміни перезапишуться. " +
		"Правити джерело в апстрімі: " + rel + ". -->"
}

// splitFrontmatter cuts a `---`-delimited YAML frontmatter off the head of an
// agent file. front is the frontmatter's lines without their terminators; body
// is everything after the closing `---\n`, verbatim.
func splitFrontmatter(data []byte) (front []string, body string, err error) {
	const delim = "---\n"
	s := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(s, delim) {
		return nil, "", errors.New("no YAML frontmatter: file does not start with `---`")
	}
	rest := s[len(delim):]
	end := strings.Index(rest, "\n"+delim)
	if end < 0 {
		return nil, "", errors.New("unterminated YAML frontmatter: no closing `---`")
	}
	return strings.Split(rest[:end], "\n"), rest[end+len("\n")+len(delim):], nil
}

// lookupKey returns the value of a top-level (unindented) scalar frontmatter
// key. Nested keys are invisible to it on purpose: `docs.source_sha` must not
// answer a question asked about `source_sha`.
func lookupKey(front []string, key string) (string, bool) {
	for _, line := range front {
		if v, ok := cutKey(line, key); ok {
			return v, true
		}
	}
	return "", false
}

// setKey replaces the first top-level occurrence of key, or appends it when the
// upstream file leaves the key at its default.
func setKey(front []string, key, val string) []string {
	for i, line := range front {
		if _, ok := cutKey(line, key); ok {
			out := make([]string, len(front))
			copy(out, front)
			out[i] = key + ": " + val
			return out
		}
	}
	return append(front, key+": "+val)
}

func cutKey(line, key string) (string, bool) {
	rest, ok := strings.CutPrefix(line, key+":")
	if !ok {
		return "", false
	}
	return strings.TrimSpace(rest), true
}
