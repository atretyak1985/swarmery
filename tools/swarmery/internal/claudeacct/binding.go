package claudeacct

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

// BindingFile is the per-project, NOT-shared settings file the binding lives in.
// It is the same tier internal/hookcfg writes to (D2 there): machine-local,
// gitignored, never carrying a decision that belongs to the whole team. Two
// engineers on one repo legitimately have different Claude accounts.
const BindingFile = ".claude/settings.local.json"

// The namespaced key inside that file:
//
//	{ "swarmery": { "claudeAccount": "nabu-org" } }
//
// A namespace of our own, because the file's other keys belong to Claude Code's
// own settings schema and must not be shadowed.
const (
	bindingNamespace = "swarmery"
	bindingField     = "claudeAccount"
)

// bindingPath is <project>/.claude/settings.local.json.
func bindingPath(projectPath string) string {
	return filepath.Join(projectPath, filepath.FromSlash(BindingFile))
}

// Binding returns the account key bound to projectPath, or "" when the project
// has no binding. A missing or malformed file is NOT an error: an unmanaged or
// hand-broken settings file must mean "default account", never a failed spawn.
//
// A binding whose value fails ValidKey is treated exactly like a broken file —
// "" — so a hand-edited key can never reach a path join.
func Binding(projectPath string) string {
	_, root, _, err := readSettings(bindingPath(projectPath))
	if err != nil {
		return ""
	}
	ns, _ := root[bindingNamespace].(map[string]any)
	key, _ := ns[bindingField].(string)
	key = strings.TrimSpace(key)
	if !ValidKey(key) {
		return ""
	}
	return key
}

// SetBinding writes the binding, following internal/hookcfg's surgery rules
// verbatim: read-modify-write through map[string]any so every foreign key
// survives, abort WITHOUT writing on unparseable JSON, copy to .bak before the
// first write, and be idempotent (a second identical call produces no diff).
// An empty account clears the binding (and prunes the empty "swarmery" object).
//
// Clearing a binding that is not there — and setting the one already stored —
// touch the file not at all, so SetBinding never reformats a settings file it
// has nothing to say about.
func SetBinding(projectPath, account string) error {
	account = strings.TrimSpace(account)
	if account != "" && !ValidKey(account) {
		return fmt.Errorf("claudeacct: %q is not a valid account key", account)
	}

	path := bindingPath(projectPath)
	raw, root, existed, err := readSettings(path)
	if err != nil {
		return err
	}
	ns, _ := root[bindingNamespace].(map[string]any)

	if account == "" {
		if ns == nil {
			return nil // nothing of ours in there — leave the file alone
		}
		if _, ok := ns[bindingField]; !ok {
			return nil
		}
		delete(ns, bindingField)
		if len(ns) == 0 {
			delete(root, bindingNamespace)
		}
	} else {
		if cur, _ := ns[bindingField].(string); cur == account {
			return nil // already bound — no write, no reformat
		}
		if ns == nil {
			ns = map[string]any{}
			root[bindingNamespace] = ns
		}
		ns[bindingField] = account
	}

	return writeSettings(path, raw, root, existed)
}

// EnvFor is what a spawner actually needs: the env DELTA for this project, to be
// appended to os.Environ(). Returns nil for an unbound project and for the
// default account — see the package doc for why that must stay empty.
func EnvFor(projectPath string) []string {
	return EnvForAccount(Binding(projectPath))
}

// EnvForAccount is EnvFor when the caller already resolved the key. Dispatch and
// verify MUST use this one: they run in a worktree whose path has no project
// settings file, so resolving from cwd there would silently yield the default
// account (see plan A3).
//
// The dir comes from Discover when the account exists, so an account living in a
// non-canonical dir still gets pointed at its real credentials; ConfigDirFor is
// the fallback for an account being provisioned. An unresolvable key yields nil
// — the default account — because a silent spawn under the default beats a
// failed spawn.
func EnvForAccount(key string) []string {
	dir, ok := ConfigDirForAccount(key)
	if !ok {
		return nil
	}
	return []string{configDirEnv + "=" + dir}
}

// ConfigDirForAccount is the config dir a spawn under `key` actually reads —
// the same answer EnvForAccount encodes as CLAUDE_CONFIG_DIR, exposed for the
// callers that must touch that dir's files directly (a settings.json snapshot
// before a user-scope install, a plugin cache lookup). Discover wins over the
// canonical path so an account living in a non-canonical dir is still found.
// ok=false for the default account and for an unresolvable key: the caller then
// keeps the default ~/.claude it already holds, which is exactly what the spawn
// falls back to.
func ConfigDirForAccount(key string) (string, bool) {
	key = strings.TrimSpace(key)
	if key == "" || key == ingest.DefaultAccount {
		return "", false
	}
	for _, a := range Discover() {
		if a.Key == key {
			return a.ConfigDir, true
		}
	}
	dir, err := ConfigDirFor(key)
	if err != nil {
		return "", false
	}
	return dir, true
}

// ConfigDirForProject is ConfigDirForAccount over the project's own binding.
func ConfigDirForProject(projectPath string) (string, bool) {
	return ConfigDirForAccount(Binding(projectPath))
}

// ── settings surgery helpers (internal/hookcfg's, verbatim in behaviour) ──────

// readSettings loads and parses the settings file. A missing file yields an
// empty root; a parse failure aborts (never write over a file we cannot read).
func readSettings(path string) (raw []byte, root map[string]any, existed bool, err error) {
	raw, err = os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, map[string]any{}, false, nil
	}
	if err != nil {
		return nil, nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, nil, true, fmt.Errorf(
			"%s is not valid JSON (%v) — aborting without writing; fix or remove the file and retry", path, err)
	}
	return raw, root, true, nil
}

// writeSettings marshals root (2-space indent, trailing newline) and writes it
// if it differs from the original bytes. The original is preserved as .bak
// before the FIRST swarmery write.
func writeSettings(path string, raw []byte, root map[string]any, existed bool) error {
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	if existed && bytes.Equal(out, raw) {
		return nil
	}
	if existed {
		bak := path + ".bak"
		if _, err := os.Stat(bak); os.IsNotExist(err) {
			if err := os.WriteFile(bak, raw, 0o644); err != nil {
				return fmt.Errorf("write backup %s: %w", bak, err)
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
