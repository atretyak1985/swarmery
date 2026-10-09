// Package providers maps a repoprovider.Kind to its Provider implementation.
//
// It lives apart from repoprovider because the implementations (github,
// gitlab) import repoprovider; a factory inside the parent would close an
// import cycle. Callers (board land, phase landing, the status poller) import
// this package and repoprovider; nothing in repoprovider imports it.
package providers

import (
	"fmt"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider/github"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider/gitlab"
)

// Factory builds the Provider for kind over ex, with env as the per-host
// credential env delta (production: credstore.Env; nil = no delta). Any kind
// other than github or gitlab — KindUnknown included — is
// repoprovider.ErrUnknownProvider.
func Factory(kind repoprovider.Kind, ex repoprovider.Exec, env func(host string) []string) (repoprovider.Provider, error) {
	switch kind {
	case repoprovider.KindGitHub:
		return github.New(ex, env), nil
	case repoprovider.KindGitLab:
		return gitlab.New(ex, env), nil
	default:
		return nil, fmt.Errorf("%w: %q", repoprovider.ErrUnknownProvider, kind)
	}
}
