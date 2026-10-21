package github

import (
	"context"
	"errors"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider"
)

// FindChangeRequest (land-criteria UX plan, phase 4, D7): `gh pr list --head`
// over a FakeExec — no real gh, git or network.

const findCall = "gh pr list --head swarm/phase-7 --state all --json number,url,state,mergedAt --limit 20 --repo acme/widgets"

func TestFindChangeRequestChoice(t *testing.T) {
	cases := []struct {
		name      string
		out       string
		wantFound bool
		wantNum   int
	}{
		{"open beats merged", `[
			{"number":3,"url":"https://github.com/acme/widgets/pull/3","state":"MERGED","mergedAt":"2026-10-20T10:00:00Z"},
			{"number":5,"url":"https://github.com/acme/widgets/pull/5","state":"OPEN","mergedAt":null},
			{"number":4,"url":"https://github.com/acme/widgets/pull/4","state":"CLOSED","mergedAt":null}]`, true, 5},
		{"only merged: the latest merge", `[
			{"number":8,"url":"https://github.com/acme/widgets/pull/8","state":"MERGED","mergedAt":"2026-10-19T10:00:00Z"},
			{"number":6,"url":"https://github.com/acme/widgets/pull/6","state":"MERGED","mergedAt":"2026-10-20T10:00:00Z"}]`, true, 6},
		{"only closed: not found", `[{"number":2,"url":"https://github.com/acme/widgets/pull/2","state":"CLOSED","mergedAt":null}]`, false, 0},
		{"zero mergedAt on an open PR stays open", `[{"number":9,"url":"https://github.com/acme/widgets/pull/9","state":"OPEN","mergedAt":"0001-01-01T00:00:00Z"}]`, true, 9},
		{"number from the URL", `[{"url":"https://github.com/acme/widgets/pull/12","state":"OPEN"}]`, true, 12},
		{"empty list", `[]`, false, 0},
		{"no output", ``, false, 0},
	}
	for _, c := range cases {
		f := &repoprovider.FakeExec{Out: map[string]string{"gh pr": c.out}}
		ref, found, err := New(f, storeEnv).FindChangeRequest(context.Background(), target, "swarm/phase-7")
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if found != c.wantFound || ref.Number != c.wantNum {
			t.Errorf("%s: got %+v found=%v, want #%d found=%v", c.name, ref, found, c.wantNum, c.wantFound)
		}
		if found && (ref.Provider != repoprovider.KindGitHub || ref.URL == "") {
			t.Errorf("%s: ref = %+v, want a github ref with a URL", c.name, ref)
		}
		if len(f.Calls) != 1 || f.Calls[0] != findCall {
			t.Errorf("%s: calls = %q, want [%q]", c.name, f.Calls, findCall)
		}
		if len(f.Envs) != 1 || len(f.Envs[0]) == 0 || f.Envs[0][0] != storeEnv("")[0] {
			t.Errorf("%s: env = %q, want the stored token delta", c.name, f.Envs)
		}
	}
}

func TestFindChangeRequestFailures(t *testing.T) {
	ctx := context.Background()
	expired := &repoprovider.FakeExec{Errs: map[string]string{"gh pr": "HTTP 401: Bad credentials"}}
	if _, _, err := New(expired, nil).FindChangeRequest(ctx, target, "swarm/phase-7"); !errors.Is(err, repoprovider.ErrNotAuthenticated) {
		t.Errorf("401: %v, want ErrNotAuthenticated", err)
	}
	missing := &repoprovider.FakeExec{Missing: map[string]bool{"gh": true}}
	if _, _, err := New(missing, nil).FindChangeRequest(ctx, target, "swarm/phase-7"); !errors.Is(err, repoprovider.ErrBinaryMissing) {
		t.Errorf("missing gh: %v, want ErrBinaryMissing", err)
	}
	garbage := &repoprovider.FakeExec{Out: map[string]string{"gh pr": "not json"}}
	if _, found, err := New(garbage, nil).FindChangeRequest(ctx, target, "swarm/phase-7"); err == nil || found {
		t.Errorf("garbage: found=%v err=%v, want an error", found, err)
	}
	smuggled := &repoprovider.FakeExec{}
	if _, _, err := New(smuggled, nil).FindChangeRequest(ctx, target, "--repo=evil/x"); !errors.Is(err, ErrInvalidRef) {
		t.Errorf("flag-shaped head: %v, want ErrInvalidRef", err)
	}
	if len(smuggled.Calls) != 0 {
		t.Errorf("a refused head reached gh: %v", smuggled.Calls)
	}
	// No parsed remote: no --repo, gh resolves the repo from the checkout.
	bare := &repoprovider.FakeExec{Out: map[string]string{"gh pr": `[]`}}
	if _, _, err := New(bare, nil).FindChangeRequest(ctx, repoprovider.Target{RepoDir: "/repo"}, "b"); err != nil {
		t.Fatal(err)
	}
	if want := "gh pr list --head b --state all --json number,url,state,mergedAt --limit 20"; bare.Calls[0] != want {
		t.Errorf("bare call = %q, want %q", bare.Calls[0], want)
	}
}
