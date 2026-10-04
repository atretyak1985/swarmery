package ingest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeprobe"
)

// defaultFailureTranscript is failureTranscript but rooted at the stock
// ~/.claude/projects path, so it resolves to DefaultAccount instead of "work".
func defaultFailureTranscript(t *testing.T, session, text, ts string, flagged bool) (string, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".claude", "projects")
	dir := filepath.Join(root, "-p")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	quoted, err := json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	flag := ""
	if flagged {
		flag = `"isApiErrorMessage":true,`
	}
	lines := []string{
		`{"type":"user","uuid":"u-` + session + `","timestamp":"` + ts + `","sessionId":"` + session + `","cwd":"/p","message":{"role":"user","content":"hello"}}`,
		`{"type":"assistant","uuid":"a-` + session + `",` + flag + `"timestamp":"` + ts + `","sessionId":"` + session + `","cwd":"/p","message":{"id":"msg-` + session + `","model":"<synthetic>","role":"assistant","content":[{"type":"text","text":` + string(quoted) + `}]}}`,
	}
	f := filepath.Join(dir, session+".jsonl")
	if err := os.WriteFile(f, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return f, root
}

// TestDefaultAccountAuthTripProbesWithEmptyConfigDir pins the fix for the
// false-trip bug: a default-account transcript's deferred auth trip must
// probe with configDir == "" (CLAUDE_CONFIG_DIR absent), not the spelled-out
// ~/.claude path — claudeprobe.Probe's contract selects the default account
// by that absence, and a non-empty ~/.claude dir makes the probe itself
// report the (correctly logged-in) default account as logged out.
func TestDefaultAccountAuthTripProbesWithEmptyConfigDir(t *testing.T) {
	db := testDB(t)
	breakerAt(t, breakerNoon)
	var gotDir string
	sawDir := false
	prev := breakerProbe
	breakerProbe = func(_ context.Context, dir string) claudeprobe.Result {
		gotDir, sawDir = dir, true
		return claudeprobe.Result{Status: claudeprobe.StatusNoLogin, Reason: claudeprobe.ReasonNoLogin}
	}
	t.Cleanup(func() { breakerProbe = prev })

	f, root := defaultFailureTranscript(t, "sess-default-probe", orgDisabledText, "2026-09-30T11:55:00.000Z", true)
	if _, err := fileFrom(db, f, root); err != nil {
		t.Fatal(err)
	}
	if !sawDir {
		t.Fatal("breakerProbe was never called")
	}
	if gotDir != "" {
		t.Errorf("probe configDir = %q, want \"\" — the default account must be probed by CLAUDE_CONFIG_DIR's absence, not ~/.claude spelled out", gotDir)
	}
}
