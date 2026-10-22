package runcore

import (
	"os"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeflags"
)

// ScrubVCSTokensEnv keeps agent spawns away from the daemon's code-host
// credentials. Scrubbing is ON by default: every spawn's environment loses the
// variables gh and glab read a token or a config dir from, so an agent can only
// reach a code host the way the daemon's own land path decides to — through
// internal/repoprovider, which runs from the daemon's own process and env and is
// never scrubbed (repoprovider/exec_scrub_test.go pins that). "0", "off",
// "false" or "no" opts out and leaves the env byte-identical; any other value,
// unset included, scrubs.
//
// It removes ENVIRONMENT-carried credentials only. Without the config-dir
// overrides gh/glab fall back to their default config and the keyring, where
// the operator's own `gh auth login` lives, so this is not a full credential
// boundary; and only spawns through this package are scrubbed (see the README
// for the paths that are not). A skill calling `gh`/`glab` itself loses API
// access only when that access came from an env token. SSH push is unaffected.
const ScrubVCSTokensEnv = "SWARMERY_AGENT_SCRUB_VCS_TOKENS"

// scrubbedVCSKeys are the variables removed under ScrubVCSTokensEnv: the
// tokens gh and glab read (GH_ENTERPRISE_TOKEN is the one gh reads for a GitHub
// Enterprise Server host, and credstore exports it there) and the config dirs
// that would point either CLI at a stored login.
var scrubbedVCSKeys = map[string]bool{
	"GH_TOKEN":            true,
	"GITHUB_TOKEN":        true,
	"GH_ENTERPRISE_TOKEN": true,
	"GLAB_TOKEN":          true,
	"GITLAB_TOKEN":        true,
	"GH_CONFIG_DIR":       true,
	"GLAB_CONFIG_DIR":     true,
}

// scrubVCSTokens applies ScrubVCSTokensEnv to a composed spawn env. Opted out it
// returns env itself — the same slice, no copy — so the opt-out is a
// byte-identical environment. Otherwise it returns a new slice without the
// scrubbed keys, every other entry kept in order.
func scrubVCSTokens(env []string) []string {
	if claudeflags.OptedOut(os.Getenv(ScrubVCSTokensEnv)) {
		return env
	}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		if scrubbedVCSKeys[key] {
			continue
		}
		out = append(out, kv)
	}
	return out
}
