package claudeacct

// The estate DECLARATION: its non-walking reader and its only writer.
//
// Resolve (resolve.go) is the walking reader and stays the only one that
// climbs. Estate reads one directory's declaration and nothing else; SetEstate
// writes it with the same surgery discipline SetBinding uses — the two writers
// share the `swarmery` namespace and neither may delete the other's field.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Estate returns the estate declared AT dir — the `swarmery.estate` field of
// <dir>/.claude/settings.local.json — and dir itself as the root. ("", "") for a
// missing, malformed or undeclared file, and for a value that fails ValidKey:
// exactly Binding's tolerance, and no error. No walk: a declaration on an
// ancestor is Resolve's business, not this reader's.
func Estate(dir string) (key, root string) {
	if strings.TrimSpace(dir) == "" {
		return "", "" // never read a RELATIVE binding path against the process cwd
	}
	ns, _ := namespaceAt(dir)
	if key = validField(ns, estateField); key == "" {
		return "", ""
	}
	return key, filepath.Clean(dir)
}

// SetEstate declares dir as the root of estate key (key != "") or removes the
// declaration at dir (key == ""). Behaviour matches SetBinding line for line:
// read-modify-write through map[string]any so every foreign key survives, abort
// WITHOUT writing on unparseable JSON (readSettings' error verbatim), .bak of the
// original once before the first write (writeSettings), a missing file created
// with its parent directory, and idempotent — setting the value already stored
// or removing one that is not there touches the file not at all.
//
// Removing deletes ONLY the estate field and prunes the `swarmery` object only
// when nothing of ours is left, so a claudeAccount binding at dir survives. The
// account is not touched either way: the two axes are independent.
func SetEstate(dir, key string) error {
	key = strings.TrimSpace(key)
	if key != "" && !ValidKey(key) {
		return fmt.Errorf("claudeacct: %q is not a valid estate key", key)
	}
	if strings.TrimSpace(dir) == "" {
		return errors.New("claudeacct: SetEstate needs a directory")
	}

	path := bindingPath(dir)
	raw, root, existed, err := readSettings(path)
	if err != nil {
		return err
	}
	ns, _ := root[bindingNamespace].(map[string]any)
	// Lock 1, as SetBinding: a tracked or unclassifiable target is refused on
	// set and clear, and left byte-identical.
	if err := refuseDistrustedTarget(path, existed); err != nil {
		return err
	}

	if key == "" {
		if ns == nil {
			return nil // nothing of ours in there — leave the file alone
		}
		if _, ok := ns[estateField]; !ok {
			return nil
		}
		delete(ns, estateField)
		if len(ns) == 0 {
			delete(root, bindingNamespace)
		}
	} else {
		if cur, _ := ns[estateField].(string); cur == key {
			return nil // already declared — no write, no reformat
		}
		if ns == nil {
			ns = map[string]any{}
			root[bindingNamespace] = ns
		}
		ns[estateField] = key
	}

	return writeSettings(path, raw, root, existed)
}

// RevertEstate undoes an estate write at dir that did not read back. When the
// binding file existed before the write (existedBefore) the declaration is put
// back to prev through SetEstate. When it did NOT, the file the write created
// is removed — that file and nothing else: not its .claude directory, and there
// is no .bak to remove (one is only made of a file that already existed) — so a
// failed declaration never leaves an empty `{}` settings file behind.
func RevertEstate(dir, prev string, existedBefore bool) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("claudeacct: RevertEstate needs a directory")
	}
	if existedBefore {
		return SetEstate(dir, prev)
	}
	path := bindingPath(dir)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

// EstateSettingsTracked says why dir's .claude/settings.json — the file an
// estate hands every descendant (Resolution.SettingsFile) — may not become an
// estate's settings without the operator's explicit say-so: git tracks it, so
// its keys are whatever the repository ships, or git cannot tell. "" when the
// file is absent, untracked, or outside any repository.
func EstateSettingsTracked(dir string) string {
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	path := filepath.Join(cleanAbs(dir), filepath.FromSlash(ProjectSettingsFile))
	if _, err := os.Lstat(path); err != nil {
		return ""
	}
	f := probeGitTracked(path)
	switch {
	case f.verdict.honoured():
		return ""
	case f.verdict == trackTracked:
		return fmt.Sprintf("git tracks %s, so the estate's settings would be whatever the repository ships",
			filepath.Join(f.dir, f.name))
	default:
		return fmt.Sprintf("git cannot say whether %s is tracked (%s)", filepath.Join(f.dir, f.name), f.detail)
	}
}
