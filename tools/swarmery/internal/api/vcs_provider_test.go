package api

// Tests for PUT /api/projects/{id}/vcs/provider (vcs_provider.go) and the
// askProvider flag GET /api/projects/{id}/vcs raises for an unclassified host.
// The vcs endpoint's exec, prober and env are the vcs_test.go fakes; the
// project path is a temp dir, so the settings file written is the test's own.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider"
)

// putVcsProvider sends the PUT for project id and returns the status.
func putVcsProvider(t *testing.T, srv string, id int64, body string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut,
		fmt.Sprintf("%s/api/projects/%d/vcs/provider", srv, id), bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// selfHostedVcsFixture is project 1 on a temp dir whose origin is a self-hosted
// host the (stubbed) GitLab probe does not recognise, with an unrelated
// settings.local.json key that the answer must preserve.
func selfHostedVcsFixture(t *testing.T) (*vcsFixture, string) {
	t.Helper()
	fake := &repoprovider.FakeExec{Out: map[string]string{
		"git remote": "https://git.example.com/acme/widgets.git\n",
		"glab api":   `{"username":"tanuki"}`,
	}}
	f, exec := newVcsFixture(t, fake)
	project := t.TempDir()
	exec(`UPDATE projects SET path = ? WHERE id = 1`, project)
	writeClaudeFile(t, project, "settings.local.json", `{"swarmery":{"estate":{"keep":true}},"other":1}`)
	return f, project
}

func TestPutProjectVcsProviderPersistsAndGetReportsIt(t *testing.T) {
	f, project := selfHostedVcsFixture(t)

	before, raw := f.get(t)
	if before.Provider != repoprovider.KindUnknown || !before.AskProvider || raw["askProvider"] != true {
		t.Fatalf("before the answer: provider/askProvider = %q/%v, want unknown/true", before.Provider, raw["askProvider"])
	}

	if got := putVcsProvider(t, f.srv, 1, `{"provider":"gitlab"}`); got != http.StatusNoContent {
		t.Fatalf("PUT status = %d, want 204", got)
	}

	data, err := os.ReadFile(filepath.Join(project, ".claude", "settings.local.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Swarmery struct {
			Vcs    map[string]any `json:"vcs"`
			Estate map[string]any `json:"estate"`
		} `json:"swarmery"`
		Other float64 `json:"other"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("settings.local.json: %v (%s)", err, data)
	}
	if settings.Swarmery.Vcs["provider"] != "gitlab" {
		t.Errorf("swarmery.vcs.provider = %v, want gitlab", settings.Swarmery.Vcs["provider"])
	}
	if settings.Swarmery.Estate["keep"] != true || settings.Other != 1 {
		t.Errorf("unrelated keys lost: %s", data)
	}

	// No ?fresh: the PUT itself dropped the cached unknown answer.
	after, raw := f.get(t)
	if after.Provider != repoprovider.KindGitLab || after.Source != repoprovider.SourceConfig {
		t.Errorf("after the answer: provider/source = %q/%q, want gitlab/config", after.Provider, after.Source)
	}
	if after.Terms.Change != "Merge Request" || after.Terms.ChangeShort != "MR" || after.Terms.Provider != "GitLab" {
		t.Errorf("terms = %+v, want GitLab/Merge Request/MR", after.Terms)
	}
	if after.AskProvider || raw["askProvider"] != false {
		t.Errorf("askProvider = %v after the answer, want false", raw["askProvider"])
	}
	if after.CliLogin != "glab auth login --hostname git.example.com" {
		t.Errorf("cliLogin = %q", after.CliLogin)
	}
	if after.Auth.Status != repoprovider.AuthOK || after.Auth.Login != "tanuki" {
		t.Errorf("auth = %+v, want ok as tanuki through glab", after.Auth)
	}
}

func TestPutProjectVcsProviderRejectsOtherValues(t *testing.T) {
	f, project := selfHostedVcsFixture(t)
	for _, body := range []string{`{"provider":"bitbucket"}`, `{"provider":"unknown"}`, `{"provider":""}`, `{}`, `not json`} {
		if got := putVcsProvider(t, f.srv, 1, body); got != http.StatusBadRequest {
			t.Errorf("PUT %s: status = %d, want 400", body, got)
		}
	}
	data, err := os.ReadFile(filepath.Join(project, ".claude", "settings.local.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(`"vcs"`)) {
		t.Errorf("a rejected answer was written: %s", data)
	}
}

func TestPutProjectVcsProviderUnknownProjectIs404(t *testing.T) {
	f, _ := selfHostedVcsFixture(t)
	if got := putVcsProvider(t, f.srv, 999, `{"provider":"github"}`); got != http.StatusNotFound {
		t.Errorf("status = %d, want 404", got)
	}
}

func TestPutProjectVcsProviderRefusesNonObjectSettings(t *testing.T) {
	f, project := selfHostedVcsFixture(t)
	writeClaudeFile(t, project, "settings.local.json", `["not","an","object"]`)
	if got := putVcsProvider(t, f.srv, 1, `{"provider":"github"}`); got != http.StatusConflict {
		t.Errorf("status = %d, want 409", got)
	}
}

// A host with no remote has nothing to ask about: askProvider stays false.
func TestProjectVcsAskProviderOnlyWithRemote(t *testing.T) {
	fake := &repoprovider.FakeExec{Errs: map[string]string{
		"git remote": "error: No such remote 'origin'",
	}}
	f, _ := newVcsFixture(t, fake)
	if dto, _ := f.get(t); dto.AskProvider {
		t.Error("askProvider = true without a remote")
	}
}
