package agentsync

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Options is one run of sync or check against one project.
type Options struct {
	// ProjectDir is the project checkout whose .claude/ is read and written.
	ProjectDir string
	// ClaudeDir anchors the plugin install (default ~/.claude).
	ClaudeDir string
	// Marketplace is the marketplace name (default DefaultMarketplace).
	Marketplace string
	// Now is a test seam for the generated_at stamp.
	Now func() time.Time
}

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o Options) locator() Locator {
	return Locator{ClaudeDir: o.ClaudeDir, Marketplace: o.Marketplace}
}

// Action is what Plan would do to the project-local file.
type Action string

const (
	ActionCreate    Action = "create"
	ActionUpdate    Action = "update"
	ActionUnchanged Action = "unchanged"
)

// Plan is the resolved intent for one declared agent.
type Plan struct {
	Name      string
	Path      string // absolute path of the generated override
	Source    string // upstream, marketplace-root-relative
	SHA       string // upstream ShortSHA
	Isolation string
	Action    Action
	Content   []byte
}

// Plans resolves every declared override into a Plan without touching disk.
// A project that declares nothing yields no plans and, therefore, no files.
func Plans(o Options) ([]Plan, error) {
	overrides, err := ReadOverrides(o.ProjectDir)
	if err != nil {
		return nil, err
	}
	if len(overrides) == 0 {
		return nil, nil
	}
	loc := o.locator()
	day := o.now()
	out := make([]Plan, 0, len(overrides))
	for _, ov := range overrides {
		src, err := loc.Find(ov.Name)
		if err != nil {
			return nil, err
		}
		content, err := Render(src, ov.Isolation, day)
		if err != nil {
			return nil, err
		}
		p := Plan{
			Name:      ov.Name,
			Path:      OverridePath(o.ProjectDir, ov.Name),
			Source:    src.Rel,
			SHA:       src.SHA,
			Isolation: ov.Isolation,
			Action:    ActionCreate,
			Content:   content,
		}
		switch existing, err := os.ReadFile(p.Path); {
		case err == nil && equalIgnoringStamp(existing, content):
			// Only generated_at would move. Rewriting would churn the diff of
			// every project every day for no behavioural change.
			p.Action, p.Content = ActionUnchanged, existing
		case err == nil:
			p.Action = ActionUpdate
		case errors.Is(err, os.ErrNotExist):
			p.Action = ActionCreate
		default:
			return nil, fmt.Errorf("read %s: %w", p.Path, err)
		}
		out = append(out, p)
	}
	return out, nil
}

// Apply writes every plan that is not already up to date.
func Apply(plans []Plan) error {
	for _, p := range plans {
		if p.Action == ActionUnchanged {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p.Path), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(p.Path), err)
		}
		if err := os.WriteFile(p.Path, p.Content, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", p.Path, err)
		}
	}
	return nil
}

// Drift is one reason a generated file no longer matches what it claims.
type Drift struct {
	Name   string
	Path   string
	Reason string
}

// Check compares each generated override against the upstream it was generated
// from. It never writes. A non-empty result is what makes the CLI exit 1: the
// point of the stamp is that upstream moving on is loud, not silent.
func Check(o Options) ([]Drift, error) {
	overrides, err := ReadOverrides(o.ProjectDir)
	if err != nil {
		return nil, err
	}
	loc := o.locator()
	var drifts []Drift
	for _, ov := range overrides {
		path := OverridePath(o.ProjectDir, ov.Name)
		add := func(format string, args ...any) {
			drifts = append(drifts, Drift{Name: ov.Name, Path: path, Reason: fmt.Sprintf(format, args...)})
		}
		src, err := loc.Find(ov.Name)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			add("declared in settings.json but not generated yet")
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		front, _, err := splitFrontmatter(data)
		if err != nil {
			add("%v", err)
			continue
		}
		if by, ok := lookupKey(front, keyGeneratedBy); !ok || by != generatorID {
			add("handwritten fork: no `%s: %s` stamp", keyGeneratedBy, generatorID)
			continue
		}
		if sha, _ := lookupKey(front, keySourceSHA); sha != src.SHA {
			add("upstream moved on: generated from %s %s, %s is now %s", src.Rel, sha, src.Rel, src.SHA)
			continue
		}
		if iso, _ := lookupKey(front, isolationKey); iso != ov.Isolation {
			add("%s is %q but settings.json declares %q", isolationKey, iso, ov.Isolation)
		}
	}
	return drifts, nil
}

// equalIgnoringStamp compares two generated files while ignoring generated_at,
// the only field that moves without the content moving.
func equalIgnoringStamp(a, b []byte) bool {
	return stripStamp(a) == stripStamp(b)
}

func stripStamp(data []byte) string {
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	kept := lines[:0]
	for _, l := range lines {
		if _, ok := cutKey(l, keyGeneratedAt); ok {
			continue
		}
		kept = append(kept, l)
	}
	return strings.Join(kept, "\n")
}
