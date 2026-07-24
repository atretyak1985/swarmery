package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// putPreset issues a PUT /api/projects/1/permission-preset with the given body.
func putPreset(t *testing.T, url, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, url+"/api/projects/1/permission-preset",
		strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func getPresetView(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url + "/api/projects/1/permission-preset")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET preset status = %d: %s", resp.StatusCode, b)
	}
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return out
}

// TestPermissionPresetDefaultFailsClosed: an unset project reads the
// approval-required default with every category 'ask'.
func TestPermissionPresetDefaultFailsClosed(t *testing.T) {
	srv, _ := testServerWithDB(t) // ingests project id 1

	view := getPresetView(t, srv.URL)
	if view["preset"] != "approval-required" {
		t.Fatalf("default preset = %v, want approval-required", view["preset"])
	}
	if view["lockedDown"] != false {
		t.Fatalf("default lockedDown = %v, want false", view["lockedDown"])
	}
	cats, _ := view["categories"].([]any)
	if len(cats) == 0 {
		t.Fatal("categories missing")
	}
	for _, c := range cats {
		m := c.(map[string]any)
		if m["policy"] != "ask" {
			t.Errorf("category %v policy = %v, want ask", m["category"], m["policy"])
		}
	}
}

// TestPermissionPresetEscalation428: switching to unrestricted without confirm
// returns 428 with an escalations payload; with confirm it compiles.
func TestPermissionPresetEscalation428(t *testing.T) {
	srv, db := testServerWithDB(t)

	// No confirm → 428 + escalations.
	resp := putPreset(t, srv.URL, `{"preset":"unrestricted"}`)
	if resp.StatusCode != http.StatusPreconditionRequired {
		t.Fatalf("unrestricted without confirm status = %d, want 428", resp.StatusCode)
	}
	var payload struct {
		Escalations []string `json:"escalations"`
		Reason      string   `json:"reason"`
	}
	json.NewDecoder(resp.Body).Decode(&payload)
	resp.Body.Close()
	if len(payload.Escalations) == 0 {
		t.Fatal("428 payload has no escalations")
	}

	// No rules were written (the 428 must not have side effects).
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM approval_rules WHERE project_id = 1 AND source = 'preset'`).Scan(&count)
	if count != 0 {
		t.Fatalf("428 wrote %d preset rules, want 0 (no side effects)", count)
	}

	// With confirm → 200 and rules compiled; git_push must NOT be auto-approved.
	resp = putPreset(t, srv.URL, `{"preset":"unrestricted","confirm":true}`)
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("unrestricted with confirm status = %d: %s", resp.StatusCode, b)
	}
	resp.Body.Close()

	var pushCount int
	db.QueryRow(`SELECT COUNT(*) FROM approval_rules
		WHERE project_id = 1 AND source = 'preset' AND tool_pattern IN ('Bash(git push*)','Bash(gh *)')`).Scan(&pushCount)
	if pushCount != 0 {
		t.Fatalf("default unrestricted auto-approved git_push (%d rules)", pushCount)
	}
	db.QueryRow(`SELECT COUNT(*) FROM approval_rules WHERE project_id = 1 AND source = 'preset'`).Scan(&count)
	if count == 0 {
		t.Fatal("unrestricted+confirm compiled no rules")
	}
}

// TestPermissionPresetLockedDown: locked-down sets the flag (surfaced by GET)
// and compiles no rules; it does not require confirm.
func TestPermissionPresetLockedDown(t *testing.T) {
	srv, db := testServerWithDB(t)

	resp := putPreset(t, srv.URL, `{"preset":"locked-down"}`)
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("locked-down status = %d: %s", resp.StatusCode, b)
	}
	resp.Body.Close()

	view := getPresetView(t, srv.URL)
	if view["lockedDown"] != true {
		t.Fatalf("lockedDown = %v, want true", view["lockedDown"])
	}
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM approval_rules WHERE project_id = 1 AND source = 'preset'`).Scan(&count)
	if count != 0 {
		t.Fatalf("locked-down compiled %d rules, want 0", count)
	}
}

// TestPermissionPresetPreservesManualRules: setting a preset never touches a
// hand-written manual rule.
func TestPermissionPresetPreservesManualRules(t *testing.T) {
	srv, db := testServerWithDB(t)

	// Create a manual rule via the public API (source='manual').
	resp := postRule(t, srv.URL, `{"projectId":1,"toolPattern":"Bash(terraform plan*)"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create manual rule = %d", resp.StatusCode)
	}
	var created map[string]any
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if created["source"] != "manual" {
		t.Fatalf("manual rule source = %v, want manual", created["source"])
	}
	manualID := int64(created["id"].(float64))

	// Compile unrestricted then approval-required.
	for _, b := range []string{`{"preset":"unrestricted","confirm":true}`, `{"preset":"approval-required"}`} {
		r := putPreset(t, srv.URL, b)
		if r.StatusCode != http.StatusOK {
			t.Fatalf("PUT %s = %d", b, r.StatusCode)
		}
		r.Body.Close()
	}

	// The manual rule still exists with source='manual'.
	var pattern, source string
	if err := db.QueryRow(`SELECT tool_pattern, source FROM approval_rules WHERE id = ?`, manualID).
		Scan(&pattern, &source); err != nil {
		t.Fatalf("manual rule gone: %v", err)
	}
	if source != "manual" || pattern != "Bash(terraform plan*)" {
		t.Fatalf("manual rule mutated: %q/%q", pattern, source)
	}

	// And a managed rule cannot be deleted through the manual DELETE surface.
	r := putPreset(t, srv.URL, `{"preset":"unrestricted","confirm":true}`)
	r.Body.Close()
	var presetRuleID int64
	db.QueryRow(`SELECT id FROM approval_rules WHERE project_id = 1 AND source = 'preset' LIMIT 1`).Scan(&presetRuleID)
	if presetRuleID != 0 {
		req, _ := http.NewRequest(http.MethodDelete,
			srv.URL+"/api/approval-rules/"+jsonNum(float64(presetRuleID)), nil)
		del, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if del.StatusCode != http.StatusConflict {
			t.Errorf("DELETE managed rule = %d, want 409", del.StatusCode)
		}
		del.Body.Close()
	}
}

// TestPermissionPresetRoundTrip: GET reflects a value set via PUT (overrides
// survive), and an unrestricted view marks command_exec allow / git_push ask.
func TestPermissionPresetRoundTrip(t *testing.T) {
	srv, _ := testServerWithDB(t)

	// Set unrestricted with git_push explicitly allowed (needs confirm).
	resp := putPreset(t, srv.URL,
		`{"preset":"unrestricted","confirm":true,"overrides":{"git_push":"allow"}}`)
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("PUT status = %d: %s", resp.StatusCode, b)
	}
	resp.Body.Close()

	view := getPresetView(t, srv.URL)
	if view["preset"] != "unrestricted" {
		t.Fatalf("preset = %v, want unrestricted", view["preset"])
	}
	ov, _ := view["overrides"].(map[string]any)
	if ov["git_push"] != "allow" {
		t.Fatalf("overrides.git_push = %v, want allow", ov["git_push"])
	}
	policyOf := map[string]string{}
	for _, c := range view["categories"].([]any) {
		m := c.(map[string]any)
		policyOf[m["category"].(string)] = m["policy"].(string)
	}
	if policyOf["command_exec"] != "allow" {
		t.Errorf("command_exec policy = %q, want allow", policyOf["command_exec"])
	}
	if policyOf["git_push"] != "allow" {
		t.Errorf("git_push policy = %q, want allow (explicitly overridden)", policyOf["git_push"])
	}
}

// TestPermissionPresetGetUnknownProject: GET on a phantom project → 404.
func TestPermissionPresetGetUnknownProject(t *testing.T) {
	srv, _ := testServerWithDB(t)
	resp, err := http.Get(srv.URL + "/api/projects/9999/permission-preset")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET unknown project = %d, want 404", resp.StatusCode)
	}
}

// TestPermissionPresetBadInput: unknown preset / project / override → 4xx.
func TestPermissionPresetBadInput(t *testing.T) {
	srv, _ := testServerWithDB(t)

	resp := putPreset(t, srv.URL, `{"preset":"wide-open"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown preset = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()

	resp = putPreset(t, srv.URL, `{"preset":"unrestricted","confirm":true,"overrides":{"bogus":"allow"}}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad override = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// Unknown project id.
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/projects/9999/permission-preset",
		strings.NewReader(`{"preset":"locked-down"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown project = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
}
