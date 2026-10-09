package providers

import (
	"errors"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider"
)

func TestFactoryResolvesKnownKinds(t *testing.T) {
	for _, c := range []struct {
		kind   repoprovider.Kind
		change string
	}{
		{repoprovider.KindGitHub, "Pull Request"},
		{repoprovider.KindGitLab, "Merge Request"},
	} {
		p, err := Factory(c.kind, &repoprovider.FakeExec{}, nil)
		if err != nil {
			t.Fatalf("%s: %v", c.kind, err)
		}
		if p.Kind() != c.kind || p.Terms().Change != c.change {
			t.Errorf("%s: Kind %q Terms %+v", c.kind, p.Kind(), p.Terms())
		}
	}
}

func TestFactoryRefusesUnknown(t *testing.T) {
	for _, k := range []repoprovider.Kind{repoprovider.KindUnknown, "", "bitbucket", "GitHub"} {
		p, err := Factory(k, &repoprovider.FakeExec{}, nil)
		if !errors.Is(err, repoprovider.ErrUnknownProvider) || p != nil {
			t.Errorf("Factory(%q) = %v, %v; want nil, ErrUnknownProvider", k, p, err)
		}
	}
}
