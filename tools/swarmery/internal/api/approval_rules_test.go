package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func postRule(t *testing.T, url, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(url+"/api/approval-rules", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestApprovalRulesCRUD(t *testing.T) {
	srv, _ := testServerWithDB(t) // fixture ingests one project (id 1)

	// Create: global rule.
	resp := postRule(t, srv.URL, `{"toolPattern":"Bash(git *)","note":"trusted git"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	var created map[string]any
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if created["toolPattern"] != "Bash(git *)" || created["projectId"] != nil ||
		created["enabled"] != true || created["action"] != "approve" {
		t.Fatalf("created = %v", created)
	}
	id := int64(created["id"].(float64))

	// Create: project-scoped rule.
	resp = postRule(t, srv.URL, `{"projectId":1,"toolPattern":"Read"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("project rule status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Validation failures → 400.
	for _, bad := range []string{
		`{"toolPattern":"*"}`,                    // wildcard tool part
		`{"toolPattern":"AskUserQuestion"}`,      // never auto-approvable
		`{"toolPattern":""}`,                     // empty
		`{"projectId":999,"toolPattern":"Read"}`, // unknown project
		`not json`,
	} {
		resp := postRule(t, srv.URL, bad)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("POST %s → %d, want 400", bad, resp.StatusCode)
		}
		resp.Body.Close()
	}

	// List: newest first, projectSlug joined.
	var list []map[string]any
	getJSON(t, srv.URL+"/api/approval-rules", &list)
	if len(list) != 2 || list[0]["projectSlug"] == nil {
		t.Fatalf("list = %v", list)
	}

	// PATCH toggle off.
	req, _ := http.NewRequest(http.MethodPatch,
		fmt.Sprintf("%s/api/approval-rules/%d", srv.URL, id),
		bytes.NewReader([]byte(`{"enabled":false}`)))
	req.Header.Set("Content-Type", "application/json")
	tresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var toggled map[string]any
	json.NewDecoder(tresp.Body).Decode(&toggled)
	tresp.Body.Close()
	if tresp.StatusCode != http.StatusOK || toggled["enabled"] != false {
		t.Fatalf("toggle = %d %v", tresp.StatusCode, toggled)
	}

	// DELETE → 204, second DELETE → 404.
	for i, want := range []int{http.StatusNoContent, http.StatusNotFound} {
		req, _ := http.NewRequest(http.MethodDelete,
			fmt.Sprintf("%s/api/approval-rules/%d", srv.URL, id), nil)
		dresp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		dresp.Body.Close()
		if dresp.StatusCode != want {
			t.Errorf("delete #%d = %d, want %d", i+1, dresp.StatusCode, want)
		}
	}

	// PATCH unknown id → 404.
	req, _ = http.NewRequest(http.MethodPatch, srv.URL+"/api/approval-rules/424242",
		bytes.NewReader([]byte(`{"enabled":true}`)))
	presp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	presp.Body.Close()
	if presp.StatusCode != http.StatusNotFound {
		t.Errorf("patch unknown = %d, want 404", presp.StatusCode)
	}
}
