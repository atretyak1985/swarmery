// The launcher page `swarmery open` hands the browser instead of the unlock
// URL. A process's argv is visible to every local user (ps), so the URL with
// its ticket never goes on the opener's command line: the opener gets the
// path of a small HTML page whose script replaces itself with the URL. The
// page is 0600 in a 0700 per-user directory (~/.swarmery/run), and is deleted
// shortly after the browser has read it; a page an earlier run left behind is
// swept on the next run.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/linksig"
)

// launcherGlob matches the launcher pages in the run dir.
const launcherGlob = "open-*.html"

// openRunDir is the per-user dir the launcher pages go in; launcherTTL is how
// long a launcher page lives before it is deleted (and past which an earlier
// run's page counts as stale). Tests replace both.
var (
	openRunDir  = defaultRunDir
	launcherTTL = 10 * time.Second
)

// defaultRunDir is ~/.swarmery/run.
func defaultRunDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot resolve the home dir for the launcher page: %w", err)
	}
	return filepath.Join(home, ".swarmery", "run"), nil
}

// ensureRunDir creates dir (and its parent) with mode 0700 and checks it is a
// real directory — a symlink is refused, never followed — owned by this user;
// an own directory with a looser mode is tightened to 0700.
func ensureRunDir(dir string) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return fmt.Errorf("%s is not a directory (a symlink is refused)", dir)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s is not owned by this user", dir)
	}
	if fi.Mode().Perm() != 0o700 {
		return os.Chmod(dir, 0o700)
	}
	return nil
}

// sweepLaunchers deletes launcher pages in dir older than launcherTTL: an
// earlier run whose delayed removal never ran.
func sweepLaunchers(dir string, now time.Time) {
	paths, _ := filepath.Glob(filepath.Join(dir, launcherGlob))
	for _, p := range paths {
		fi, err := os.Lstat(p)
		if err == nil && fi.Mode().IsRegular() && fi.ModTime().Before(now.Add(-launcherTTL)) {
			_ = os.Remove(p)
		}
	}
}

// launcherPage is the HTML that replaces itself with target. target is
// JSON-encoded, which escapes <, > and & too, so it cannot close the script.
// No referrer is sent on the navigation.
func launcherPage(target string) ([]byte, error) {
	js, err := json.Marshal(target)
	if err != nil {
		return nil, err
	}
	return []byte(`<!doctype html>
<html><head><meta charset="utf-8"><meta name="referrer" content="no-referrer"><title>swarmery</title></head>
<body><script>location.replace(` + string(js) + `)</script></body></html>
`), nil
}

// writeLauncher writes the launcher page for target into dir as a new 0600
// file (created exclusively, never through a symlink) and returns its path.
func writeLauncher(dir, target string) (string, error) {
	page, err := launcherPage(target)
	if err != nil {
		return "", err
	}
	name, err := linksig.NewNonce()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "open-"+name+".html")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(page); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

// prepareLauncher readies the run dir, sweeps stale pages and writes the page
// for target.
func prepareLauncher(target string) (string, error) {
	dir, err := openRunDir()
	if err != nil {
		return "", err
	}
	if err := ensureRunDir(dir); err != nil {
		return "", fmt.Errorf("launcher dir: %w", err)
	}
	sweepLaunchers(dir, time.Now())
	return writeLauncher(dir, target)
}

// removeLater deletes path after the given delay without holding the command
// up: a detached shell sleeps and removes it (only the path is on its command
// line, never the URL). If that cannot start, the delay is waited out here.
var removeLater = func(path string, after time.Duration) {
	secs := strconv.FormatFloat(after.Seconds(), 'f', 3, 64)
	cmd := exec.Command("/bin/sh", "-c", `sleep "$1"; rm -f -- "$2"`, "swarmery-open-cleanup", secs, path)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		time.Sleep(after)
		_ = os.Remove(path)
		return
	}
	_ = cmd.Process.Release()
}
