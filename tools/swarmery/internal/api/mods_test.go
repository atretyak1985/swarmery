package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/modscan"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/sysscan"
)

// modsServer builds a temp claude tree with one cache mod, scans it with the
// fake claude script (never the real CLI), attaches the scanner to the rescan
// endpoint, and returns the server plus the fake's invocation log.
func modsServer(t *testing.T) (srv *httptest.Server, logPath string) {
	t.Helper()
	claude := t.TempDir()
	mod := filepath.Join(claude, "plugins", "cache", "mkt", "linkmod", "1.0.0")
	writeFile(t, filepath.Join(mod, ".claude-plugin", "plugin.json"), `{"name":"linkmod","version":"1.0.0"}`)
	writeFile(t, filepath.Join(mod, "hooks", "hooks.json"), `{"modules":["./register.tsx"]}`)
	writeFile(t, filepath.Join(mod, "hooks", "register.tsx"), "export default 'SECRET-MODULE-SOURCE'\n")

	bin, err := filepath.Abs("../modscan/testdata/fake-claude")
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := filepath.Abs("../../testdata/fixtures/mods/validate-json.json")
	if err != nil {
		t.Fatal(err)
	}
	logPath = filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("FAKE_CLAUDE_MODE", "ok")
	t.Setenv("FAKE_CLAUDE_FIXTURE", fixture)
	t.Setenv("FAKE_CLAUDE_LOG", logPath)

	db, err := store.Open(filepath.Join(t.TempDir(), "mods.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	scanner := sysscan.New(db, sysscan.Config{ClaudeDir: claude, ClaudeBin: bin, RescanInterval: time.Hour}, nil)
	// The registry Scan never runs the mods pass (it must stay cheap for
	// sysedit); the daemon's Run loop does, as this explicit pass does here.
	if _, err := scanner.ScanMods(context.Background(), false); err != nil {
		t.Fatalf("mods pass: %v", err)
	}
	AttachModRescanner(scanner)
	t.Cleanup(func() { AttachModRescanner(nil) })

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv = httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, logPath
}

func validateCalls(t *testing.T, logPath string) int {
	t.Helper()
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(raw), "\n")
}

func postRescan(t *testing.T, url, origin string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url+"/api/system/mods/rescan", nil)
	if err != nil {
		t.Fatal(err)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestSystemModsListAndDetail(t *testing.T) {
	srv, _ := modsServer(t)

	var list []modDTO
	getJSON(t, srv.URL+"/api/system/mods", &list)
	if len(list) != 1 {
		t.Fatalf("list = %+v, want one mod", list)
	}
	m := list[0]
	if m.Name != "linkmod" || m.Source != modscan.SourceCache || m.PluginID == nil || *m.PluginID != "linkmod@mkt" ||
		m.Version == nil || *m.Version != "1.0.0" || m.ModuleSHA == nil || m.ValidatedAt == nil ||
		!strings.HasSuffix(m.Dir, filepath.Join("linkmod", "1.0.0")) {
		t.Errorf("mod = %+v", m)
	}
	if len(m.Events) == 0 || len(m.Uses) == 0 || len(m.Env.Reads) != 1 || len(m.Errors) != 0 {
		t.Errorf("events=%v uses=%v env=%+v errors=%v", m.Events, m.Uses, m.Env, m.Errors)
	}
	if len(m.Risk) == 0 || m.Risk[0].Level != modscan.LevelRed || m.Risk[0].Label != "can approve tool calls" {
		t.Errorf("risk = %+v", m.Risk)
	}

	resp, err := http.Get(srv.URL + "/api/system/mods/" + jsonInt(m.ID))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("detail: %d %s", resp.StatusCode, raw)
	}
	var d modDetailDTO
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if d.ID != m.ID || d.Dir != m.Dir || !d.Report.Validated || !d.Report.Success ||
		len(d.Report.GatingHooks) != 4 || len(d.Report.Raw) == 0 {
		t.Errorf("detail = %+v", d)
	}
	if strings.Contains(string(raw), "SECRET-MODULE-SOURCE") {
		t.Error("detail leaked module source; it must show the folder path only")
	}
}

func jsonInt(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestSystemModNotFound(t *testing.T) {
	srv, _ := modsServer(t)
	for path, want := range map[string]int{
		"/api/system/mods/9999": http.StatusNotFound,
		"/api/system/mods/abc":  http.StatusBadRequest,
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("GET %s = %d, want %d", path, resp.StatusCode, want)
		}
	}
}

func TestSystemModsRescanOrigin(t *testing.T) {
	srv, logPath := modsServer(t)
	before := validateCalls(t, logPath)

	if code, body := postRescan(t, srv.URL, "https://evil.example"); code != http.StatusForbidden {
		t.Errorf("rescan with a foreign Origin = %d %s, want 403", code, body)
	}
	if validateCalls(t, logPath) != before {
		t.Error("a rejected rescan still ran the validator")
	}

	code, body := postRescan(t, srv.URL, srv.URL)
	if code != http.StatusOK {
		t.Fatalf("rescan with the daemon's own Origin = %d %s, want 200", code, body)
	}
	var st sysscan.ModStats
	if err := json.Unmarshal([]byte(body), &st); err != nil || st.Mods != 1 || st.Validated != 1 {
		t.Errorf("rescan stats = %s (%v), want 1 mod forced through validate", body, err)
	}
	if validateCalls(t, logPath) != before+1 {
		t.Errorf("validate calls = %d, want %d (forced re-validate)", validateCalls(t, logPath), before+1)
	}

	AttachModRescanner(nil)
	if code, _ := postRescan(t, srv.URL, ""); code != http.StatusServiceUnavailable {
		t.Errorf("rescan without a scanner = %d, want 503", code)
	}
}
