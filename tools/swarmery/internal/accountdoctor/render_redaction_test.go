package accountdoctor

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// Criterion 8: a fixture store with distinctive sentinel VALUES; a full report
// rendered as text and as JSON carries every NAME and no VALUE. The report is
// deliberately POISONED — a finding whose detail quotes each value, the way a
// careless future arm might — so this fails the moment redact() is bypassed.
func TestRenderRedactsEveryStoreValue(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	values := map[string]string{
		"PACK_DB_HOST":   "zzq-sentinel-host.example.invalid",
		"PACK_DB_PASS":   `zzq"sentinel\pass`, // JSON-escaped in the JSON form
		"PACK_API_BASIC": "zzq-sentinel-basic-token",
	}
	var body strings.Builder
	for n, v := range values {
		body.WriteString(n + "=" + v + "\n")
	}
	f.anchoredEstate(t, root, "estate", body.String())
	mcp := `{"mcpServers":{"db":{"env":{"H":"${PACK_DB_HOST}","P":"${PACK_DB_PASS}","B":"${PACK_API_BASIC}","R":"${PACK_ENV_ONLY}"}}}}`
	installed := map[string][]installRecord{}
	f.plugin(t, installed, "db@m", mcp)
	f.writeInstalled(t, installed)
	f.enable(t, map[string]bool{"db@m": true})
	for n, v := range values {
		t.Setenv(n, v)
	}
	t.Setenv("PACK_ENV_ONLY", "zzq-sentinel-from-the-environment")

	rep, err := Fast(Options{Path: root})
	if err != nil {
		t.Fatal(err)
	}
	all := []string{"zzq-sentinel-from-the-environment"}
	for _, v := range values {
		all = append(all, v)
	}
	rep.Findings = append(rep.Findings, Finding{ID: "poison", Severity: SevInfo, Title: "poison",
		Detail: strings.Join(all, " | "), File: values["PACK_DB_HOST"]})

	var js, txt bytes.Buffer
	if err := RenderJSON(&js, rep); err != nil {
		t.Fatal(err)
	}
	if err := RenderText(&txt, rep); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(js.Bytes()) {
		t.Fatalf("redaction broke the JSON: %s", js.String())
	}
	for _, out := range []string{js.String(), txt.String()} {
		for _, v := range all {
			if strings.Contains(out, v) {
				t.Errorf("value %q survived rendering", v)
			}
		}
		// The JSON-escaped spelling must not survive either.
		if strings.Contains(out, `zzq\"sentinel`) {
			t.Error("the escaped spelling of a value survived")
		}
		for n := range values {
			if !strings.Contains(out, n) {
				t.Errorf("name %s missing from a rendering", n)
			}
		}
		if !strings.Contains(out, redactedMarker) {
			t.Error("nothing was redacted — the poison must have been")
		}
	}
}

// A credential VALUE that coincides with a location the doctor resolved — the
// estate root itself, or one component of it such as the operator's user name —
// is not a leak: the path is the session's own working tree. Every location,
// and every path under one of its ancestors, keeps its spelling in both forms,
// so the preflight hook still reads estateRoot. The same value OUTSIDE a
// location is still redacted, and a value that spills past one is redacted
// whole.
func TestRenderKeepsLocationsAValueCoincidesWith(t *testing.T) {
	f := newFixture(t)
	owner := "zzqowner"
	root := filepath.Join(t.TempDir(), owner, "estate")
	fresh := filepath.Join(root, "fresh")
	mustMkdir(t, fresh, 0o755)
	spill := "pg://zzq-user:zzq-pass@" + root
	f.anchoredEstate(t, root, "estate", "PACK_OWNER="+owner+"\nPACK_DSN="+spill+"\n")
	installed := map[string][]installRecord{}
	f.plugin(t, installed, "fs@m", `{"mcpServers":{"fs":{"env":{"R":"${PACK_FS_ROOT}"}}}}`)
	f.writeInstalled(t, installed)
	f.enable(t, map[string]bool{"fs@m": true})
	t.Setenv("PACK_FS_ROOT", root)

	rep, err := Fast(Options{Path: fresh})
	if err != nil {
		t.Fatal(err)
	}
	ancestorFile := filepath.Join(filepath.Dir(root), ".claude", "settings.local.json")
	rep.Findings = append(rep.Findings, Finding{ID: "poison", Severity: SevInfo, Title: "poison",
		Detail: "owner " + owner + " | dsn " + spill + " | parent " + ancestorFile})

	var js, txt bytes.Buffer
	if err := RenderJSON(&js, rep); err != nil {
		t.Fatal(err)
	}
	if err := RenderText(&txt, rep); err != nil {
		t.Fatal(err)
	}
	var got Report
	if err := json.Unmarshal(js.Bytes(), &got); err != nil {
		t.Fatalf("redaction broke the JSON: %v\n%s", err, js.String())
	}
	if got.Path != fresh || got.EstateRoot != root {
		t.Errorf("path = %q, estateRoot = %q; want %q and %q", got.Path, got.EstateRoot, fresh, root)
	}
	for name, out := range map[string]string{"json": js.String(), "text": txt.String()} {
		for _, loc := range []string{fresh, root, ancestorFile} {
			if !strings.Contains(out, loc) {
				t.Errorf("%s: location %q lost its spelling:\n%s", name, loc, out)
			}
		}
		if strings.Contains(out, "owner "+owner) {
			t.Errorf("%s: the value outside every location survived", name)
		}
		if strings.Contains(out, "zzq-user:zzq-pass") {
			t.Errorf("%s: a value that spills past a location survived", name)
		}
	}
}
