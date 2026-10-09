package repoprovider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/pluginreq"
)

// Config is a project's VCS settings: the `vcs` block of .claude/project.json,
// overridden per key by `swarmery.vcs` in .claude/settings.local.json (the
// operator's machine-local answers, e.g. "this self-hosted host is GitLab").
type Config struct {
	// Provider is "auto" / "" (detect), "github" or "gitlab".
	Provider string `json:"provider"`
	// BaseBranch is the change request's target; "" = the host default.
	BaseBranch string `json:"baseBranch"`
	// ForkRemote is reserved for the fork workflow (ErrForkUnsupported).
	ForkRemote string `json:"forkRemote"`
	// AllowPushToBase lifts the push-to-base refusal (ErrBaseRefused).
	AllowPushToBase bool `json:"allowPushToBase"`
}

// ExplicitKind is the provider the config pins, or "" for auto-detection.
func (c Config) ExplicitKind() Kind {
	switch Kind(strings.ToLower(strings.TrimSpace(c.Provider))) {
	case KindGitHub:
		return KindGitHub
	case KindGitLab:
		return KindGitLab
	default:
		return ""
	}
}

// configLayer decodes one source with "is this key present" semantics, so a
// later layer overrides only the keys it actually sets.
type configLayer struct {
	Provider        *string `json:"provider"`
	BaseBranch      *string `json:"baseBranch"`
	ForkRemote      *string `json:"forkRemote"`
	AllowPushToBase *bool   `json:"allowPushToBase"`
}

func (l configLayer) apply(c *Config) {
	if l.Provider != nil {
		c.Provider = strings.TrimSpace(*l.Provider)
	}
	if l.BaseBranch != nil {
		c.BaseBranch = strings.TrimSpace(*l.BaseBranch)
	}
	if l.ForkRemote != nil {
		c.ForkRemote = strings.TrimSpace(*l.ForkRemote)
	}
	if l.AllowPushToBase != nil {
		c.AllowPushToBase = *l.AllowPushToBase
	}
}

// LoadConfig merges project.json `vcs` with settings.local.json
// `swarmery.vcs`; the local file wins per key. A missing or malformed source
// contributes nothing — the zero Config means "auto-detect, host defaults,
// push to base refused".
func LoadConfig(projectPath string) Config {
	var c Config
	if raw, ok := pluginreq.ReadProjectConfig(projectPath)["vcs"]; ok {
		var l configLayer
		if json.Unmarshal(raw, &l) == nil {
			l.apply(&c)
		}
	}
	if l, ok := readLocalVCS(projectPath); ok {
		l.apply(&c)
	}
	return c
}

func readLocalVCS(projectPath string) (configLayer, bool) {
	raw, err := os.ReadFile(filepath.Join(projectPath, ".claude", "settings.local.json"))
	if err != nil {
		return configLayer{}, false
	}
	var doc struct {
		Swarmery struct {
			VCS json.RawMessage `json:"vcs"`
		} `json:"swarmery"`
	}
	if json.Unmarshal(raw, &doc) != nil || len(doc.Swarmery.VCS) == 0 {
		return configLayer{}, false
	}
	var l configLayer
	if json.Unmarshal(doc.Swarmery.VCS, &l) != nil {
		return configLayer{}, false
	}
	return l, true
}
