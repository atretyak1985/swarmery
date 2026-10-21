package gitlab

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider"
)

// FindChangeRequest (land-criteria UX plan, phase 4, D7): `glab mr list
// --source-branch` over a FakeExec — no real glab, git or network.

const findCall = "glab mr list --source-branch swarm/phase-7 --all --output json -R gitlab.example.com/group/sub/widgets"

func TestFindChangeRequestChoice(t *testing.T) {
	const base = "https://gitlab.example.com/group/sub/widgets/-/merge_requests/"
	cases := []struct {
		name      string
		out       string
		wantFound bool
		wantNum   int
	}{
		{"open beats merged", `[
			{"iid":3,"web_url":"` + base + `3","state":"merged","merged_at":"2026-10-20T10:00:00.123Z"},
			{"iid":5,"web_url":"` + base + `5","state":"opened","merged_at":null},
			{"iid":4,"web_url":"` + base + `4","state":"closed","merged_at":null}]`, true, 5},
		{"only merged: the latest merge", `[
			{"iid":8,"web_url":"` + base + `8","state":"merged","merged_at":"2026-10-19T10:00:00Z"},
			{"iid":6,"web_url":"` + base + `6","state":"merged","merged_at":"2026-10-20T10:00:00Z"}]`, true, 6},
		{"only closed: not found", `[{"iid":2,"web_url":"` + base + `2","state":"closed"}]`, false, 0},
		{"iid from the URL", `[{"web_url":"` + base + `12","state":"opened"}]`, true, 12},
		{"empty list", `[]`, false, 0},
		{"null", `null`, false, 0},
		{"no output", ``, false, 0},
	}
	for _, c := range cases {
		f := &repoprovider.FakeExec{Out: map[string]string{"glab mr": c.out}}
		ref, found, err := New(f, storeEnv).FindChangeRequest(context.Background(), target, "swarm/phase-7")
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if found != c.wantFound || ref.Number != c.wantNum {
			t.Errorf("%s: got %+v found=%v, want !%d found=%v", c.name, ref, found, c.wantNum, c.wantFound)
		}
		if found && (ref.Provider != repoprovider.KindGitLab || ref.URL == "") {
			t.Errorf("%s: ref = %+v, want a gitlab ref with a URL", c.name, ref)
		}
		if len(f.Calls) != 1 || f.Calls[0] != findCall {
			t.Errorf("%s: calls = %q, want [%q]", c.name, f.Calls, findCall)
		}
		if len(f.Envs) != 1 || !slices.Contains(f.Envs[0], hostEnv+"="+host) {
			t.Errorf("%s: env = %q, want %s=%s", c.name, f.Envs, hostEnv, host)
		}
	}
}

func TestFindChangeRequestFailures(t *testing.T) {
	ctx := context.Background()
	expired := &repoprovider.FakeExec{Errs: map[string]string{"glab mr": "HTTP 401 Unauthorized"}}
	if _, _, err := New(expired, nil).FindChangeRequest(ctx, target, "swarm/phase-7"); !errors.Is(err, repoprovider.ErrNotAuthenticated) {
		t.Errorf("401: %v, want ErrNotAuthenticated", err)
	}
	missing := &repoprovider.FakeExec{Missing: map[string]bool{"glab": true}}
	if _, _, err := New(missing, nil).FindChangeRequest(ctx, target, "swarm/phase-7"); !errors.Is(err, repoprovider.ErrBinaryMissing) {
		t.Errorf("missing glab: %v, want ErrBinaryMissing", err)
	}
	garbage := &repoprovider.FakeExec{Out: map[string]string{"glab mr": "not json"}}
	if _, found, err := New(garbage, nil).FindChangeRequest(ctx, target, "swarm/phase-7"); err == nil || found {
		t.Errorf("garbage: found=%v err=%v, want an error", found, err)
	}
	smuggled := &repoprovider.FakeExec{}
	if _, _, err := New(smuggled, nil).FindChangeRequest(ctx, target, "-R evil"); !errors.Is(err, repoprovider.ErrInvalidRef) {
		t.Errorf("flag-shaped head: %v, want ErrInvalidRef", err)
	}
	if len(smuggled.Calls) != 0 {
		t.Errorf("a refused head reached glab: %v", smuggled.Calls)
	}
}
