package accountdoctor

import (
	"bytes"
	"encoding/json"
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
		"PACK_DB_HOST":  "zzq-sentinel-host.example.invalid",
		"PACK_DB_PASS":  `zzq"sentinel\pass`, // JSON-escaped in the JSON form
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
