package accountdoctor

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Redaction may only ever rewrite string VALUES: a store value that is `true`,
// a digit run that also occurs in a number field, the account key itself, or a
// JSON key name must leave the document valid and its structure — and its
// identifier fields — intact, while a real secret disappears everywhere,
// including from inside a path.
func TestRenderJSONRedactsStringValuesOnly(t *testing.T) {
	f := newFixture(t)
	f.loggedInAccount(t, "workacct")
	const secret = "zzqsecretpathpart"
	root := filepath.Join(t.TempDir(), "x-"+secret+"-y")
	mustWrite(t, filepath.Join(root, ".claude", "settings.local.json"),
		`{"swarmery":{"claudeAccount":"workacct","estate":"estate"}}`, 0o644)

	// A number field of the report carries a known digit run: the home
	// profile's byte count.
	profile := `{"projects":{"/a":{}}}` + strings.Repeat(" ", 12345-len(`{"projects":{"/a":{}}}`))
	mustWrite(t, filepath.Join(f.home, ".claude.json"), profile, 0o644)
	digits := strconv.Itoa(len(profile)) // "12345"

	f.anchoredEstate(t, root, "estate",
		"PACK_X=true\nPACK_P="+digits+"\nPACK_K=workacct\nPACK_N=credentials\nPACK_S="+secret+"\n")
	// keep the explicit account binding: anchoredEstate rewrote the file
	mustWrite(t, filepath.Join(root, ".claude", "settings.local.json"),
		`{"swarmery":{"claudeAccount":"workacct","estate":"estate"}}`, 0o644)
	installed := map[string][]installRecord{}
	f.plugin(t, installed, "p@m", `{"mcpServers":{"a":{"env":{"X":"${PACK_X}","S":"${PACK_S}"}}}}`)
	f.writeInstalled(t, installed)
	f.enable(t, map[string]bool{"p@m": true})
	t.Setenv("PACK_X", "")
	t.Setenv("PACK_S", "")

	rep, err := Fast(Options{Path: root})
	if err != nil {
		t.Fatal(err)
	}
	// The default profile is only on a default account; force the number field
	// the digit run lives in onto this report.
	rep.DefaultProfile = &DefaultProfile{Home: ProfileFile{Path: "/h", Exists: true, Bytes: int64(len(profile)), Projects: 1},
		CredentialSources: []string{}}
	rep.VarsExpected = []string{"PACK_S", "PACK_X"} // names — never rewritten
	rep.Findings = append(rep.Findings, Finding{ID: "poison", Severity: SevInfo, Title: "t", Detail: "leak " + secret})

	var js bytes.Buffer
	if err := RenderJSON(&js, rep); err != nil {
		t.Fatal(err)
	}
	out := js.Bytes()
	if !json.Valid(out) {
		t.Fatalf("redaction produced invalid JSON:\n%s", out)
	}
	if bytes.Contains(out, []byte(secret)) {
		t.Errorf("the secret survived: %s", out)
	}
	var back map[string]any
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	if back["account"] != "workacct" || back["estate"] != "estate" || back["source"] != "pin" {
		t.Errorf("identifier fields rewritten: account=%v estate=%v source=%v", back["account"], back["estate"], back["source"])
	}
	if _, ok := back["credentials"].(float64); !ok {
		t.Errorf("credentials is no longer a number: %#v", back["credentials"])
	}
	if b, ok := back["launchedViaSwarmery"].(bool); !ok || b {
		t.Errorf("a boolean was rewritten: %#v", back["launchedViaSwarmery"])
	}
	if got := back["defaultProfile"].(map[string]any)["home"].(map[string]any)["bytes"]; got != float64(len(profile)) {
		t.Errorf("a number field was rewritten: %#v", got)
	}
	if !strings.Contains(string(out), `"varsExpected":["PACK_S","PACK_X"]`) {
		t.Errorf("the variable NAMES were rewritten: %s", out)
	}
	if !strings.Contains(back["path"].(string), redactedMarker) {
		t.Errorf("the secret inside the path was not replaced: %v", back["path"])
	}
}
