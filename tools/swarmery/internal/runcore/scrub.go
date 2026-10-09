package runcore

import (
	"os"
	"strings"
)

// ScrubVCSTokensEnv opts agent spawns out of the daemon's code-host
// credentials. When it is "1", every spawn's environment loses the variables
// gh and glab read a token or a config dir from, so an agent can only reach a
// code host the way the daemon's own land path decides to — through
// internal/repoprovider. Any other value (unset included) leaves the env alone.
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

// scrubVCSTokens applies ScrubVCSTokensEnv to a composed spawn env. With the
// flag off it returns env itself — the same slice, no copy — so an operator who
// never set it gets a byte-identical environment. With it on it returns a new
// slice without the scrubbed keys, every other entry kept in order.
func scrubVCSTokens(env []string) []string {
	if os.Getenv(ScrubVCSTokensEnv) != "1" {
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
