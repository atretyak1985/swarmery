package onboard

// TogglePlugin flips enabledPlugins["<pack>@swarmery"] in
// <projectDir>/.claude/settings.json — merge-only surgery in the Attach mold
// (attach.go:132-186, 294-306): every foreign key survives, the pre-edit file
// is backed up to settings.json.bak before a real write. Enable sets the key
// to true; disable DELETES the key — the same end state Detach leaves
// (onboard.go:368-373) and what projectscan reads as "off". core is refused:
// its lifecycle (hooks, statusline, project.json) belongs to Attach/Detach.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var (
	// ErrCoreLocked — core carries hooks/statusline/project.json; use Attach/Detach.
	ErrCoreLocked = errors.New("core is managed via attach/detach")
	// ErrNoSettings — nothing to edit; the caller maps this to 409.
	ErrNoSettings = errors.New("no .claude/settings.json — attach the project first")
	// ErrBadSettings — settings.json is not valid JSON; never overwritten.
	ErrBadSettings = errors.New("malformed .claude/settings.json")
)

// ToggleResult reports what a TogglePlugin call did.
type ToggleResult struct {
	Changed bool
	// Backup is ".claude/settings.json.bak" when a real write happened, else "".
	Backup string
}

func TogglePlugin(projectDir, pack string, enabled bool) (*ToggleResult, error) {
	if pack == "core" {
		return nil, ErrCoreLocked
	}
	sPath := filepath.Join(projectDir, ".claude", "settings.json")
	orig, err := os.ReadFile(sPath)
	if os.IsNotExist(err) {
		return nil, ErrNoSettings
	}
	if err != nil {
		return nil, err
	}
	settings := map[string]any{}
	if uerr := json.Unmarshal(orig, &settings); uerr != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadSettings, uerr)
	}

	key := pack + marketplaceSuffix
	res := &ToggleResult{}
	if enabled {
		ep, ok := settings["enabledPlugins"].(map[string]any)
		if !ok {
			if _, present := settings["enabledPlugins"]; present {
				return nil, fmt.Errorf("%w: enabledPlugins has an unexpected shape", ErrBadSettings)
			}
			ep = map[string]any{}
			settings["enabledPlugins"] = ep
		}
		if on, _ := ep[key].(bool); !on {
			ep[key] = true
			res.Changed = true
		}
	} else if ep, _ := settings["enabledPlugins"].(map[string]any); ep != nil {
		if _, present := ep[key]; present {
			delete(ep, key)
			res.Changed = true
		}
	}
	if !res.Changed {
		return res, nil
	}
	if err := os.WriteFile(sPath+".bak", orig, 0o644); err != nil {
		return nil, fmt.Errorf("write backup %s.bak: %w", sPath, err)
	}
	res.Backup = ".claude/settings.json.bak"
	if err := writeJSON(sPath, settings); err != nil {
		return nil, err
	}
	return res, nil
}
