package api

// Agent roles for the roster (board-redesign phase 4: agent picker).
//
// The role is NOT the daemon's to decide: every pack ships
// plugins/<pack>/agents/roles.json ({"<agent>": "<role>"}) next to its agent
// files, and a project may add its own .claude/agents/roles.json. The roster
// reads the file in the directory of each agent's file_path and nothing else —
// no agent names live here. A missing file, a missing entry or a value outside
// the fixed vocabulary all resolve to "domain", the catch-all group.
//
// Files are cached per directory and re-read only when their mtime (or size)
// changes, so a roster request costs one stat per agents/ directory.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// roleDomain is the fallback role for anything the roles file does not name.
const roleDomain = "domain"

// knownRoles is the fixed role vocabulary (scripts/tests/agent-roles.test.sh
// enforces the same set on the shipped files).
var knownRoles = map[string]bool{
	"orchestrate": true, "implement": true, "review": true,
	"research": true, "ops": true, roleDomain: true,
}

type rolesFile struct {
	mtime time.Time
	size  int64
	roles map[string]string // nil when the file is absent or unreadable
}

// agentRoleCache memoises roles.json per directory, keyed by the directory path.
type agentRoleCache struct {
	mu   sync.Mutex
	dirs map[string]rolesFile
}

var agentRoles = &agentRoleCache{dirs: map[string]rolesFile{}}

// rolesFor returns the role map for dir, re-reading roles.json only when its
// mtime or size moved since the cached read. A missing file yields nil.
func (c *agentRoleCache) rolesFor(dir string) map[string]string {
	p := filepath.Join(dir, "roles.json")
	fi, err := os.Stat(p)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		delete(c.dirs, dir)
		return nil
	}
	if cached, ok := c.dirs[dir]; ok && cached.mtime.Equal(fi.ModTime()) && cached.size == fi.Size() {
		return cached.roles
	}
	entry := rolesFile{mtime: fi.ModTime(), size: fi.Size()}
	if b, rerr := os.ReadFile(p); rerr == nil {
		var m map[string]string
		if json.Unmarshal(b, &m) == nil {
			entry.roles = m
		}
	}
	c.dirs[dir] = entry
	return entry.roles
}

// roleOf resolves an agent's role from the roles.json beside its file. The
// registry name may be plugin-qualified ("core:tech-lead") while the file keys
// bare names, so the bare name is tried first, then the file's basename.
func (c *agentRoleCache) roleOf(name, path string) string {
	if path == "" {
		return roleDomain
	}
	roles := c.rolesFor(filepath.Dir(path))
	if roles == nil {
		return roleDomain
	}
	bare := name
	if i := strings.LastIndex(bare, ":"); i >= 0 {
		bare = bare[i+1:]
	}
	for _, k := range []string{bare, strings.TrimSuffix(filepath.Base(path), ".md")} {
		if r, ok := roles[k]; ok {
			if knownRoles[r] {
				return r
			}
			return roleDomain
		}
	}
	return roleDomain
}

// enabledIn reports whether a registry agent resolves in the scoped project:
// a local agent (the user's own or the project's) always does; a plugin agent
// only when its pack is in the project's effective enabledPlugins
// (settings.json + settings.local.json + declared overlays — effectiveScope).
// Unscoped (fleet) there is no project to gate on, so every row is enabled.
func (e *effectiveScope) enabledIn(origin string, pluginName *string) bool {
	if e == nil || origin != "plugin" {
		return true
	}
	if pluginName == nil {
		return false
	}
	for _, p := range e.packs {
		if p == *pluginName {
			return true
		}
	}
	return false
}
