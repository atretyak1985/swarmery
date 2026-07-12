package ingest

import (
	"context"
	"database/sql"
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
	ProjectsRoot   string        // e.g. ~/.claude/projects
	RescanInterval time.Duration // fallback full rescan cadence (default 2s)
	StatusInterval time.Duration // session-status ticker cadence (default 10s)
	Thresholds     Thresholds    // active/idle windows (default 2m/30m)
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

// discover lists every transcript under the projects root: main transcripts
// first, then sidechains — so parent subagent_start events exist before their
// sidechain files are ingested (§1/§7 layout).
func (p *Pipeline) discover() []string {
	root := p.cfg.ProjectsRoot
	mains, _ := filepath.Glob(filepath.Join(root, "*", "*.jsonl"))
	sides, _ := filepath.Glob(filepath.Join(root, "*", "*", "subagents", "agent-*.jsonl"))
	sort.Strings(mains)
	sort.Strings(sides)
	return append(mains, sides...)
}

// Backfill runs one full scan of the projects root, ingesting every
// transcript from its persisted offset (0 on first sight). Per-file errors
// are logged and counted, never fatal.
func (p *Pipeline) Backfill(ctx context.Context) Metrics {
	start := time.Now()
	files := p.discover()
	for _, f := range files {
		if ctx.Err() != nil {
			break
		}
		p.tailOne(f, false)
	}
	m := p.Metrics()
	log.Printf("ingest: backfill of %s done in %s — scanned=%d %s",
		p.cfg.ProjectsRoot, time.Since(start).Round(time.Millisecond), len(files), m)
	return m
}

// tailOne incrementally ingests a single file and publishes bus notifications
// for whatever it produced. Safe to call for unchanged files (cheap no-op).
func (p *Pipeline) tailOne(path string, logPickup bool) {
	p.mu.Lock()
	if until, ok := p.errUntil[path]; ok && time.Now().Before(until) {
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()

	res, err := TailFile(p.db, path, p.cfg.Thresholds)
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
			shortPath(path, p.cfg.ProjectsRoot), res.Lines, len(res.NewEventIDs),
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
}

// rescan is the 2s safety net: stat every discovered file and tail the ones
// whose size or inode disagree with the cached offset state.
func (p *Pipeline) rescan() {
	for _, f := range p.discover() {
		fi, err := os.Stat(f)
		if err != nil {
			continue // vanished mid-scan — next rescan sorts it out
		}
		p.mu.Lock()
		st, ok := p.state[f]
		p.mu.Unlock()
		if ok && st.offset == fi.Size() && st.inode == inodeOf(fi) {
			continue
		}
		p.tailOne(f, true)
	}
}

// recomputeStatuses ages sessions (active→idle→completed) and emits
// session_updated for every transition.
func (p *Pipeline) recomputeStatuses() {
	changed, err := RecomputeStatuses(p.db, p.cfg.Thresholds, time.Now())
	if err != nil {
		log.Printf("warn: ingest: status ticker: %v", err)
	}
	for _, id := range changed {
		if p.bus != nil {
			p.bus.Publish(Notification{Type: NoteSessionUpdated, SessionID: id})
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
	if _, err := os.Stat(p.cfg.ProjectsRoot); err != nil {
		return fmt.Errorf("projects root: %w", err)
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

// addWatchTree registers the root plus every existing project, session
// companion, and subagents directory. Failures are logged once and tolerated.
func (p *Pipeline) addWatchTree(w *fsnotify.Watcher) {
	p.watchDir(w, p.cfg.ProjectsRoot)
	levels := [][]string{
		{"*"},                   // project dirs
		{"*", "*"},              // session companion dirs
		{"*", "*", "subagents"}, // sidechain dirs
	}
	for _, lvl := range levels {
		dirs, _ := filepath.Glob(filepath.Join(append([]string{p.cfg.ProjectsRoot}, lvl...)...))
		for _, d := range dirs {
			if fi, err := os.Stat(d); err == nil && fi.IsDir() {
				p.watchDir(w, d)
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
