package ingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Config tunes the live ingest pipeline. Zero values fall back to defaults.
type Config struct {
	// ProjectsRoots are the Claude Code transcript roots to ingest, in
	// priority order. Plural because ONE machine can run several Claude Code
	// subscriptions side by side (CLAUDE_CONFIG_DIR): every config dir
	// ~/.claude, ~/.claude-<account>, … owns its own projects/ subtree, and a
	// single-root daemon is blind to all but one of them. A one-element list
	// behaves exactly like the old single root.
	ProjectsRoots  []string      // e.g. [~/.claude/projects, ~/.claude-work/projects]
	RescanInterval time.Duration // fallback full rescan cadence (default 2s)
	StatusInterval time.Duration // session-status ticker cadence (default 10s)
	Thresholds     Thresholds    // active/idle windows (default 2m/30m)
	Exclude        ExcludeList   // project path globs to skip (see exclude.go)

	// OnSessionTerminal fires once per status-ticker transition to
	// 'completed', with the COUNT of the session's status='error' events —
	// the notify wiring point (main.go emits session_completed /
	// session_error through it). Deliberately NOT called for procwatch- or
	// approvals-driven transitions (v1 scope). May be nil.
	OnSessionTerminal func(sessionID int64, errorCount int)

	// OnAwaitingReply fires once per status-ticker transition to
	// 'awaiting_reply' — the reply-extractor wiring point (main.go sets it
	// only under --reply-extract). It runs on the ticker goroutine, so it
	// must not block. May be nil.
	OnAwaitingReply func(sessionID int64)
}

func (c Config) withDefaults() Config {
	if c.RescanInterval <= 0 {
		c.RescanInterval = 2 * time.Second
	}
	if c.StatusInterval <= 0 {
		c.StatusInterval = 10 * time.Second
	}
	c.Thresholds = c.Thresholds.orDefaults()
	return c
}

// ErrNoProjectsRoots is returned when NOT ONE configured projects root exists
// on this machine — the multi-root spelling of the old single-root "projects
// root: no such file or directory" refusal. Individually missing roots are
// only warnings (see Pipeline.Run).
var ErrNoProjectsRoots = errors.New("no configured projects root exists")

// ExistingRoots partitions configured projects roots into the ones that are
// directories on this machine and the ones that are not. Shared by the daemon
// (Pipeline.Run) and the one-shot `swarmery backfill` so both apply the same
// tolerate-some / refuse-all rule.
func ExistingRoots(roots []string) (present, missing []string) {
	for _, r := range roots {
		if fi, err := os.Stat(r); err == nil && fi.IsDir() {
			present = append(present, r)
			continue
		}
		missing = append(missing, r)
	}
	return present, missing
}

// Metrics are cumulative pipeline counters (logged periodically).
type Metrics struct {
	Files        int64 // distinct files ingested at least once
	Lines        int64 // parseable records processed
	SkippedLines int64 // malformed lines skipped
	Events       int64 // events rows created
	Errors       int64 // per-file errors (never fatal to the pipeline)
}

func (m Metrics) String() string {
	return fmt.Sprintf("files=%d lines=%d skipped=%d events=%d errors=%d",
		m.Files, m.Lines, m.SkippedLines, m.Events, m.Errors)
}

type fileState struct {
	offset int64
	inode  int64
}

// Pipeline is the resilient live ingest loop: full backfill, fsnotify tail
// with a periodic rescan safety net, and a session-status ticker. No
// single-file error ever stops it.
type Pipeline struct {
	db  *sql.DB
	cfg Config
	bus *Bus

	mu       sync.Mutex
	state    map[string]fileState // in-memory mirror of file_offsets
	errUntil map[string]time.Time // per-file error backoff
	metrics  Metrics
}

const errBackoff = 15 * time.Second

// NewPipeline builds a pipeline; bus may be nil (no live notifications).
func NewPipeline(db *sql.DB, cfg Config, bus *Bus) *Pipeline {
	return &Pipeline{
		db:       db,
		cfg:      cfg.withDefaults(),
		bus:      bus,
		state:    map[string]fileState{},
		errUntil: map[string]time.Time{},
	}
}

// Metrics returns a snapshot of the cumulative counters.
func (p *Pipeline) Metrics() Metrics {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.metrics
}

// scanned is one discovered transcript together with the projects root it was
// found under. The root is the ACCOUNT dimension on a multi-subscription
// machine (see Config.ProjectsRoots) and has to travel with the path: two
// roots can hold same-named project dirs, and the file path alone does not say
// which subscription produced it.
type scanned struct {
	path string
	root string
}

// discover lists every transcript under every projects root: per root, main
// transcripts first, then sidechains — so parent subagent_start events exist
// before their sidechain files are ingested (§1/§7 layout). Roots are walked
// in configured order and never interleave, so the ordering guarantee holds
// per root, which is all it ever needed (a sidechain lives under its main
// transcript's root by construction). Excluded project dirs are dropped here,
// covering both the backfill and the rescan safety net.
func (p *Pipeline) discover() []scanned {
	var out []scanned
	for _, root := range p.cfg.ProjectsRoots {
		mains, _ := filepath.Glob(filepath.Join(root, "*", "*.jsonl"))
		sides, _ := filepath.Glob(filepath.Join(root, "*", "*", "subagents", "agent-*.jsonl"))
		sort.Strings(mains)
		sort.Strings(sides)
		for _, f := range append(mains, sides...) {
			if p.excludedUnder(root, f) {
				continue
			}
			out = append(out, scanned{path: f, root: root})
		}
	}
	return out
}

// rootFor resolves which configured projects root a path came from. fsnotify
// hands us bare paths (no scan context), so the mapping must be recoverable
// from the path alone; the LONGEST matching root wins, so a root nested inside
// another one still attributes its own files. "" when the path is under none
// of the roots.
func (p *Pipeline) rootFor(path string) string {
	best := ""
	for _, root := range p.cfg.ProjectsRoots {
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if len(root) > len(best) {
			best = root
		}
	}
	return best
}

// excludedUnder reports whether a transcript path lives under an excluded
// project dir (first path element below ITS projects root, flattened-name
// match). An empty root (path under no configured root) is never excluded —
// the same posture the single-root code had for out-of-root paths.
func (p *Pipeline) excludedUnder(root, path string) bool {
	if root == "" || len(p.cfg.Exclude) == 0 {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return false
	}
	dir, _, _ := strings.Cut(rel, string(filepath.Separator))
	return p.cfg.Exclude.MatchProjectDir(dir)
}

// Backfill runs one full scan of the projects root, ingesting every
// transcript from its persisted offset (0 on first sight). Per-file errors
// are logged and counted, never fatal.
func (p *Pipeline) Backfill(ctx context.Context) Metrics {
	start := time.Now()
	// Heal legacy NULL project names first: unchanged transcripts are offset
	// no-ops (TailFile returns early at size == offset), so pre-existing
	// projects would otherwise never get their derived display name.
	if healed, err := HealProjectNames(p.db); err != nil {
		log.Printf("warn: ingest: heal project names: %v", err)
	} else if healed > 0 {
		log.Printf("ingest: healed %d project name(s) from path", healed)
	}
	// Merge phantom projects (worktree / in-repo-subdir cwds) into their
	// canonical parent: rows minted before path canonicalization existed
	// would otherwise sit on the Projects page forever.
	if merged, err := HealProjectAttribution(p.db); err != nil {
		log.Printf("warn: ingest: heal project attribution: %v", err)
	} else if merged > 0 {
		log.Printf("ingest: merged %d phantom project(s) into their parents", merged)
	}
	// Heal stub sessions ('(unknown)' project / empty cwd / empty started_at):
	// unchanged transcripts are offset no-ops, so the per-batch upsert heal
	// would never see them again — re-attribute from the transcript files and
	// emit session_updated so open dashboards converge.
	if ids, err := HealStubSessions(p.db, p.cfg.ProjectsRoots, p.cfg.Exclude); err != nil {
		log.Printf("warn: ingest: heal stub sessions: %v", err)
	} else if len(ids) > 0 {
		log.Printf("ingest: healed %d stub session(s) from transcripts", len(ids))
		if p.bus != nil {
			for _, id := range ids {
				p.bus.Publish(Notification{Type: NoteSessionUpdated, SessionID: id})
			}
		}
	}
	// Heal Write-create rows recorded before their diff was synthesised from
	// `content` (diff ''): unchanged transcripts are offset no-ops, so the
	// Diffs tab would show "create +0 −0" for them forever.
	if healed, err := HealCreateDiffs(p.db, p.cfg.ProjectsRoots); err != nil {
		log.Printf("warn: ingest: heal create diffs: %v", err)
	} else if healed > 0 {
		log.Printf("ingest: healed %d create diff(s) from transcripts", healed)
	}
	files := p.discover()
	for _, f := range files {
		if ctx.Err() != nil {
			break
		}
		p.tailOne(f.path, false)
	}
	m := p.Metrics()
	log.Printf("ingest: backfill of %s done in %s — scanned=%d %s",
		strings.Join(p.cfg.ProjectsRoots, ", "), time.Since(start).Round(time.Millisecond), len(files), m)
	return m
}

// tailOne incrementally ingests a single file and publishes bus notifications
// for whatever it produced. Safe to call for unchanged files (cheap no-op).
func (p *Pipeline) tailOne(path string, logPickup bool) {
	// Resolve the origin root once: it gates exclusion, tags the ingest with
	// the account dimension, and shortens the log line.
	root := p.rootFor(path)
	if p.excludedUnder(root, path) {
		return // fsnotify can deliver excluded paths directly — filter here too
	}
	p.mu.Lock()
	if until, ok := p.errUntil[path]; ok && time.Now().Before(until) {
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()

	res, err := TailFile(p.db, path, root, p.cfg.Thresholds)
	now := time.Now()

	p.mu.Lock()
	if err != nil {
		p.metrics.Errors++
		p.errUntil[path] = now.Add(errBackoff)
		p.mu.Unlock()
		log.Printf("warn: ingest: %s: %v (backing off %s)", path, err, errBackoff)
		return
	}
	delete(p.errUntil, path)
	if _, seen := p.state[res.Path]; !seen {
		p.metrics.Files++
	}
	ino := int64(0)
	if fi, statErr := os.Stat(res.Path); statErr == nil {
		ino = inodeOf(fi)
	}
	p.state[res.Path] = fileState{offset: res.NextOffset, inode: ino}
	p.metrics.Lines += int64(res.Lines)
	p.metrics.SkippedLines += int64(res.SkippedLines)
	p.metrics.Events += int64(len(res.NewEventIDs))
	p.mu.Unlock()

	if res.Reset {
		log.Printf("ingest: %s: offset reset to 0 (file recreated/truncated) — dedup absorbs the re-read", path)
	}
	if res.Lines > 0 && logPickup {
		log.Printf("ingest: %s: +%d lines, +%d events (lag %s)",
			shortPath(path, root), res.Lines, len(res.NewEventIDs),
			lagFrom(res.LastTS, now).Round(time.Millisecond))
	}

	if p.bus == nil || res.SessionID == 0 || res.Lines == 0 {
		return
	}
	typ := NoteSessionUpdated
	if res.SessionCreated {
		typ = NoteSessionStarted
	}
	p.bus.Publish(Notification{Type: typ, SessionID: res.SessionID})
	for _, id := range res.NewEventIDs {
		p.bus.Publish(Notification{Type: NoteEventAppended, SessionID: res.SessionID, EventID: id})
	}
	// Refined rows (async subagent duration reconcile) were already broadcast
	// once with the launch roundtrip duration — re-publish so live clients
	// replace their stale copies instead of showing "0.1s" pills forever.
	for _, id := range res.UpdatedEventIDs {
		p.bus.Publish(Notification{Type: NoteEventAppended, SessionID: res.SessionID, EventID: id})
	}
	// Board cards this batch's TodoWrite calls changed (capture.go hook A):
	// newly captured cards, and accepted cards a completed todo moved to
	// in_review. The tail transaction has committed by now, so exactly one
	// frame per row that really changed — a replayed transcript inserts
	// nothing and moves nothing, and therefore says nothing.
	for _, id := range res.CapturedTaskIDs {
		p.bus.Publish(Notification{Type: NoteTaskUpdated, SessionID: res.SessionID, TaskID: id})
	}
}

// rescan is the 2s safety net: stat every discovered file and tail the ones
// whose size or inode disagree with the cached offset state.
func (p *Pipeline) rescan() {
	for _, f := range p.discover() {
		fi, err := os.Stat(f.path)
		if err != nil {
			continue // vanished mid-scan — next rescan sorts it out
		}
		p.mu.Lock()
		st, ok := p.state[f.path]
		p.mu.Unlock()
		if ok && st.offset == fi.Size() && st.inode == inodeOf(fi) {
			continue
		}
		p.tailOne(f.path, true)
	}
}

// recomputeStatuses ages sessions (active→idle→completed), emits
// session_updated for every transition, and fires OnSessionTerminal for
// completions (with the session's error-event count) and OnAwaitingReply for
// transitions into awaiting_reply.
func (p *Pipeline) recomputeStatuses() {
	changed, err := RecomputeStatuses(p.db, p.cfg.Thresholds, time.Now())
	if err != nil {
		log.Printf("warn: ingest: status ticker: %v", err)
	}
	for _, c := range changed {
		if p.bus != nil {
			p.bus.Publish(Notification{Type: NoteSessionUpdated, SessionID: c.ID})
		}
		if c.Status == StatusAwaitingReply && p.cfg.OnAwaitingReply != nil {
			p.cfg.OnAwaitingReply(c.ID)
		}
		// Board capture hook B (capture.go): a session that finished having
		// captured no todos leaves one card built from its opening prompt. The
		// status UPDATE above has already committed, and the 'sess:<uuid>' key
		// makes a repeat call a no-op, so this is safe to run on every
		// transition into 'completed'.
		if c.Status == "completed" {
			// Board capture lifecycle signal 2 (capture.go): the session that
			// produced these cards is over, so everything the user accepted from
			// it moves on to in_review. Runs BEFORE the fallback card below only
			// for readability — sweep what exists, then mint; a card minted below
			// is brand-new and in 'triage', which the sweep never matches.
			for _, taskID := range SweepSessionToReview(p.db, c.ID) {
				if p.bus != nil {
					p.bus.Publish(Notification{Type: NoteTaskUpdated, SessionID: c.ID, TaskID: taskID})
				}
			}
			if taskID, inserted := CaptureSessionCard(p.db, c.ID); inserted && p.bus != nil {
				p.bus.Publish(Notification{Type: NoteTaskUpdated, SessionID: c.ID, TaskID: taskID})
			}
		}
		if c.Status == "completed" && p.cfg.OnSessionTerminal != nil {
			var errs int
			if err := p.db.QueryRow(
				`SELECT COUNT(*) FROM events WHERE session_id = ? AND status = 'error'`,
				c.ID).Scan(&errs); err != nil {
				log.Printf("warn: ingest: status ticker: error count for session %d: %v", c.ID, err)
				continue
			}
			p.cfg.OnSessionTerminal(c.ID, errs)
		}
	}
	if len(changed) > 0 {
		log.Printf("ingest: status ticker: %d session(s) transitioned", len(changed))
	}
}

// Run executes the pipeline until ctx is done: initial backfill, then
// fsnotify watches (best-effort; macOS kqueue needs per-directory watches and
// can hit fd limits on big roots) with the periodic rescan as the safety net.
func (p *Pipeline) Run(ctx context.Context) error {
	// A configured root that is not on THIS machine is logged and skipped, not
	// fatal: a roots list is shared across machines (dotfiles, launchd plists)
	// and names config dirs only some of them have. All roots missing keeps
	// the single-root contract verbatim — a hard error.
	present, missing := ExistingRoots(p.cfg.ProjectsRoots)
	for _, r := range missing {
		log.Printf("warn: ingest: projects root %s does not exist — skipped", r)
	}
	if len(present) == 0 {
		return fmt.Errorf("projects root: %w", ErrNoProjectsRoots)
	}
	p.Backfill(ctx)

	// fsnotify is a latency optimization; on any setup failure the pipeline
	// silently degrades to rescan-only operation.
	var fsEvents chan fsnotify.Event
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("warn: ingest: fsnotify unavailable (%v) — rescan-only mode", err)
	} else {
		defer watcher.Close()
		p.addWatchTree(watcher)
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
					log.Printf("warn: ingest: fsnotify: %v", werr)
				}
			}
		}()
	}

	rescanT := time.NewTicker(p.cfg.RescanInterval)
	defer rescanT.Stop()
	statusT := time.NewTicker(p.cfg.StatusInterval)
	defer statusT.Stop()
	flushT := time.NewTicker(250 * time.Millisecond)
	defer flushT.Stop()
	metricsT := time.NewTicker(time.Minute)
	defer metricsT.Stop()

	dirty := map[string]struct{}{}
	var lastLogged Metrics

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case ev := <-fsEvents:
			if ev.Op.Has(fsnotify.Create) {
				if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
					p.watchDir(watcher, ev.Name)
					continue
				}
			}
			if strings.HasSuffix(ev.Name, ".jsonl") &&
				(ev.Op.Has(fsnotify.Write) || ev.Op.Has(fsnotify.Create)) {
				dirty[ev.Name] = struct{}{}
			}

		case <-flushT.C:
			for f := range dirty {
				delete(dirty, f)
				p.tailOne(f, true)
			}

		case <-rescanT.C:
			p.rescan()

		case <-statusT.C:
			p.recomputeStatuses()

		case <-metricsT.C:
			if m := p.Metrics(); m != lastLogged {
				log.Printf("ingest: metrics %s", m)
				lastLogged = m
			}
		}
	}
}

// addWatchTree registers EVERY root plus its existing project, session
// companion, and subagents directories. Failures are logged once and
// tolerated. Missing roots are skipped silently — Run already named them.
func (p *Pipeline) addWatchTree(w *fsnotify.Watcher) {
	levels := [][]string{
		{"*"},                   // project dirs
		{"*", "*"},              // session companion dirs
		{"*", "*", "subagents"}, // sidechain dirs
	}
	for _, root := range p.cfg.ProjectsRoots {
		if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
			continue
		}
		p.watchDir(w, root)
		for _, lvl := range levels {
			dirs, _ := filepath.Glob(filepath.Join(append([]string{root}, lvl...)...))
			for _, d := range dirs {
				if fi, err := os.Stat(d); err == nil && fi.IsDir() {
					p.watchDir(w, d)
				}
			}
		}
	}
}

func (p *Pipeline) watchDir(w *fsnotify.Watcher, dir string) {
	if err := w.Add(dir); err != nil {
		log.Printf("warn: ingest: watch %s: %v (rescan covers it)", dir, err)
	}
}

func shortPath(path, root string) string {
	if rel, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return path
}
