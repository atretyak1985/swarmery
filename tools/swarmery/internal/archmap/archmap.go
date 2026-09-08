// Package archmap reads a project's architecture-out/architecture-map.json
// (the architecture-pack contract, schemaVersion 1) and answers the one
// question the map is uniquely able to answer without a model: given a set of
// changed files, WHICH modules and flows does this branch touch.
//
// Everything here degrades rather than guesses. A missing artifact, a map from
// a future schemaVersion and a truncated JSON body are all plain errors the
// caller turns into "unknown" (a nil DTO field, a 404), never into a zero that
// reads as "nothing changed".
package archmap

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ErrNoMap reports that the project has no architecture-map.json on disk. It
// is separated from a parse failure so callers can answer 404-vs-500 honestly.
var ErrNoMap = errors.New("archmap: no architecture-map.json")

// MapFileName is the artifact archmap reads, relative to OutDir.
const MapFileName = "architecture-map.json"

// OutDir is the per-project directory the architecture-map skill writes into.
const OutDir = "architecture-out"

// Module is one node of the map. Only the fields Match needs are decoded;
// the artifact carries more (responsibility, dependencies, keyFiles, …) and
// unknown fields are ignored on purpose so a richer map still loads.
type Module struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Path  string `json:"path"`
	Layer string `json:"layer"`
}

// FlowStep is one hop of a flow. `file` is optional in the schema — steps
// without one simply never match.
type FlowStep struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Action string `json:"action"`
	File   string `json:"file"`
}

// Flow is a named, file-anchored path through the modules.
type Flow struct {
	ID    string     `json:"id"`
	Name  string     `json:"name"`
	Steps []FlowStep `json:"steps"`
}

// Map is the decoded artifact.
type Map struct {
	SchemaVersion    int      `json:"schemaVersion"`
	AnalyzedAt       string   `json:"analyzedAt"`
	AnalyzedAtCommit string   `json:"analyzedAtCommit"`
	Modules          []Module `json:"modules"`
	Flows            []Flow   `json:"flows"`
}

// TouchedModule is a module the change set landed in, with the files that put
// it there (repo-relative, ascending).
type TouchedModule struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Files []string `json:"files"`
}

// TouchedFlow is a flow whose file-anchored steps the change set hit.
type TouchedFlow struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Steps []string `json:"steps"`
}

// Touched is the result of Match. Every slice is non-nil so the JSON encoding
// is `[]` rather than `null` — the UI renders an empty strip, not a crash.
type Touched struct {
	Modules   []TouchedModule `json:"modules"`
	Flows     []TouchedFlow   `json:"flows"`
	Unmatched []string        `json:"unmatched"`
}

// Load parses <projectPath>/architecture-out/architecture-map.json.
//
// A schemaVersion other than 1 is refused rather than best-effort decoded: the
// module/flow shapes below ARE the v1 contract, and silently matching against a
// v2 artifact would produce confident nonsense.
func Load(projectPath string) (*Map, error) {
	p := filepath.Join(projectPath, OutDir, MapFileName)
	raw, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w at %s", ErrNoMap, p)
		}
		return nil, fmt.Errorf("archmap: read %s: %w", p, err)
	}
	var m Map
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("archmap: parse %s: %w", p, err)
	}
	if m.SchemaVersion != 1 {
		return nil, fmt.Errorf("archmap: unsupported schemaVersion %d in %s (want 1)", m.SchemaVersion, p)
	}
	return &m, nil
}

// normPath puts a repo-relative path into the one shape both sides of a
// comparison must be in: forward slashes, no "./" prefix, no trailing slash.
// Module paths in the artifact are hand-written by the skill, so they arrive
// as "tools/swarmery/internal/", "./web/src" and "web/src" interchangeably.
func normPath(p string) string {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	p = strings.TrimPrefix(p, "./")
	if p != "/" {
		p = strings.TrimSuffix(p, "/")
	}
	return p
}

// prefixLen reports how much of `file` the module path `dir` claims, or -1 for
// no claim. A module path matches a file when it IS the file (a module anchored
// on a single file) or is one of its ancestor directories — never when it is
// merely a string prefix, so `web/src` does not swallow `web/src-legacy/x.ts`.
func prefixLen(dir, file string) int {
	if dir == "" {
		return -1
	}
	if dir == file {
		return len(dir)
	}
	if strings.HasPrefix(file, dir+"/") {
		return len(dir)
	}
	return -1
}

// Match maps changed files onto the map: each file goes to the module whose
// `path` is its LONGEST claiming prefix (so a nested module beats its parent),
// and onto every flow step whose `file` it equals exactly. A file that no
// module claims lands in Unmatched.
//
// Modules and flows come back in map order (the artifact's own layer-ordered
// sequence), files and steps ascending, so the output is stable across calls.
func Match(m *Map, files []string) Touched {
	out := Touched{Modules: []TouchedModule{}, Flows: []TouchedFlow{}, Unmatched: []string{}}
	if m == nil {
		out.Unmatched = append(out.Unmatched, normFiles(files)...)
		sort.Strings(out.Unmatched)
		return out
	}

	// Pre-normalise module paths once — Match runs over every changed file.
	type modEntry struct {
		mod  Module
		norm string
	}
	mods := make([]modEntry, 0, len(m.Modules))
	for _, mod := range m.Modules {
		mods = append(mods, modEntry{mod: mod, norm: normPath(mod.Path)})
	}

	// Flow steps, indexed by the file that anchors them. One file can anchor
	// steps in several flows, hence the slice.
	type stepRef struct {
		flowIdx int
		label   string
	}
	byStepFile := map[string][]stepRef{}
	for fi, f := range m.Flows {
		for _, s := range f.Steps {
			if s.File == "" {
				continue
			}
			byStepFile[normPath(s.File)] = append(byStepFile[normPath(s.File)], stepRef{
				flowIdx: fi,
				label:   stepLabel(s),
			})
		}
	}

	filesByMod := map[string]map[string]struct{}{}
	stepsByFlow := map[int]map[string]struct{}{}
	unmatched := map[string]struct{}{}

	for _, raw := range files {
		f := normPath(raw)
		if f == "" {
			continue
		}
		best, bestLen := "", -1
		for _, me := range mods {
			if n := prefixLen(me.norm, f); n > bestLen {
				best, bestLen = me.mod.ID, n
			}
		}
		if bestLen < 0 {
			unmatched[f] = struct{}{}
		} else {
			if filesByMod[best] == nil {
				filesByMod[best] = map[string]struct{}{}
			}
			filesByMod[best][f] = struct{}{}
		}
		// Flow membership is independent of module membership: a file can
		// anchor a flow step and still be unmatched by every module path.
		for _, ref := range byStepFile[f] {
			if stepsByFlow[ref.flowIdx] == nil {
				stepsByFlow[ref.flowIdx] = map[string]struct{}{}
			}
			stepsByFlow[ref.flowIdx][ref.label] = struct{}{}
		}
	}

	for _, mod := range m.Modules {
		set, ok := filesByMod[mod.ID]
		if !ok {
			continue
		}
		out.Modules = append(out.Modules, TouchedModule{ID: mod.ID, Name: mod.Name, Files: sortedKeys(set)})
	}
	for fi, f := range m.Flows {
		set, ok := stepsByFlow[fi]
		if !ok {
			continue
		}
		out.Flows = append(out.Flows, TouchedFlow{ID: f.ID, Name: f.Name, Steps: sortedKeys(set)})
	}
	out.Unmatched = sortedKeys(unmatched)
	return out
}

// stepLabel renders a step for the strip: the anchoring file, plus the action
// when the artifact carries one.
func stepLabel(s FlowStep) string {
	f := normPath(s.File)
	if s.Action == "" {
		return f
	}
	return f + " — " + s.Action
}

func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func normFiles(files []string) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		if n := normPath(f); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// ModuleCount is the number of modules in the map — the K of "M of K modules
// touched". Safe on a nil map.
func ModuleCount(m *Map) int {
	if m == nil {
		return 0
	}
	return len(m.Modules)
}
