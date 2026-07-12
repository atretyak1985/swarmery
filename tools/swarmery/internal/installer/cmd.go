package installer

import (
	"flag"
	"fmt"
	"os"
	"os/user"
	"runtime"
	"strconv"
)

// Version is the CLI version reported by `swarmery status`.
const Version = "0.1.0"

// CmdInstall implements `swarmery install [--port <n>]` (env: SWARMERY_PORT).
// A port explicitly configured at install time is baked into the plist's
// EnvironmentVariables; otherwise the daemon uses its built-in default.
func CmdInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	port := fs.Int("port", envPort(), "daemon HTTP port baked into the plist (env: SWARMERY_PORT; 0 = daemon default)")
	fs.Parse(args)
	if *port < 0 || *port > 65535 {
		return fmt.Errorf("invalid port %d", *port)
	}

	sys, err := realSystem()
	if err != nil {
		return err
	}
	sourceBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate current binary: %w", err)
	}
	return sys.Install(sourceBin, *port)
}

// CmdUninstall implements `swarmery uninstall`.
func CmdUninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	fs.Parse(args)
	sys, err := realSystem()
	if err != nil {
		return err
	}
	return sys.Uninstall()
}

// CmdStatus implements `swarmery status`.
func CmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	fs.Parse(args)
	sys, err := realSystem()
	if err != nil {
		return err
	}
	return sys.Status()
}

// realSystem wires a System against the actual host environment.
func realSystem() (*System, error) {
	if runtime.GOOS != "darwin" {
		return nil, fmt.Errorf("install/uninstall/status use launchd and are macOS-only (got %s)", runtime.GOOS)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home dir: %w", err)
	}
	u, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("resolve current user: %w", err)
	}
	return &System{Home: home, UID: u.Uid, Run: ExecRunner{}, Out: os.Stdout}, nil
}

// envPort mirrors the serve command's SWARMERY_PORT handling, but returns 0
// (meaning "not configured") when the variable is absent or invalid.
func envPort() int {
	if v := os.Getenv("SWARMERY_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 && p < 65536 {
			return p
		}
	}
	return 0
}
