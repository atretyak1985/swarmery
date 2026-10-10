package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// PATCH /api/epics/{taskId}/docs {line, class} — the Criteria tab's "Mark
// [LAND] / [MANUAL]" button (phase-run outcomes plan, phase 3, D3).

func patchDoc(t *testing.T, url, body string) (int, planDocResponse) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPatch, url, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out planDocResponse
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode, out
}

func TestPatchPlanDocMarksCriterionClass(t *testing.T) {
	srv, _, taskID, planDir := epicFixture(t)
	url := srv.URL + "/api/epics/" + itoa(taskID) + "/docs?path=phase-1-schema.md"
	doc := filepath.Join(planDir, "phase-1-schema.md")
	read := func() string {
		t.Helper()
		b, err := os.ReadFile(doc)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	orig := read()

	// Line 6 is "- [ ] b" (see TestPlanDocPatch). The class is case-insensitive.
	status, resp := patchDoc(t, url, `{"line":6,"class":"land"}`)
	if status != http.StatusOK {
		t.Fatalf("mark LAND = %d", status)
	}
	if got := read(); !strings.Contains(got, "\n- [ ] [LAND] b\n") || resp.Content != got {
		t.Fatalf("doc after mark:\n%s\nresponse content matches: %v", got, resp.Content == got)
	}
	if resp.Backup == "" {
		t.Fatal("a changing mark wrote no backup")
	}
	if b, err := os.ReadFile(resp.Backup); err != nil || string(b) != orig {
		t.Errorf("backup = %q (%v), want the pre-mark doc", b, err)
	}

	// The same class again changes nothing and takes no backup.
	status, resp = patchDoc(t, url, `{"line":6,"class":"LAND"}`)
	if status != http.StatusOK || resp.Backup != "" || !strings.Contains(resp.Content, "- [ ] [LAND] b") {
		t.Fatalf("idempotent mark = %d backup=%q", status, resp.Backup)
	}

	// Another class replaces the marker rather than stacking a second one.
	if status, _ = patchDoc(t, url, `{"line":6,"class":"MANUAL"}`); status != http.StatusOK {
		t.Fatalf("mark MANUAL = %d", status)
	}
	if got := read(); !strings.Contains(got, "\n- [ ] [MANUAL] b\n") || strings.Contains(got, "[LAND]") {
		t.Fatalf("doc after re-mark:\n%s", got)
	}
}

func TestPatchPlanDocClassErrors(t *testing.T) {
	srv, _, taskID, planDir := epicFixture(t)
	fenced := "# Phase\n\n```md\n- [ ] an example\n```\n- [ ] real\n"
	if err := os.WriteFile(filepath.Join(planDir, "fenced.md"), []byte(fenced), 0o644); err != nil {
		t.Fatal(err)
	}
	base := srv.URL + "/api/epics/" + itoa(taskID) + "/docs?path="
	cases := []struct {
		name, path, body string
		want             int
	}{
		{"unknown class", "phase-1-schema.md", `{"line":6,"class":"SHIP"}`, http.StatusBadRequest},
		{"empty class", "phase-1-schema.md", `{"line":6,"class":""}`, http.StatusBadRequest},
		{"done and class", "phase-1-schema.md", `{"line":6,"done":true,"class":"LAND"}`, http.StatusBadRequest},
		{"class without line", "phase-1-schema.md", `{"class":"LAND"}`, http.StatusBadRequest},
		{"not a checkbox", "phase-1-schema.md", `{"line":0,"class":"LAND"}`, http.StatusUnprocessableEntity},
		{"out of range", "phase-1-schema.md", `{"line":9999,"class":"LAND"}`, http.StatusUnprocessableEntity},
		{"negative line", "phase-1-schema.md", `{"line":-1,"class":"LAND"}`, http.StatusUnprocessableEntity},
		{"checkbox inside a fence", "fenced.md", `{"line":3,"class":"LAND"}`, http.StatusUnprocessableEntity},
		{"missing doc", "nope.md", `{"line":1,"class":"LAND"}`, http.StatusNotFound},
		// Confinement strips the `..`: the path resolves inside plan/, where no
		// such doc exists.
		{"path escape", "../../secret.md", `{"line":1,"class":"LAND"}`, http.StatusNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got, _ := patchDoc(t, base+c.path, c.body); got != c.want {
				t.Errorf("%s = %d, want %d", c.name, got, c.want)
			}
		})
	}
	// A refused mark leaves the doc and the backup dir alone.
	if b, _ := os.ReadFile(filepath.Join(planDir, "fenced.md")); string(b) != fenced {
		t.Errorf("fenced doc changed: %q", b)
	}
	if _, err := os.Stat(filepath.Join(planDir, ".backups")); !os.IsNotExist(err) {
		t.Errorf("a refused mark wrote a backup dir (%v)", err)
	}
	// The real criterion of the fenced doc (line 5) is markable.
	if got, _ := patchDoc(t, base+"fenced.md", `{"line":5,"class":"MANUAL"}`); got != http.StatusOK {
		t.Errorf("unfenced criterion = %d", got)
	}
}
