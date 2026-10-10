package installer

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudebin"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/route"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/version"
)

// Version is the build identity reported by `swarmery status` — the semver of
// the release line plus the commit it was built from, resolved by the single
// shared source in internal/version (whose bare semver /api/health serves).
var Version = version.String()

// installEnvKeys maps each install flag to the SWARMERY_* var it bakes into the
// plist. launchd does not inherit the installing shell's environment, so these
// are the only way to configure the daemon under launchd.
var installEnvKeys = []struct {
	flag, env string
	// noShellEnv drops the shell-environment step of the precedence chain for
	// this var, leaving flag > existing plist. Only CLAUDE_CONFIG_DIR sets it:
	// the SWARMERY_* vars are deliberate, but CLAUDE_CONFIG_DIR is ambient in
	// every session started with an explicit config dir, so reading it from the
	// installing shell would silently bake THAT shell's account into the daemon
	// — `swarmery install` run from a second-account session would quietly
	// re-home every background run.
	noShellEnv bool
}{
	{flag: "onboard-roots", env: "SWARMERY_ONBOARD_ROOTS"},
	{flag: "workspace-root", env: "SWARMERY_WORKSPACE_ROOT"},
	{flag: "statusline-src", env: "SWARMERY_STATUSLINE_SRC"},
	{flag: "projects-roots", env: "SWARMERY_PROJECTS_ROOTS"},
	{flag: "trusted-origins", env: "SWARMERY_TRUSTED_ORIGINS"},
	{flag: "claude-config-dir", env: "CLAUDE_CONFIG_DIR", noShellEnv: true},
	// The daemon must resolve the REAL claude, never the accounts-pack PATH
	// shim: an explicit SWARMERY_CLAUDE_BIN short-circuits claudebin.Resolve
	// before any PATH lookup (risk R5).
	{flag: "claude-bin", env: "SWARMERY_CLAUDE_BIN"},
	// The complexity router's phase-run surface and its policy file. The mode
	// is read per phase run, so launchd's env is the only place an `active`
	// switch can live; docs/routing-active.md is the enable/rollback runbook.
	{flag: "route-phaserun", env: routePhaserunEnv},
	{flag: "route-policy", env: route.EnvPolicy},
}

// routePhaserunEnv mirrors internal/phaserun's routeModeEnv (unexported there).
const routePhaserunEnv = "SWARMERY_ROUTE_PHASERUN"

// CmdInstall implements
//
//	swarmery install [--port <n>] [--onboard-roots <dirs>]
//	                 [--workspace-root <dir>] [--statusline-src <dir>]
//	                 [--projects-roots <dirs|auto>] [--trusted-origins <origins>]
//	                 [--claude-config-dir <dir>] [--claude-bin <path>]
//	                 [--route-phaserun off|shadow|active] [--route-policy <abs path>]
//
// Anything the daemon needs at runtime is baked into the plist's
// EnvironmentVariables. Because Install rewrites the whole plist, a bare
// reinstall would otherwise WIPE previously-baked vars (silently disabling
// onboarding); to prevent that, any var not re-supplied on this run is
// PRESERVED from the existing plist. Precedence per var: explicit flag > shell
// env > existing plist. --onboard-roots enables POST /api/projects/onboard (and
// the dashboard "new project" button); clear it deliberately with --flag "".
func CmdInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	// Defaults are empty (NOT the env): resolution — flag > env > prev plist —
	// happens in mergeInstallEnv so preservation works. Explicitness is read
	// from fs.Visit, so an unset flag can fall back instead of clobbering.
	port := fs.Int("port", -1, "daemon HTTP port baked into the plist (env: SWARMERY_PORT; default keeps the existing/daemon value)")
	onboardRoots := fs.String("onboard-roots", "",
		"comma-separated allow-list of parent dirs enabling project onboarding (env: SWARMERY_ONBOARD_ROOTS)")
	workspaceRoot := fs.String("workspace-root", "",
		"shared workspace repo root baked into the plist (env: SWARMERY_WORKSPACE_ROOT)")
	statuslineSrc := fs.String("statusline-src", "",
		"plugins/core/statusline dir onboarding copies from (env: SWARMERY_STATUSLINE_SRC)")
	projectsRoots := fs.String("projects-roots", "",
		"comma-separated transcript roots, or 'auto' = every ~/.claude*/projects — what makes "+
			"a second account's sessions and usage visible (env: SWARMERY_PROJECTS_ROOTS)")
	trustedOrigins := fs.String("trusted-origins", "",
		"comma-separated extra browser origins (scheme://host[:port]) allowed through the "+
			"cross-origin fence on writes and /api/ws, and whose hosts pass the Host fence on every "+
			"route — set it when the dashboard is reached by any name or port other than the daemon's "+
			"own loopback address (env: SWARMERY_TRUSTED_ORIGINS; empty = the daemon's own origin only)")
	claudeConfigDir := fs.String("claude-config-dir", "",
		"CLAUDE_CONFIG_DIR baked into the plist — the account every daemon-spawned run uses when a "+
			"project has no binding. Set it when the operator's own sessions always export one: Claude "+
			"Code namespaces its keychain credential per config dir, so an unset var sends the daemon "+
			"to a DIFFERENT (often empty) credential than the interactive shell uses, and every run "+
			"fails with \"OAuth session expired\". Unlike the SWARMERY_* vars this one is never read "+
			"from the installing shell — only this flag or the existing plist.")
	claudeBin := fs.String("claude-bin", "",
		"absolute path of the REAL claude executable baked into the plist (env: SWARMERY_CLAUDE_BIN) — "+
			"resolve it with the accounts-pack shim dir (~/.swarmery/bin) stripped from PATH, never the shim itself")
	routePhaserun := fs.String("route-phaserun", "",
		"complexity-router mode for plan phase runs: off, shadow or active (env: "+routePhaserunEnv+
			"; unset = the daemon default, shadow). See docs/routing-active.md")
	routePolicy := fs.String("route-policy", "",
		"absolute path of the route policy JSON the router loads (e.g. config/route-policy.phaserun.json); "+
			"it must load (env: "+route.EnvPolicy+"; unset = the in-code DefaultPolicy; clear with --route-policy \"\")")
	fs.Parse(args)
	if *port != -1 && (*port < 0 || *port > 65535) {
		return fmt.Errorf("invalid port %d", *port)
	}

	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	sys, err := realService()
	if err != nil {
		return err
	}
	prev := sys.ExistingEnv()

	env, preserved := mergeInstallEnv(prev, set, map[string]string{
		"onboard-roots":   *onboardRoots,
		"workspace-root":  *workspaceRoot,
		"statusline-src":  *statuslineSrc,
		"projects-roots":  *projectsRoots,
		"trusted-origins": *trustedOrigins,

		"claude-config-dir": *claudeConfigDir,
		"claude-bin":        *claudeBin,
		"route-phaserun":    *routePhaserun,
		"route-policy":      *routePolicy,
	}, os.LookupEnv)
	if err := validateInstallEnv(env); err != nil {
		return err
	}
	for _, k := range preserved {
		fmt.Fprintf(os.Stdout, "  preserving %s from existing %s\n", k, sys.DefinitionKind())
	}
	resolvedPort := resolveInstallPort(prev, set["port"], *port)

	sourceBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate current binary: %w", err)
	}
	return sys.Install(sourceBin, resolvedPort, env...)
}

// mergeInstallEnv resolves the plist EnvironmentVariables for an install run.
// Per var the precedence is explicit flag > shell env > existing plist (prev),
// so a bare reinstall preserves what was baked before instead of wiping it.
// preserved lists the vars carried over from prev (nothing was re-supplied) so
// the caller can report them. Explicitly passing an empty flag clears the var.
func mergeInstallEnv(
	prev map[string]string,
	set map[string]bool,
	flags map[string]string,
	getenv func(string) (string, bool),
) (env []EnvVar, preserved []string) {
	handled := make(map[string]bool, len(installEnvKeys))
	for _, k := range installEnvKeys {
		var val string
		switch {
		case set[k.flag]:
			val = strings.TrimSpace(flags[k.flag])
		default:
			if v, ok := getenv(k.env); ok && !k.noShellEnv {
				val = strings.TrimSpace(v)
			} else if p, ok := prev[k.env]; ok {
				val = p
				if p != "" {
					preserved = append(preserved, k.env)
				}
			}
		}
		if val != "" {
			env = append(env, EnvVar{Key: k.env, Value: val})
		}
		handled[k.env] = true
	}

	// Carry over every OTHER var already baked into the plist. ExistingPlistEnv
	// promises to preserve "the onboarding allow-list (or any other baked var)",
	// but the loop above only knows the handful with flags — so a var an operator
	// baked in by hand was silently WIPED by the next `swarmery install`. The
	// daemon reads ~60 SWARMERY_* knobs and only four have flags; on this machine
	// SWARMERY_EXCLUDE was one hand-baked var away from being lost that way.
	// SWARMERY_PORT is excluded because resolveInstallPort owns it and plist.go
	// emits it separately — passing it through here would duplicate the key.
	extra := make([]string, 0, len(prev))
	for k := range prev {
		if handled[k] || k == "SWARMERY_PORT" || strings.TrimSpace(prev[k]) == "" {
			continue
		}
		extra = append(extra, k)
	}
	sort.Strings(extra) // deterministic plist output
	for _, k := range extra {
		env = append(env, EnvVar{Key: k, Value: prev[k]})
		preserved = append(preserved, k)
	}
	return env, preserved
}

// validateInstallEnv refuses a resolved plist env the daemon could not use. It
// runs on the merged values, so a bad value is caught whether it came from a
// flag, the installing shell or the existing definition:
//
//   - SWARMERY_CLAUDE_BIN must name the real claude (validateClaudeBin);
//   - SWARMERY_ROUTE_PHASERUN must be off, shadow or active — the daemon reads
//     anything else as shadow with only a log line, so a typo'd "actve" would
//     look installed and change nothing;
//   - SWARMERY_ROUTE_POLICY must be absolute and load — an unloadable policy
//     makes the router record nothing for every phase run AND every dispatch.
func validateInstallEnv(env []EnvVar) error {
	for _, e := range env {
		switch e.Key {
		case "SWARMERY_CLAUDE_BIN":
			if err := validateClaudeBin(e.Value); err != nil {
				return err
			}
		case routePhaserunEnv:
			switch route.Mode(strings.ToLower(strings.TrimSpace(e.Value))) { // as route.ModeFromEnv reads it
			case route.ModeOff, route.ModeShadow, route.ModeActive:
			default:
				return fmt.Errorf("%s %q is not one of off, shadow, active (pass --route-phaserun \"\" to clear it)",
					routePhaserunEnv, e.Value)
			}
		case route.EnvPolicy:
			const hint = ` (pass --route-policy "" to clear it)`
			if !filepath.IsAbs(e.Value) {
				return fmt.Errorf("%s %q is not an absolute path — launchd does not run in your checkout%s",
					route.EnvPolicy, e.Value, hint)
			}
			if _, err := route.LoadPolicy(e.Value); err != nil {
				return fmt.Errorf("%s: %w%s", route.EnvPolicy, err, hint)
			}
		}
	}
	return nil
}

// validateClaudeBin refuses a SWARMERY_CLAUDE_BIN (from --claude-bin, the
// shell, or the existing definition) that the daemon could not use as the REAL
// claude: it must be absolute, exist, be an executable non-directory, and not
// live under the accounts-pack shim dir — the override short-circuits every
// shim filter in claudebin.Resolve, so baking the shim in would loop every
// daemon spawn back through `swarmery account exec` (risk R5).
func validateClaudeBin(p string) error {
	const hint = ` (pass --claude-bin "" to clear it)`
	if !filepath.IsAbs(p) {
		return fmt.Errorf("SWARMERY_CLAUDE_BIN %q is not an absolute path%s", p, hint)
	}
	fi, err := os.Stat(p)
	if err != nil {
		return fmt.Errorf("SWARMERY_CLAUDE_BIN %q: %w%s", p, err, hint)
	}
	if fi.IsDir() || fi.Mode()&0o111 == 0 {
		return fmt.Errorf("SWARMERY_CLAUDE_BIN %q is not an executable file%s", p, hint)
	}
	if claudebin.UnderShimDir(p) {
		return fmt.Errorf("SWARMERY_CLAUDE_BIN %q is under the accounts-pack shim dir %s — name the real claude, never the shim%s",
			p, claudebin.ShimDir(), hint)
	}
	return nil
}

// resolveInstallPort mirrors mergeInstallEnv for the special-cased port: an
// explicit --port wins, else SWARMERY_PORT, else the port baked into the
// existing plist, else 0 (daemon default).
func resolveInstallPort(prev map[string]string, explicit bool, flagVal int) int {
	if explicit {
		return flagVal
	}
	if p := envPort(); p != 0 {
		return p
	}
	if v, ok := prev["SWARMERY_PORT"]; ok {
		if p, err := strconv.Atoi(v); err == nil && p > 0 && p < 65536 {
			return p
		}
	}
	return 0
}

// CmdUninstall implements `swarmery uninstall`.
func CmdUninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	fs.Parse(args)
	sys, err := realService()
	if err != nil {
		return err
	}
	return sys.Uninstall()
}

// CmdStatus implements `swarmery status`.
func CmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	fs.Parse(args)
	sys, err := realService()
	if err != nil {
		return err
	}
	return sys.Status()
}

// service is what the three verbs need from a backend. Two implementations:
// *System (launchd, macOS) and *Systemd (systemd --user, Linux).
type service interface {
	Install(sourceBin string, port int, env ...EnvVar) error
	Uninstall() error
	Status() error
	// ExistingEnv reads the environment baked into the installed service
	// definition, so a reinstall can preserve what was not re-supplied.
	ExistingEnv() map[string]string
	// DefinitionKind names that definition in operator output ("plist", "unit file").
	DefinitionKind() string
}

// realService wires the backend for the actual host environment.
func realService() (service, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home dir: %w", err)
	}
	u, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("resolve current user: %w", err)
	}
	return newService(runtime.GOOS, home, u.Uid, ExecRunner{}, os.Stdout)
}

// newService is realService's pure core: the GOOS switch, with every input
// injectable so a test can exercise the choice on any host.
func newService(goos, home, uid string, run Runner, out io.Writer) (service, error) {
	switch goos {
	case "darwin":
		return &System{Home: home, UID: uid, Run: run, Out: out}, nil
	case "linux":
		return &Systemd{Home: home, Run: run, Out: out}, nil
	default:
		return nil, fmt.Errorf("install/uninstall/service-status need launchd (macOS) or systemd --user (Linux); got %s", goos)
	}
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
