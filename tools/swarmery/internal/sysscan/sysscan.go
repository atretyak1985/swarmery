// Package sysscan is the phase-4 system-registry scanner (Stage 1): a
// READ-ONLY scanner over the agent-system config surface — agents, skills,
// hooks, commands, plugin cache, overlays — that indexes items into the
// registry tables (agents/skills/hooks/commands), versions agent and skill
// content into *_versions by sha256, and publishes system_item_updated
// notes on the ingest bus.
//
// Parsing follows docs/system-config-format.md (the step-01 discovery doc)
// verbatim — no field is invented beyond that spec. Tolerant by contract: a
// broken frontmatter, unreadable file, or unparseable settings.json warns,
// records a config_lint_findings parse_error row, and degrades — one bad
// file never stops the scan and the scanner never panics.
//
// It NEVER writes to ~/.claude or any project's .claude/ — its only output
// is DB rows and bus notifications.
package sysscan

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

// Notification kinds carried in ingest.Notification.Kind for
// NoteSystemItemUpdated — mirrored by SystemItemKind in web/src/api/types.ts.
const (
	KindAgent   = "agent"
	KindSkill   = "skill"
	KindHook    = "hook"
	KindCommand = "command"
)

// DefaultRescanInterval is the fallback full-rescan cadence. Config files
// change on human cadence; unchanged files are hash-skipped, so a full pass
// is cheap.
const DefaultRescanInterval = 30 * time.Second

// DefaultClaudeDir resolves ~/.claude.
func DefaultClaudeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".claude"
	}
	return filepath.Join(home, ".claude")
}

// Config tunes the scanner. Zero values fall back to defaults; OverlaysDir
// is optional ("" disables the overlays listing).
type Config struct {
	ClaudeDir      string        // user tier + plugin cache root (default ~/.claude)
	OverlaysDir    string        // overlays/ dir listed in the scan report (optional)
	RescanInterval time.Duration // fallback full-rescan cadence (default 30s)

	// Lint thresholds (step-04). Precedence: explicit config value > the
	// SWARMERY_LINT_* env override > the package default (lint.go).
	MinSkillDescription int // skill_short_description: min description runes (default 40; env SWARMERY_LINT_MIN_SKILL_DESC)
	MaxClaudeMDTokens   int // claude_md_oversized: max estimated tokens, len/4 (default 2500; env SWARMERY_LINT_MAX_CLAUDE_MD_TOKENS)
}

func (c Config) withDefaults() Config {
	if c.ClaudeDir == "" {
		c.ClaudeDir = DefaultClaudeDir()
	}
	if c.RescanInterval <= 0 {
		c.RescanInterval = DefaultRescanInterval
	}
	if c.MinSkillDescription <= 0 {
		c.MinSkillDescription = envInt(EnvMinSkillDescription, DefaultMinSkillDescription)
	}
	if c.MaxClaudeMDTokens <= 0 {
		c.MaxClaudeMDTokens = envInt(EnvMaxClaudeMDTokens, DefaultMaxClaudeMDTokens)
	}
	return c
}

// SourceCounts splits one item kind's count by where the items came from.
type SourceCounts struct {
	Global  int // <ClaudeDir> tier
	Project int // <project>/.claude tier
	Plugin  int // plugin cache tier
}

func (c SourceCounts) String() string {
	return fmt.Sprintf("global=%d project=%d plugin=%d", c.Global, c.Project, c.Plugin)
}

// Stats reports one scan pass. Item counts are the present state (totals,
// not deltas — the scan is a converging upsert); NewVersions/Deleted are
// this pass's deltas.
type Stats struct {
	Agents       SourceCounts
	Skills       SourceCounts
	Commands     SourceCounts
	Hooks        int      // hook entries present across all parsed settings files
	HooksManaged int      // …of which carry the "swarmery hook" marker
	NewVersions  int      // agent/skill current-version changes this pass
	Deleted      int      // items soft-deleted this pass (vanished files)
	ParseErrors  int      // items kept with a parse_error lint finding
	Warnings     int      // tolerated stumbles this pass
	Overlays     []string // overlay file paths (templates are read on-demand in the API, step-05)
}

func (s Stats) String() string {
	return fmt.Sprintf("agents(%s) skills(%s) commands(%s) hooks=%d(managed=%d) versions+=%d deleted=%d parse_errors=%d overlays=%d warnings=%d",
		s.Agents, s.Skills, s.Commands, s.Hooks, s.HooksManaged,
		s.NewVersions, s.Deleted, s.ParseErrors, len(s.Overlays), s.Warnings)
}

// Scanner runs idempotent scan passes against one DB.
type Scanner struct {
	db  *sql.DB
	cfg Config
	bus *ingest.Bus // may be nil (no live notifications)
}

// New builds a scanner; bus may be nil.
func New(db *sql.DB, cfg Config, bus *ingest.Bus) *Scanner {
	return &Scanner{db: db, cfg: cfg.withDefaults(), bus: bus}
}

// publish emits one system_item_updated note (no-op without a bus).
func (s *Scanner) publish(kind string, id int64) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(ingest.Notification{Type: ingest.NoteSystemItemUpdated, Kind: kind, ItemID: id})
}

// sha256Hex is the content-hash helper (same style as the ingest dedup key).
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }

// stem returns the file name without its extension — the command/agent name
// fallback (commands have no `name:` key at all, format doc §4).
func stem(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// Scan performs one full idempotent pass over every source. READ-ONLY on
// disk; every DB write converges (re-runs on unchanged config are no-ops).
func (s *Scanner) Scan() (Stats, error) {
	var st Stats
	warn := func(format string, args ...any) {
		st.Warnings++
		log.Printf("warn: sysscan: "+format, args...)
	}

	projects, err := s.loadProjects()
	if err != nil {
		return st, fmt.Errorf("load projects: %w", err)
	}
	plugins := pluginRoots(s.cfg.ClaudeDir, warn)

	seenAgents := map[int64]bool{}
	seenSkills := map[int64]bool{}
	seenCommands := map[int64]bool{}

	// ── agents: agents/**/*.md, recursive (project trees nest, §1.1) ──────
	globalSrc := source{scope: "global", origin: "local"}
	for _, p := range walkMD(filepath.Join(s.cfg.ClaudeDir, "agents"), warn) {
		s.scanAgentFile(mdFile{path: p, src: globalSrc}, &st, &st.Agents.Global, seenAgents, warn)
	}
	for _, pr := range projects {
		src := source{scope: "project", projectID: sql.NullInt64{Int64: pr.id, Valid: true}, origin: "local"}
		for _, p := range walkMD(filepath.Join(pr.path, ".claude", "agents"), warn) {
			s.scanAgentFile(mdFile{path: p, src: src}, &st, &st.Agents.Project, seenAgents, warn)
		}
	}
	for _, pl := range plugins {
		src := source{scope: "global", origin: "plugin", plugin: pl.plugin}
		for _, p := range walkMD(filepath.Join(pl.root, "agents"), warn) {
			s.scanAgentFile(mdFile{path: p, src: src}, &st, &st.Agents.Plugin, seenAgents, warn)
		}
	}

	// ── skills: dirs holding a SKILL.md; plugins scan skills/*/ ONLY (§2.2) ─
	for _, d := range walkSkillDirs(filepath.Join(s.cfg.ClaudeDir, "skills"), warn) {
		s.scanSkillDir(skillDir{dir: d, src: globalSrc}, &st, &st.Skills.Global, seenSkills, warn)
	}
	for _, pr := range projects {
		src := source{scope: "project", projectID: sql.NullInt64{Int64: pr.id, Valid: true}, origin: "local"}
		for _, d := range walkSkillDirs(filepath.Join(pr.path, ".claude", "skills"), warn) {
			s.scanSkillDir(skillDir{dir: d, src: src}, &st, &st.Skills.Project, seenSkills, warn)
		}
	}
	for _, pl := range plugins {
		src := source{scope: "global", origin: "plugin", plugin: pl.plugin}
		for _, d := range globSkillDirs(filepath.Join(pl.root, "skills")) {
			s.scanSkillDir(skillDir{dir: d, src: src}, &st, &st.Skills.Plugin, seenSkills, warn)
		}
	}

	// ── commands: commands/*.md — name = file stem (§4) ───────────────────
	for _, p := range walkMD(filepath.Join(s.cfg.ClaudeDir, "commands"), warn) {
		s.scanCommandFile(mdFile{path: p, src: globalSrc}, &st, &st.Commands.Global, seenCommands, warn)
	}
	for _, pr := range projects {
		src := source{scope: "project", projectID: sql.NullInt64{Int64: pr.id, Valid: true}, origin: "local"}
		for _, p := range walkMD(filepath.Join(pr.path, ".claude", "commands"), warn) {
			s.scanCommandFile(mdFile{path: p, src: src}, &st, &st.Commands.Project, seenCommands, warn)
		}
	}
	for _, pl := range plugins {
		src := source{scope: "global", origin: "plugin", plugin: pl.plugin}
		for _, p := range walkMD(filepath.Join(pl.root, "commands"), warn) {
			s.scanCommandFile(mdFile{path: p, src: src}, &st, &st.Commands.Plugin, seenCommands, warn)
		}
	}

	// ── hooks: settings files, per-source_file delete-and-insert (§3) ─────
	cands := settingsCandidates(s.cfg.ClaudeDir, projects)
	candSet := map[string]bool{}
	for _, c := range cands {
		candSet[c.path] = true
		s.scanHooksFile(c, &st, warn)
	}
	s.sweepStaleHookFiles(candSet, &st, warn)

	// ── soft-delete sweep: rows whose file vanished under a scanned root ──
	roots := []string{s.cfg.ClaudeDir}
	for _, pr := range projects {
		roots = append(roots, pr.path)
	}
	s.sweepDeleted(KindAgent, "agents", "file_path", seenAgents, roots, &st, warn)
	s.sweepDeleted(KindSkill, "skills", "dir_path", seenSkills, roots, &st, warn)
	s.sweepDeleted(KindCommand, "commands", "file_path", seenCommands, roots, &st, warn)

	// ── overlays: paths only — the API reads them on demand (step-05) ─────
	st.Overlays = overlayFiles(s.cfg.OverlaysDir, warn)

	return st, nil
}

// Run scans immediately, then rescans on fsnotify changes (debounced) and on
// every RescanInterval tick until ctx ends. Scan errors are logged, never
// fatal — the loop always keeps ticking (pattern: ingest/tail + wsingest).
func (s *Scanner) Run(ctx context.Context) error {
	scan := func() {
		st, err := s.Scan()
		if err != nil {
			log.Printf("error: sysscan: %v", err)
			return
		}
		log.Printf("sysscan: %s", st)
		// Lint is the step-04 post-pass: it re-evaluates every rule against
		// the registry the scan just converged (design §3.5).
		ls, err := s.Lint()
		if err != nil {
			log.Printf("error: sysscan lint: %v", err)
			return
		}
		log.Printf("sysscan lint: %s", ls)
	}
	scan()

	// fsnotify is a latency optimization; on any setup failure the scanner
	// silently degrades to rescan-only operation. The plugin cache is NOT
	// watched (it changes only on /plugin update) — the periodic rescan
	// covers it.
	var fsEvents chan fsnotify.Event
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("warn: sysscan: fsnotify unavailable (%v) — rescan-only mode", err)
	} else {
		defer watcher.Close()
		s.addWatches(watcher)
		fsEvents = make(chan fsnotify.Event, 256)
		go func() {
			for {
				select {
				case ev, ok := <-watcher.Events:
					if !ok {
						return
					}
					select {
					case fsEvents <- ev:
					default: // burst overflow — rescan covers it
					}
				case werr, ok := <-watcher.Errors:
					if !ok {
						return
					}
					log.Printf("warn: sysscan: fsnotify: %v", werr)
				}
			}
		}()
	}

	rescanT := time.NewTicker(s.cfg.RescanInterval)
	defer rescanT.Stop()
	debounceT := time.NewTicker(500 * time.Millisecond)
	defer debounceT.Stop()
	dirty := false

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case ev := <-fsEvents:
			if ev.Op.Has(fsnotify.Create) {
				if fi, statErr := os.Stat(ev.Name); statErr == nil && fi.IsDir() {
					_ = watcher.Add(ev.Name) // new subdir — keep the tree covered
				}
			}
			if !ev.Op.Has(fsnotify.Chmod) {
				dirty = true
			}

		case <-debounceT.C:
			if dirty {
				dirty = false
				scan()
			}

		case <-rescanT.C:
			scan()
		}
	}
}

// addWatches registers the user tier plus every project's .claude config
// dirs. Failures are tolerated silently — the rescan ticker is the safety
// net (macOS kqueue needs per-directory watches and can hit fd limits).
func (s *Scanner) addWatches(w *fsnotify.Watcher) {
	addTree := func(root string) {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if strings.HasPrefix(d.Name(), ".") && path != root {
					return fs.SkipDir
				}
				_ = w.Add(path)
			}
			return nil
		})
	}
	_ = w.Add(s.cfg.ClaudeDir) // settings.json lives at the root
	for _, sub := range []string{"agents", "skills", "commands"} {
		addTree(filepath.Join(s.cfg.ClaudeDir, sub))
	}
	projects, err := s.loadProjects()
	if err != nil {
		return
	}
	for _, pr := range projects {
		_ = w.Add(filepath.Join(pr.path, ".claude"))
		for _, sub := range []string{"agents", "skills", "commands"} {
			addTree(filepath.Join(pr.path, ".claude", sub))
		}
	}
}
