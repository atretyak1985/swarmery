package installer

// Linux backend: the same three verbs (install / uninstall / service-status)
// over a `systemd --user` unit instead of a launchd LaunchAgent. Selected by
// runtime.GOOS in newService (cmd.go); the macOS path is untouched.
//
// Everything that is not the service manager itself — where the binary lives,
// where the logs go, which SWARMERY_* vars are baked in and how a reinstall
// preserves them — is shared with the launchd backend through the package
// helpers, so the two cannot drift on anything but the unit/plist syntax.
//
// All systemctl interaction goes through the Runner interface, like launchctl.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// UnitName is the systemd user unit for the swarmery daemon.
const UnitName = "swarmery.service"

// Systemd is the Linux counterpart of System: a user home, a Runner that
// executes systemctl / ps, and where progress is written.
type Systemd struct {
	Home string
	Run  Runner
	Out  io.Writer
}

// BinPath returns ~/.swarmery/bin/swarmery — the same path the launchd backend uses.
func (s *Systemd) BinPath() string { return homeBinPath(s.Home) }

// LogsDir returns ~/.swarmery/logs.
func (s *Systemd) LogsDir() string { return homeLogsDir(s.Home) }

// DBPath returns ~/.swarmery/swarmery.db.
func (s *Systemd) DBPath() string { return homeDBPath(s.Home) }

// UnitPath returns ~/.config/systemd/user/swarmery.service.
func (s *Systemd) UnitPath() string {
	return filepath.Join(s.Home, ".config", "systemd", "user", UnitName)
}

// DefinitionKind names the service definition in operator output ("unit file",
// where launchd says "plist").
func (s *Systemd) DefinitionKind() string { return "unit file" }

// Unit renders the systemd user unit for the daemon. Twin of Plist: the same
// binary path, the same logs, the same `serve` verb, the same baked
// environment (SWARMERY_PORT first when port > 0, then extra in order).
// Restart=on-failure is the KeepAlive equivalent; WantedBy=default.target is
// what makes `systemctl --user enable` start it at login.
func Unit(binPath, logsDir string, port int, extra ...EnvVar) string {
	entries := make([]EnvVar, 0, 1+len(extra))
	if port > 0 {
		entries = append(entries, EnvVar{Key: "SWARMERY_PORT", Value: fmt.Sprintf("%d", port)})
	}
	entries = append(entries, extra...)

	var env strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&env, "Environment=%s\n", unitQuote(e.Key+"="+e.Value))
	}
	var b strings.Builder
	fmt.Fprintf(&b, `[Unit]
Description=swarmery control plane daemon
After=default.target

[Service]
ExecStart=%s serve
Restart=on-failure
RestartSec=2
%sStandardOutput=append:%s/swarmery.out.log
StandardError=append:%s/swarmery.err.log

[Install]
WantedBy=default.target
`, binPath, env.String(), logsDir, logsDir)
	return b.String()
}

// unitQuote renders one Environment= assignment so systemd reads it back
// verbatim: double-quoted (values are paths and root lists, which carry
// spaces and commas), backslash and quote escaped, and `%` doubled because a
// bare `%` is a specifier (%h, %u, …) to systemd.
func unitQuote(assignment string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`)
	return `"` + r.Replace(assignment) + `"`
}

// unitUnquote is the inverse of unitQuote for one Environment= value.
func unitUnquote(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		v = v[1 : len(v)-1]
	}
	return strings.NewReplacer(`%%`, `%`, `\"`, `"`, `\\`, `\`).Replace(v)
}

// ExistingEnv reads the Environment= assignments out of the installed unit, so
// a reinstall can PRESERVE values the operator did not re-supply — the exact
// contract of System.ExistingPlistEnv. Best-effort: no unit, or a unit without
// assignments, yields nil.
func (s *Systemd) ExistingEnv() map[string]string {
	data, err := os.ReadFile(s.UnitPath())
	if err != nil {
		return nil
	}
	return parseUnitEnv(string(data))
}

// parseUnitEnv extracts KEY=value pairs from every `Environment=` line.
func parseUnitEnv(text string) map[string]string {
	var out map[string]string
	for _, line := range strings.Split(text, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "Environment=")
		if !ok {
			continue
		}
		k, v, found := strings.Cut(unitUnquote(rest), "=")
		if !found || k == "" {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[k] = v
	}
	return out
}

func (s *Systemd) systemctl(args ...string) (string, error) {
	return s.Run.Run("systemctl", append([]string{"--user"}, args...)...)
}

// active reports whether the unit is currently running in the user manager.
func (s *Systemd) active() bool {
	out, err := s.systemctl("is-active", UnitName)
	return err == nil && strings.TrimSpace(out) == "active"
}

// Install copies sourceBin into ~/.swarmery/bin, writes the user unit
// (embedding SWARMERY_PORT when port > 0), reloads the manager and enables
// the unit for login start. Re-running is idempotent: binary and unit are
// overwritten in place, and a unit that was already running is restarted so
// it picks up the new binary — `enable --now` alone leaves a running unit on
// its old inode.
func (s *Systemd) Install(sourceBin string, port int, env ...EnvVar) error {
	for _, dir := range []string{filepath.Dir(s.BinPath()), s.LogsDir(), filepath.Dir(s.UnitPath())} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	if err := copyBinary(sourceBin, s.BinPath()); err != nil {
		return err
	}
	fmt.Fprintf(s.Out, "installed binary: %s\n", s.BinPath())

	wasActive := s.active()
	if err := os.WriteFile(s.UnitPath(), []byte(Unit(s.BinPath(), s.LogsDir(), port, env...)), 0o644); err != nil {
		return fmt.Errorf("write unit: %w", err)
	}
	fmt.Fprintf(s.Out, "wrote unit: %s\n", s.UnitPath())
	if port > 0 {
		fmt.Fprintf(s.Out, "  SWARMERY_PORT=%d (Environment=)\n", port)
	}
	for _, e := range env {
		fmt.Fprintf(s.Out, "  %s=%s (Environment=)\n", e.Key, e.Value)
	}

	if out, err := s.systemctl("daemon-reload"); err != nil {
		return fmt.Errorf("systemctl --user daemon-reload: %v\n%s", err, out)
	}
	if out, err := s.systemctl("enable", "--now", UnitName); err != nil {
		return fmt.Errorf("systemctl --user enable --now %s: %v\n%s", UnitName, err, out)
	}
	if wasActive {
		if out, err := s.systemctl("restart", UnitName); err != nil {
			return fmt.Errorf("systemctl --user restart %s: %v\n%s", UnitName, err, out)
		}
		fmt.Fprintf(s.Out, "restarting existing service %s\n", UnitName)
	}
	fmt.Fprintf(s.Out, "service %s enabled in the user manager (Restart=on-failure, WantedBy=default.target)\n", UnitName)
	return nil
}

// Uninstall disables and stops the unit and removes its file. Logs
// (~/.swarmery/logs) and the database (~/.swarmery/swarmery.db) are
// intentionally preserved — uninstall is the rollback, not a purge.
func (s *Systemd) Uninstall() error {
	if _, err := os.Stat(s.UnitPath()); err == nil {
		if out, err := s.systemctl("disable", "--now", UnitName); err != nil {
			return fmt.Errorf("systemctl --user disable --now %s: %v\n%s", UnitName, err, out)
		}
		fmt.Fprintf(s.Out, "service %s disabled and stopped\n", UnitName)
		if err := os.Remove(s.UnitPath()); err != nil {
			return fmt.Errorf("remove unit: %w", err)
		}
		fmt.Fprintf(s.Out, "removed unit: %s\n", s.UnitPath())
		if out, err := s.systemctl("daemon-reload"); err != nil {
			return fmt.Errorf("systemctl --user daemon-reload: %v\n%s", err, out)
		}
	} else {
		fmt.Fprintf(s.Out, "unit already absent: %s — nothing to stop\n", s.UnitPath())
	}
	fmt.Fprintf(s.Out, "kept logs (%s) and database (%s)\n", s.LogsDir(), s.DBPath())
	return nil
}

// Status prints service health in the same shape as the launchd backend:
// state, PID, uptime, CLI version, and database size.
func (s *Systemd) Status() error {
	fmt.Fprintf(s.Out, "swarmery %s\n", Version)

	show, err := s.systemctl("show", "-p", "LoadState", "-p", "ActiveState", "-p", "MainPID", "-p", "ActiveEnterTimestamp", UnitName)
	props := parseShow(show)
	switch {
	case err != nil || props["LoadState"] != "loaded":
		fmt.Fprintf(s.Out, "  service: not installed (%s not loaded in the user manager)\n", UnitName)
	default:
		state := props["ActiveState"]
		if state == "" {
			state = "loaded"
		}
		fmt.Fprintf(s.Out, "  service: %s\n", state)
		if pid := props["MainPID"]; pid != "" && pid != "0" {
			fmt.Fprintf(s.Out, "  pid:     %s\n", pid)
			if up, err := s.Run.Run("ps", "-o", "etime=", "-p", pid); err == nil {
				fmt.Fprintf(s.Out, "  uptime:  %s\n", strings.TrimSpace(up))
			}
		}
		if since := props["ActiveEnterTimestamp"]; since != "" {
			fmt.Fprintf(s.Out, "  since:   %s\n", since)
		}
	}

	if st, err := os.Stat(s.UnitPath()); err == nil && !st.IsDir() {
		fmt.Fprintf(s.Out, "  unit:    %s\n", s.UnitPath())
	} else {
		fmt.Fprintf(s.Out, "  unit:    absent (%s)\n", s.UnitPath())
	}
	if st, err := os.Stat(s.DBPath()); err == nil && !st.IsDir() {
		fmt.Fprintf(s.Out, "  db:      %s (%s)\n", s.DBPath(), formatBytes(st.Size()))
	} else {
		fmt.Fprintf(s.Out, "  db:      absent (%s)\n", s.DBPath())
	}
	return nil
}

// parseShow turns `systemctl show` output (`Key=Value` per line) into a map.
func parseShow(out string) map[string]string {
	props := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok && k != "" {
			props[k] = v
		}
	}
	return props
}
