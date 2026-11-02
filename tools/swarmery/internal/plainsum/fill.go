package plainsum

// Filler writes the `## In plain words` section into a new plan's docs when
// whoever wrote the plan left it out. The planner and Planning Mode are told
// to write it (core's plan format), but a card micro-plan is minted from a
// template (internal/taskdir) and a plan written by hand in a session often
// skips it; the Plans page then made the operator wait for a model run on
// first view, and re-ran it after every checkbox tick, because the cache key
// is the whole doc. Written once into the markdown, the section is authored
// text: shown as is, never regenerated, edited like the rest of the doc.
//
// What it touches, deliberately narrow:
//
//   - only plans under <root>/<project>/workspace/working/YYYY/MM/DD/<slug>/plan
//     whose date is on or after Since — existing plans are not backfilled;
//   - only once every doc in plan/ has been quiet for Quiet, so a planner
//     still writing the plan is not raced;
//   - only README.md and phase-*.md without the section (Authored == "");
//   - the write is skipped when the doc changed while the model ran, and a
//     doc version that failed is not retried by this process.

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

// DefaultFillQuiet is how long a plan must sit unchanged before it is filled.
const DefaultFillQuiet = 2 * time.Minute

// FillSince is the first plan date the daemon fills: the day the Filler
// shipped. Plans dated before it are left as they are, never backfilled.
var FillSince = time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)

// DefaultFillInterval is the Filler's pass cadence.
const DefaultFillInterval = time.Minute

// Filler fills missing plain-words sections; see the comment above.
type Filler struct {
	Svc  *Service
	Root string // the workspace root (wsingest's --workspace-root)
	// Since is the first plan date (the YYYY/MM/DD in the path, UTC) filled.
	Since    time.Time
	Quiet    time.Duration
	Interval time.Duration
	Now      func() time.Time

	tried map[string]time.Time // doc path → mtime of the version that failed
}

// Run passes every Interval until ctx ends.
func (f *Filler) Run(ctx context.Context) {
	interval := f.Interval
	if interval <= 0 {
		interval = DefaultFillInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			f.Pass(ctx)
		}
	}
}

// Pass fills every eligible plan once. Exported for tests.
func (f *Filler) Pass(ctx context.Context) {
	if f.tried == nil {
		f.tried = map[string]time.Time{}
	}
	dirs, err := filepath.Glob(filepath.Join(f.Root, "*", "workspace", "working", "*", "*", "*", "*", "plan"))
	if err != nil {
		log.Printf("warn: plainsum fill: %v", err)
		return
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		if ctx.Err() != nil {
			return
		}
		if !f.datedFrom(dir) {
			continue
		}
		docs, ok := f.quietDocs(dir)
		if !ok {
			continue
		}
		for _, doc := range docs {
			if ctx.Err() != nil {
				return
			}
			f.fillDoc(ctx, dir, doc)
		}
	}
}

// datedFrom reports whether the plan dir's YYYY/MM/DD is on or after Since.
func (f *Filler) datedFrom(planDir string) bool {
	task := filepath.Dir(planDir)
	day := filepath.Dir(task)
	month := filepath.Dir(day)
	year := filepath.Dir(month)
	at, err := time.Parse("2006/01/02", filepath.Base(year)+"/"+filepath.Base(month)+"/"+filepath.Base(day))
	if err != nil {
		return false
	}
	since := f.Since.UTC().Truncate(24 * time.Hour)
	return !at.Before(since)
}

// quietDocs lists README.md and the phase docs of a plan dir, or ok=false
// while any markdown in it changed within Quiet (or there is no README).
func (f *Filler) quietDocs(planDir string) ([]string, bool) {
	quiet := f.Quiet
	if quiet <= 0 {
		quiet = DefaultFillQuiet
	}
	now := f.now()
	all, err := filepath.Glob(filepath.Join(planDir, "*.md"))
	if err != nil {
		return nil, false
	}
	var docs []string
	hasReadme := false
	for _, p := range all {
		fi, err := os.Stat(p)
		if err != nil {
			return nil, false
		}
		if now.Sub(fi.ModTime()) < quiet {
			return nil, false
		}
		base := filepath.Base(p)
		switch {
		case base == "README.md":
			hasReadme = true
			docs = append([]string{p}, docs...)
		case strings.HasPrefix(base, "phase-"):
			docs = append(docs, p)
		}
	}
	return docs, hasReadme
}

func (f *Filler) fillDoc(ctx context.Context, planDir, path string) {
	fi, err := os.Stat(path)
	if err != nil {
		return
	}
	if at, ok := f.tried[path]; ok && at.Equal(fi.ModTime()) {
		return
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return
	}
	doc := string(body)
	if Authored(doc) != "" || strings.TrimSpace(doc) == "" {
		return
	}
	isReadme := filepath.Base(path) == "README.md"
	kind, source := KindPhase, doc
	if isReadme {
		kind = KindPlan
		if spec, err := os.ReadFile(filepath.Join(planDir, "spec.md")); err == nil && strings.TrimSpace(string(spec)) != "" {
			source = doc + "\n\n---\n\n" + string(spec)
		}
	}
	text, err := f.Svc.Generate(ctx, kind, DocLang(doc), source)
	if err != nil {
		f.tried[path] = fi.ModTime()
		log.Printf("warn: plainsum fill %s: %v", path, err)
		return
	}
	// The doc must be the version the model read.
	now, err := os.ReadFile(path)
	if err != nil || string(now) != doc {
		return
	}
	out := InsertAuthored(doc, text, isReadme)
	if err := writeAtomic(path, out, fi.Mode()); err != nil {
		f.tried[path] = fi.ModTime()
		log.Printf("warn: plainsum fill write %s: %v", path, err)
		return
	}
	log.Printf("plainsum: wrote In plain words into %s", path)
}

func (f *Filler) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

func writeAtomic(path, body string, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".plainsum-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, mode.Perm()); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}

var (
	h1Re      = regexp.MustCompile(`(?m)^# .*$`)
	headingRe = regexp.MustCompile(`(?m)^#{1,6}\s+`)
	fenceRe   = regexp.MustCompile("(?s)```.*?```")
	inlineRe  = regexp.MustCompile("`[^`\n]*`")
	firstH2Re = regexp.MustCompile(`(?m)^## `)
)

// InsertAuthored puts `## In plain words` + text into doc: directly under the
// H1 for a README, before the first `## ` heading for a phase doc (after its
// header rows), and after the H1 or at the top when neither exists. Headings
// inside text are flattened so the section stays one section. Pure.
func InsertAuthored(doc, text string, readme bool) string {
	text = strings.TrimSpace(headingRe.ReplaceAllString(strings.TrimSpace(text), ""))
	section := "## In plain words\n\n" + text + "\n\n"
	at := -1
	if !readme {
		if loc := firstH2Re.FindStringIndex(doc); loc != nil {
			at = loc[0]
		}
	}
	if at < 0 {
		if loc := h1Re.FindStringIndex(doc); loc != nil {
			at = loc[1]
			for at < len(doc) && doc[at] == '\n' {
				at++
			}
		} else {
			at = 0
		}
	}
	head := doc[:at]
	if at > 0 {
		head = strings.TrimRight(head, "\n") + "\n\n"
	}
	return head + section + strings.TrimLeft(doc[at:], "\n")
}

// DocLang is the language a plan doc is written in: Ukrainian when Cyrillic
// makes up at least 30 % of the letters of its prose (code and inline code
// left out, they are English either way), else English.
func DocLang(doc string) string {
	prose := inlineRe.ReplaceAllString(fenceRe.ReplaceAllString(doc, ""), "")
	var cyr, letters int
	for _, r := range prose {
		if !unicode.IsLetter(r) {
			continue
		}
		letters++
		if unicode.Is(unicode.Cyrillic, r) {
			cyr++
		}
	}
	if letters > 0 && cyr*10 >= letters*3 {
		return "uk"
	}
	return "en"
}
