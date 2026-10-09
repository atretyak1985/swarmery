package repoprovider

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// settingsLocalMode is the mode of a settings.local.json PersistProviderAnswer
// CREATES. An existing file keeps its own permission bits: operators keep env
// secrets there and often lock it to 0600, which a rewrite must never widen.
const settingsLocalMode = 0o644

// persistMu serialises PersistProviderAnswer's read-modify-write, so two
// answers saved at once cannot drop each other's keys.
var persistMu sync.Mutex

// PersistProviderAnswer records the operator's "which service hosts this
// repo?" answer — the "ask once" store for a host Detect could not classify —
// as `swarmery.vcs.provider` in <projectPath>/.claude/settings.local.json.
// LoadConfig reads it back as the project's explicit provider.
//
// The write is a read-modify-write that keeps every other key of the file
// (top-level ones, other `swarmery.*` blocks, other `swarmery.vcs` keys)
// intact; `.claude/` and the file are created when absent, and the result is
// written through a temp file renamed into place — 0644 for a new file, the
// existing file's own permission bits otherwise. Only github and gitlab
// are accepted (anything else is ErrUnknownProvider), and a file that is not a
// JSON object is refused rather than overwritten.
func PersistProviderAnswer(projectPath string, kind Kind) error {
	if kind != KindGitHub && kind != KindGitLab {
		return fmt.Errorf("%w: %q (want %q or %q)", ErrUnknownProvider, kind, KindGitHub, KindGitLab)
	}
	if projectPath == "" {
		return errors.New("repoprovider: empty project path")
	}
	persistMu.Lock()
	defer persistMu.Unlock()

	dir := filepath.Join(projectPath, ".claude")
	path := filepath.Join(dir, "settings.local.json")
	root := map[string]json.RawMessage{}
	mode := os.FileMode(settingsLocalMode)
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return fmt.Errorf("repoprovider: read %s: %w", path, err)
	default:
		fi, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("repoprovider: stat %s: %w", path, err)
		}
		mode = fi.Mode().Perm()
		if len(bytes.TrimSpace(raw)) > 0 {
			if err := json.Unmarshal(raw, &root); err != nil || root == nil {
				return fmt.Errorf("repoprovider: %s is not a JSON object; not overwriting it", path)
			}
		}
	}

	swarmery, err := objectAt(root, "swarmery")
	if err != nil {
		return fmt.Errorf("repoprovider: %s: %w", path, err)
	}
	vcs, err := objectAt(swarmery, "vcs")
	if err != nil {
		return fmt.Errorf("repoprovider: %s: swarmery.%w", path, err)
	}
	if vcs["provider"], err = encodeJSON(string(kind), false); err != nil {
		return err
	}
	if swarmery["vcs"], err = encodeJSON(vcs, false); err != nil {
		return err
	}
	if root["swarmery"], err = encodeJSON(swarmery, false); err != nil {
		return err
	}
	out, err := encodeJSON(root, true)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("repoprovider: create %s: %w", dir, err)
	}
	return writeFileAtomic(path, out, mode)
}

// encodeJSON marshals v WITHOUT HTML escaping, so the operator's other values
// ("Bash(a && b)" permission rules, "<placeholder>" text) round-trip byte for
// byte instead of coming back as & / <. indent pretty-prints with
// two spaces and keeps the trailing newline; compact output has none.
func encodeJSON(v any, indent bool) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if indent {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	if indent {
		return buf.Bytes(), nil
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// objectAt returns m[key] decoded as a JSON object (an empty one when the key
// is absent or null); a non-object value is an error, never silently replaced.
func objectAt(m map[string]json.RawMessage, key string) (map[string]json.RawMessage, error) {
	raw, ok := m[key]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return map[string]json.RawMessage{}, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, fmt.Errorf("%s is not a JSON object", key)
	}
	return obj, nil
}

// writeFileAtomic writes data to path through a temp file in the same
// directory, chmods it to mode and renames it into place: a reader sees the
// old file or the new one, never half of one.
func writeFileAtomic(path string, data []byte, mode os.FileMode) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("repoprovider: write %s: %w", path, err)
	}
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		return fmt.Errorf("repoprovider: write %s: %w", path, err)
	}
	if err = tmp.Chmod(mode); err != nil {
		return fmt.Errorf("repoprovider: write %s: %w", path, err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("repoprovider: write %s: %w", path, err)
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("repoprovider: write %s: %w", path, err)
	}
	return nil
}
