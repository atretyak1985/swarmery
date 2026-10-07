package api

// memory-engineering phase 1: GET /api/projects/{id}/memory/lint.

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/memlint"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/memlint/memlinttest"
)

func getLint(t *testing.T, fx *memoryFixture, id string) (*http.Response, memlint.Report) {
	t.Helper()
	res, err := http.Get(fx.srv.URL + "/api/projects/" + id + "/memory/lint")
	if err != nil {
		t.Fatalf("GET lint: %v", err)
	}
	defer res.Body.Close()
	var rep memlint.Report
	if res.StatusCode == http.StatusOK {
		if err := json.NewDecoder(res.Body).Decode(&rep); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return res, rep
}

func TestMemoryLintReportsStaleClaims(t *testing.T) {
	// The project path IS a git repo carrying the #366 merge and #224 squash
	// commits; the fixture lines live in its auto-memory root.
	repo := memlinttest.Repo(t)
	fx := newMemoryFixtureIn(t, false, repo)
	memlinttest.WriteLines(t, fx.autoDir)

	res, rep := getLint(t, fx, "1")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if rep.Dir != fx.autoDir || rep.Project != repo {
		t.Fatalf("report dir/project = %q/%q, want %q/%q", rep.Dir, rep.Project, fx.autoDir, repo)
	}
	if rep.Files != 5 || rep.Claims != 4 || len(rep.Findings) != 2 {
		t.Fatalf("report = files %d claims %d findings %d, want 5/4/2", rep.Files, rep.Claims, len(rep.Findings))
	}
	prs := map[int]bool{}
	for _, f := range rep.Findings {
		prs[f.PR] = true
		if f.MergeSHA == "" || f.MergedAt == "" || f.Claim == "" || f.File == "" || f.LineNo == 0 {
			t.Fatalf("finding missing fields: %+v", f)
		}
	}
	if !prs[366] || !prs[224] {
		t.Fatalf("findings = %+v, want #366 and #224", rep.Findings)
	}
}

func TestMemoryLintEmptyWhenNoMemoryDir(t *testing.T) {
	fx := newMemoryFixture(t, false) // no auto-memory root on disk
	res, rep := getLint(t, fx, "1")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a project with no memory", res.StatusCode)
	}
	if rep.Files != 0 || rep.Claims != 0 || rep.Findings == nil || len(rep.Findings) != 0 {
		t.Fatalf("report = %+v, want an empty report with findings []", rep)
	}
	if rep.Dir != fx.autoDir {
		t.Fatalf("report dir = %q, want %q", rep.Dir, fx.autoDir)
	}
}

func TestMemoryLintUnknownProjectIs404(t *testing.T) {
	fx := newMemoryFixture(t, false)
	res, _ := getLint(t, fx, "9999")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", res.StatusCode)
	}
}
