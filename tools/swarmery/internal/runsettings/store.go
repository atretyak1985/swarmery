package runsettings

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// runDirEnv overrides the run-artifact base (default ~/.swarmery/run), exactly
// as SWARMERY_SECRETS_DIR overrides the credential store's directory: so no test
// ever touches the operator's real ~/.swarmery.
const runDirEnv = "SWARMERY_RUN_DIR"

// userHomeDir is the $HOME seam; a package var only so a test can make the home
// directory unresolvable.
var userHomeDir = os.UserHomeDir

// baseDir is $SWARMERY_RUN_DIR when set, else ~/.swarmery/run. "" when neither
// resolves — callers then treat the store as absent.
func baseDir() string {
	if v := strings.TrimSpace(os.Getenv(runDirEnv)); v != "" {
		return filepath.Clean(v)
	}
	home, err := userHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".swarmery", "run")
}

// Dir is the composed-settings directory, <base>/settings, or "" when no base
// resolves.
func Dir() string {
	b := baseDir()
	if b == "" {
		return ""
	}
	return filepath.Join(b, "settings")
}

// write stores b at <Dir>/<sha256-hex-of-b>.json and returns that path. The
// directory is 0700, the file 0600; an existing file is never rewritten
// (content addressing makes it identical) but its mtime is refreshed, so a file
// still in use is never older than its last compose and Prune never takes it
// from under a run. The write goes through a temp file
// and a rename, so a crash can never leave a truncated file under the final
// name for a later "already exists" check to trust. "" on any failure.
func write(b []byte) string {
	dir := Dir()
	if dir == "" {
		return ""
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	// MkdirAll honours the umask and leaves an existing directory's mode alone;
	// the leaf holds settings files, so it is pinned to 0700 either way.
	if err := os.Chmod(dir, 0o700); err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	path := filepath.Join(dir, hex.EncodeToString(sum[:])+".json")
	if reusable(path, b) {
		now := time.Now()
		_ = os.Chtimes(path, now, now)
		return path
	}
	tmp, err := os.CreateTemp(dir, ".compose-*.tmp")
	if err != nil {
		return ""
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return ""
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return ""
	}
	if err := tmp.Close(); err != nil {
		return ""
	}
	if err := os.Rename(tmpName, path); err != nil {
		return ""
	}
	ok = true
	return path
}

// Prune deletes the *.json files in Dir whose mtime is older than maxAge, and
// any ".compose-*.tmp" a crashed write left behind that is older than an hour,
// and returns how many it removed. Never an error: a missing directory is zero
// removals. Content addressing means a pruned file is recreated identically by
// the next Compose that needs it. Pure filesystem — no store, no socket.
func Prune(maxAge time.Duration) (removed int) {
	dir := Dir()
	if dir == "" {
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	cutoff := time.Now().Add(-maxAge)
	tmpCutoff := time.Now().Add(-time.Hour)
	for _, e := range entries {
		name := e.Name()
		isTmp := strings.HasPrefix(name, ".compose-") && strings.HasSuffix(name, ".tmp")
		if !e.Type().IsRegular() || (filepath.Ext(name) != ".json" && !isTmp) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		limit := cutoff
		if isTmp {
			limit = tmpCutoff
		}
		if !fi.ModTime().Before(limit) {
			continue
		}
		if os.Remove(filepath.Join(dir, name)) == nil {
			removed++
		}
	}
	if removed > 0 {
		log.Printf("runsettings: pruned %d composed settings file(s) older than %s", removed, maxAge)
	}
	return removed
}

// reusable reports whether the file already at path IS what its name claims: a
// regular file, mode 0600, owned by this user, holding exactly b. Anything else
// — a planted or tampered file, a loosened mode — is replaced through the
// temp-file-and-rename path instead of being handed to a run.
func reusable(path string, b []byte) bool {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 || fi.Size() != int64(len(b)) {
		return false
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Geteuid() {
		return false
	}
	raw, err := os.ReadFile(path)
	return err == nil && bytes.Equal(raw, b)
}
