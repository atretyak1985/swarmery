// Package installer manages the daemon's auto-start integration: `swarmery
// install` copies the binary to ~/.swarmery/bin and registers a service,
// `uninstall` removes the service and its definition (keeping logs and DB),
// and `service-status` reports health. Two backends behind the same verbs,
// chosen by runtime.GOOS: a launchd LaunchAgent (RunAtLoad + KeepAlive) on
// macOS — this file — and a `systemd --user` unit on Linux (systemd.go).
//
// All launchctl / systemctl interaction goes through the Runner interface so
// logic can be tested without a real service manager.
package installer

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// System bundles the environment the installer operates on. Tests use a
// temp Home and a fake Runner; production wires os.UserHomeDir + ExecRunner.
type System struct {
	Home  string              // user home directory
	UID   string              // numeric user id, for the gui/<uid> launchd domain
	Run   Runner              // executes launchctl / ps
	Out   io.Writer           // human-readable progress output
	Sleep func(time.Duration) // nil → time.Sleep (tests inject a no-op)
}

// launchd bootout is asynchronous: the service can linger in the domain for a
// moment after `launchctl bootout` returns, so an immediate bootstrap fails
// with exit 5 (Input/output error). Reinstall therefore polls until the old
// registration disappears and retries bootstrap with a short backoff.
const (
	unregisterPolls   = 10
	unregisterDelay   = 200 * time.Millisecond
	bootstrapAttempts = 5
	bootstrapBackoff  = 500 * time.Millisecond
)

func (s *System) sleep(d time.Duration) {
	if s.Sleep != nil {
		s.Sleep(d)
		return
	}
	time.Sleep(d)
}

// The daemon's home layout is the same under launchd and systemd; both
// backends resolve it through these helpers so the two can never disagree.
func homeBinPath(home string) string { return filepath.Join(home, ".swarmery", "bin", "swarmery") }
func homeLogsDir(home string) string { return filepath.Join(home, ".swarmery", "logs") }
func homeDBPath(home string) string  { return filepath.Join(home, ".swarmery", "swarmery.db") }

// BinPath returns ~/.swarmery/bin/swarmery.
func (s *System) BinPath() string { return homeBinPath(s.Home) }

// LogsDir returns ~/.swarmery/logs.
func (s *System) LogsDir() string { return homeLogsDir(s.Home) }

// PlistPath returns ~/Library/LaunchAgents/com.swarmery.daemon.plist.
func (s *System) PlistPath() string {
	return filepath.Join(s.Home, "Library", "LaunchAgents", Label+".plist")
}

// DBPath returns ~/.swarmery/swarmery.db.
func (s *System) DBPath() string { return homeDBPath(s.Home) }

// ExistingEnv is ExistingPlistEnv under the name the service interface uses.
func (s *System) ExistingEnv() map[string]string { return s.ExistingPlistEnv() }

// DefinitionKind names the service definition in operator output.
func (s *System) DefinitionKind() string { return "plist" }

// ExistingPlistEnv reads the EnvironmentVariables dict out of the currently
// installed plist so a reinstall can PRESERVE values the operator did not
// re-supply — a bare `swarmery install` must never silently drop the onboarding
// allow-list (or any other baked var). Best-effort: an absent plist, a plutil
// failure, or a plist without the block all yield nil, and the caller simply
// preserves nothing.
func (s *System) ExistingPlistEnv() map[string]string {
	if _, err := os.Stat(s.PlistPath()); err != nil {
		return nil
	}
	out, err := s.Run.Run("plutil", "-convert", "json", "-o", "-", s.PlistPath())
	if err != nil {
		return nil
	}
	var doc struct {
		Env map[string]string `json:"EnvironmentVariables"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		return nil
	}
	return doc.Env
}

func (s *System) domain() string        { return "gui/" + s.UID }
func (s *System) serviceTarget() string { return s.domain() + "/" + Label }

// registered reports whether the service is currently known to launchd.
func (s *System) registered() bool {
	_, err := s.Run.Run("launchctl", "print", s.serviceTarget())
	return err == nil
}

// Install copies sourceBin into ~/.swarmery/bin, writes the LaunchAgent
// plist (embedding SWARMERY_PORT when port > 0), and (re)registers the
// service via `launchctl bootstrap`. Re-running is idempotent: the binary
// and plist are overwritten in place and an already-registered service is
// booted out before being bootstrapped again — never duplicated.
func (s *System) Install(sourceBin string, port int, env ...EnvVar) error {
	for _, dir := range []string{filepath.Dir(s.BinPath()), s.LogsDir(), filepath.Dir(s.PlistPath())} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}

	if err := copyBinary(sourceBin, s.BinPath()); err != nil {
		return err
	}
	fmt.Fprintf(s.Out, "installed binary: %s\n", s.BinPath())

	if err := os.WriteFile(s.PlistPath(), []byte(Plist(s.BinPath(), s.LogsDir(), port, env...)), 0o644); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}
	fmt.Fprintf(s.Out, "wrote plist: %s\n", s.PlistPath())
	if port > 0 {
		fmt.Fprintf(s.Out, "  SWARMERY_PORT=%d (EnvironmentVariables)\n", port)
	}
	for _, e := range env {
		fmt.Fprintf(s.Out, "  %s=%s (EnvironmentVariables)\n", e.Key, e.Value)
	}

	// Idempotent restart: unload an existing registration before loading the
	// fresh one so a second install updates rather than duplicates. bootout is
	// async — wait until launchd actually forgets the service.
	if s.registered() {
		if out, err := s.Run.Run("launchctl", "bootout", s.serviceTarget()); err != nil {
			return fmt.Errorf("launchctl bootout: %v\n%s", err, out)
		}
		fmt.Fprintf(s.Out, "restarting existing service %s\n", Label)
		s.waitUnregistered()
	}
	if err := s.bootstrapWithRetry(); err != nil {
		return err
	}
	fmt.Fprintf(s.Out, "service %s registered in %s (RunAtLoad + KeepAlive)\n", Label, s.domain())
	return nil
}

// waitUnregistered polls `launchctl print` until the service disappears from
// the domain (bounded); a lingering registration is tolerated because
// bootstrapWithRetry absorbs the residual race.
func (s *System) waitUnregistered() {
	for i := 0; i < unregisterPolls && s.registered(); i++ {
		s.sleep(unregisterDelay)
	}
}

// bootstrapWithRetry runs `launchctl bootstrap`, retrying with a short backoff
// — right after a bootout the domain can still hold the dying service and
// bootstrap fails with exit 5 (Input/output error) until launchd catches up.
func (s *System) bootstrapWithRetry() error {
	var out string
	var err error
	for attempt := 1; attempt <= bootstrapAttempts; attempt++ {
		if attempt > 1 {
			s.sleep(bootstrapBackoff)
			fmt.Fprintf(s.Out, "retrying launchctl bootstrap (attempt %d/%d)\n", attempt, bootstrapAttempts)
		}
		if out, err = s.Run.Run("launchctl", "bootstrap", s.domain(), s.PlistPath()); err == nil {
			return nil
		}
	}
	return fmt.Errorf("launchctl bootstrap failed after %d attempts: %v\n%s", bootstrapAttempts, err, out)
}

// Uninstall boots the service out of launchd and deletes the plist.
// Logs (~/.swarmery/logs) and the database (~/.swarmery/swarmery.db) are
// intentionally preserved — uninstall is the rollback, not a purge.
func (s *System) Uninstall() error {
	if s.registered() {
		if out, err := s.Run.Run("launchctl", "bootout", s.serviceTarget()); err != nil {
			return fmt.Errorf("launchctl bootout: %v\n%s", err, out)
		}
		fmt.Fprintf(s.Out, "service %s removed from %s\n", Label, s.domain())
	} else {
		fmt.Fprintf(s.Out, "service %s not registered — nothing to boot out\n", Label)
	}
	if err := os.Remove(s.PlistPath()); err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(s.Out, "plist already absent: %s\n", s.PlistPath())
		} else {
			return fmt.Errorf("remove plist: %w", err)
		}
	} else {
		fmt.Fprintf(s.Out, "removed plist: %s\n", s.PlistPath())
	}
	fmt.Fprintf(s.Out, "kept logs (%s) and database (%s)\n", s.LogsDir(), s.DBPath())
	return nil
}

// Status prints service health: registration/running state, PID, uptime,
// CLI version, and database size.
func (s *System) Status() error {
	fmt.Fprintf(s.Out, "swarmery %s\n", Version)

	out, err := s.Run.Run("launchctl", "print", s.serviceTarget())
	switch {
	case err != nil:
		fmt.Fprintf(s.Out, "  service: not installed (%s not in %s)\n", Label, s.domain())
	default:
		pid := parseLaunchdField(out, "pid")
		state := parseLaunchdField(out, "state")
		if state == "" {
			state = "registered"
		}
		fmt.Fprintf(s.Out, "  service: %s\n", state)
		if pid != "" {
			fmt.Fprintf(s.Out, "  pid:     %s\n", pid)
			if up, err := s.Run.Run("ps", "-o", "etime=", "-p", pid); err == nil {
				fmt.Fprintf(s.Out, "  uptime:  %s\n", strings.TrimSpace(up))
			}
		}
	}

	if st, err := os.Stat(s.PlistPath()); err == nil && !st.IsDir() {
		fmt.Fprintf(s.Out, "  plist:   %s\n", s.PlistPath())
	} else {
		fmt.Fprintf(s.Out, "  plist:   absent (%s)\n", s.PlistPath())
	}
	if st, err := os.Stat(s.DBPath()); err == nil && !st.IsDir() {
		fmt.Fprintf(s.Out, "  db:      %s (%s)\n", s.DBPath(), formatBytes(st.Size()))
	} else {
		fmt.Fprintf(s.Out, "  db:      absent (%s)\n", s.DBPath())
	}
	return nil
}

// parseLaunchdField extracts `key = value` from `launchctl print` output.
func parseLaunchdField(out, key string) string {
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(trimmed, key+" = "); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// copyBinary copies src over dst atomically (temp file + rename) so a
// running daemon keeps executing its old inode until launchd restarts it.
func copyBinary(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source binary: %w", err)
	}
	defer in.Close()

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".swarmery-*")
	if err != nil {
		return fmt.Errorf("create temp binary: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op after successful rename
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return fmt.Errorf("copy binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp binary: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return fmt.Errorf("chmod binary: %w", err)
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return fmt.Errorf("install binary: %w", err)
	}
	return nil
}

// formatBytes renders a byte count in a compact human unit.
func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
