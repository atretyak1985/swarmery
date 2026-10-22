package approvals

// Stored approval requests hold no secrets (security-audit phase 13,
// responsible disclosure: the approvals path wrote the raw hook stdin into the
// store while the transcript ingest already masked it). Only the STORED copies
// are masked — permission_requests.request_json and the permission_request
// event payload. The dedup hash, the auto-approve rules, the prod-deploy guard
// and every decision sent back to the hook keep working on the raw input.
//
// Every secret-shaped value is built at run time (internal/redact/redacttest),
// so no source line holds one.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/redact"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/redact/redacttest"
)

// sample returns the redacttest sample of one rule class.
func sample(t *testing.T, class string) redacttest.Sample {
	t.Helper()
	for _, s := range redacttest.Samples() {
		if s.Class == class {
			return s
		}
	}
	t.Fatalf("no %s sample", class)
	return redacttest.Sample{}
}

// storedCopies returns the request_json of row id and the payload of its
// permission_request event.
func storedCopies(t *testing.T, db *sql.DB, id int64) (requestJSON, eventPayload string) {
	t.Helper()
	if err := db.QueryRow(
		`SELECT pr.request_json, e.payload FROM permission_requests pr
		 JOIN events e ON e.id = pr.event_id WHERE pr.id = ?`, id).Scan(&requestJSON, &eventPayload); err != nil {
		t.Fatal(err)
	}
	return requestJSON, eventPayload
}

// editHookInput is an Edit request whose new_string is code: an assignment to
// a variable named token, followed by literal token shapes.
func editHookInput(t *testing.T, uuid, newString string) HookInput {
	t.Helper()
	toolInput, err := json.Marshal(map[string]string{
		"file_path": "/tmp/proj/main.go", "old_string": "x", "new_string": newString,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := fmt.Sprintf(
		`{"session_id":%q,"cwd":"/tmp/proj","hook_event_name":"PermissionRequest","tool_name":"Edit","tool_input":%s}`,
		uuid, toolInput)
	in, err := ParseHookStdin([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return in
}

// A Bash request carrying every secret class is stored masked: no sample is
// left anywhere in the store, and the stored copies stay valid JSON with the
// session, tool and mark in place.
func TestStoredPermissionRequestHoldsNoSecrets(t *testing.T) {
	db := testDB(t)
	seedSession(t, db, "uuid-redact-bash")
	svc := New(db, nil, Options{})

	id, _, isNew, err := svc.Open(hookInput(t, "uuid-redact-bash", "Bash", "deploy\n"+redacttest.Joined()))
	if err != nil || !isNew {
		t.Fatalf("Open: id=%d isNew=%v err=%v", id, isNew, err)
	}
	reqJSON, payload := storedCopies(t, db, id)
	for name, doc := range map[string]string{"request_json": reqJSON, "event payload": payload} {
		for _, s := range redacttest.Samples() {
			if strings.Contains(doc, s.Needle) {
				t.Errorf("%s holds a %s value", name, s.Class)
			}
		}
		var p struct {
			SessionID string `json:"session_id"`
			ToolName  string `json:"tool_name"`
			ToolInput struct {
				Command string `json:"command"`
			} `json:"tool_input"`
		}
		if err := json.Unmarshal([]byte(doc), &p); err != nil {
			t.Fatalf("%s is no longer JSON: %v", name, err)
		}
		if p.SessionID != "uuid-redact-bash" || p.ToolName != "Bash" || !strings.Contains(p.ToolInput.Command, redact.Mark) {
			t.Errorf("%s = %+v, want the session, the tool and a masked command", name, p)
		}
	}
	leaks, err := redacttest.Leaks(db, redacttest.Samples())
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range leaks {
		t.Error(l)
	}
}

// Edit content is code (D6): the literal token shapes are masked, an
// assignment to a variable named token is kept as written.
func TestStoredEditRequestKeepsCode(t *testing.T) {
	db := testDB(t)
	seedSession(t, db, "uuid-redact-edit")
	svc := New(db, nil, Options{})

	id, _, _, err := svc.Open(editHookInput(t, "uuid-redact-edit", redacttest.SourceAssignment()+redacttest.LiteralJoined()))
	if err != nil {
		t.Fatal(err)
	}
	reqJSON, payload := storedCopies(t, db, id)
	for name, doc := range map[string]string{"request_json": reqJSON, "event payload": payload} {
		var p struct {
			ToolInput struct {
				NewString string `json:"new_string"`
			} `json:"tool_input"`
		}
		if err := json.Unmarshal([]byte(doc), &p); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(p.ToolInput.NewString, redacttest.SourceAssignment()) {
			t.Errorf("%s lost the source line: %q", name, p.ToolInput.NewString)
		}
		for _, s := range redacttest.Samples() {
			if s.Class == "bearer" || s.Class == "generic_key_value" {
				continue
			}
			if strings.Contains(doc, s.Needle) {
				t.Errorf("%s holds a %s value", name, s.Class)
			}
		}
	}
}

// A request without a secret is stored byte for byte as the hook sent it.
func TestStoredCleanRequestIsVerbatim(t *testing.T) {
	db := testDB(t)
	seedSession(t, db, "uuid-redact-clean")
	svc := New(db, nil, Options{})
	in := hookInput(t, "uuid-redact-clean", "Bash", "go test ./...")
	id, _, _, err := svc.Open(in)
	if err != nil {
		t.Fatal(err)
	}
	reqJSON, payload := storedCopies(t, db, id)
	if reqJSON != string(in.Raw) || payload != string(in.Raw) {
		t.Errorf("clean request rewritten:\n stored %s\n event  %s\n hook   %s", reqJSON, payload, in.Raw)
	}
}

// The dedup hash is taken over the RAW tool_input: two requests that differ
// only in the secret are two rows (as before masking), although their stored
// copies are now identical; an identical request still attaches.
func TestDedupHashUsesRawInput(t *testing.T) {
	db := testDB(t)
	seedSession(t, db, "uuid-redact-dedup")
	svc := New(db, nil, Options{})
	gh := sample(t, "github_token").Value
	other := gh[:len(gh)-4] + "ZZZZ"

	inA := hookInput(t, "uuid-redact-dedup", "Bash", "gh auth login --with-token "+gh)
	inB := hookInput(t, "uuid-redact-dedup", "Bash", "gh auth login --with-token "+other)
	idA, _, newA, err := svc.Open(inA)
	if err != nil {
		t.Fatal(err)
	}
	idB, _, newB, err := svc.Open(inB)
	if err != nil {
		t.Fatal(err)
	}
	if !newA || !newB || idA == idB {
		t.Fatalf("requests differing only in the secret: ids %d/%d new %v/%v, want two new rows", idA, idB, newA, newB)
	}
	reqA, _ := storedCopies(t, db, idA)
	reqB, _ := storedCopies(t, db, idB)
	if reqA != reqB {
		t.Errorf("stored copies differ, want both masked to the same text:\n%s\n%s", reqA, reqB)
	}
	for _, c := range []struct {
		id int64
		in HookInput
	}{{idA, inA}, {idB, inB}} {
		want, err := DedupHash(c.in.SessionUUID, c.in.ToolName, c.in.ToolInput)
		if err != nil {
			t.Fatal(err)
		}
		var got string
		if err := db.QueryRow(`SELECT dedup_hash FROM permission_requests WHERE id = ?`, c.id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("row %d dedup_hash = %s, want the hash of the raw input %s", c.id, got, want)
		}
	}
	idA2, _, newA2, err := svc.Open(inA)
	if err != nil {
		t.Fatal(err)
	}
	if newA2 || idA2 != idA {
		t.Errorf("identical request: id %d new %v, want an attach to %d", idA2, newA2, idA)
	}
}

// A saved rule whose glob names the secret matches the raw command — so rule
// matching cannot have moved after the masking.
func TestRuleMatchesRawCommand(t *testing.T) {
	db := testDB(t)
	seedSession(t, db, "uuid-redact-rule")
	url := sample(t, "url_userinfo").Value
	ruleID := seedRule(t, db, nil, "Bash(git push "+url+"*)")
	svc := New(db, nil, Options{})

	id, ch, _, err := svc.Open(hookInput(t, "uuid-redact-rule", "Bash", "git push "+url+" main"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-ch:
		if d.Status != StatusApproved || d.Via != ViaRule || d.Reason != fmt.Sprintf("auto-approved by rule #%d", ruleID) {
			t.Fatalf("decision = %+v, want approved by rule %d", d, ruleID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the rule naming the secret did not approve the raw command")
	}
	if reqJSON, _ := storedCopies(t, db, id); strings.Contains(reqJSON, sample(t, "url_userinfo").Needle) {
		t.Error("the auto-approved row stored the secret")
	}
}

// A prod-deploy guard extra that names the secret classifies the raw command.
func TestProdGuardMatchesRawCommand(t *testing.T) {
	db := testDB(t)
	seedSession(t, db, "uuid-redact-prod")
	url := sample(t, "url_userinfo").Value
	g, err := NewProdGuard([]string{"Bash(*" + url + "*)"})
	if err != nil {
		t.Fatal(err)
	}
	svc := New(db, nil, Options{ProdGuard: g})
	id, ch, _, err := svc.Open(hookInput(t, "uuid-redact-prod", "Bash", "release --to "+url))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-ch:
		if d.Status != StatusResolvedElsewhere || d.Via != ViaLocalOnly {
			t.Fatalf("decision = %+v, want the prod-deploy hand-off", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the guard naming the secret did not classify the raw command")
	}
	if r := readProdRow(t, db, id); r.risk != RiskProdDeploy {
		t.Errorf("risk_class = %q, want %q", r.risk, RiskProdDeploy)
	}
}

// Approve and deny on a masked row still answer the waiting hook, the deny
// reason verbatim.
func TestDecisionOnMaskedRowReachesHook(t *testing.T) {
	db := testDB(t)
	seedSession(t, db, "uuid-redact-decide")
	svc := New(db, nil, Options{})
	gh := sample(t, "github_token").Value

	for _, c := range []struct{ status, reason, cmd string }{
		{StatusApproved, "", "curl -H 'X-Auth: " + gh + "' https://example.com/a"},
		{StatusDenied, "not with that credential", "curl -H 'X-Auth: " + gh + "' https://example.com/b"},
	} {
		id, ch, _, err := svc.Open(hookInput(t, "uuid-redact-decide", "Bash", c.cmd))
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.Decide(id, c.status, "dashboard", c.reason); err != nil {
			t.Fatalf("Decide %s: %v", c.status, err)
		}
		select {
		case d := <-ch:
			if d.Status != c.status || d.Reason != c.reason {
				t.Errorf("hook got %+v, want %s %q", d, c.status, c.reason)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s never reached the hook", c.status)
		}
	}
}

// An AskUserQuestion whose question quotes a secret: the stored copy is
// masked, the dashboard answers by the question text it was shown (the stored
// one), and the hook still receives the questions it asked, verbatim, with
// the answers keyed by them.
func TestAnswerOnMaskedQuestionDeliversRawQuestions(t *testing.T) {
	db := testDB(t)
	seedSession(t, db, "uuid-redact-answer")
	svc := New(db, nil, Options{})
	gh := sample(t, "github_token").Value
	rawQuestion := "Rotate the token " + gh + " now?"
	questions, err := json.Marshal([]map[string]any{
		{"question": rawQuestion, "header": "Rotate", "multiSelect": false,
			"options": []map[string]string{{"label": "Yes", "description": ""}, {"label": "No", "description": ""}}},
		{"question": "Pick fruits", "header": "Fruits", "multiSelect": true,
			"options": []map[string]string{{"label": "Apple", "description": ""}, {"label": "Banana", "description": ""}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	in, err := ParseHookStdin([]byte(fmt.Sprintf(
		`{"session_id":"uuid-redact-answer","cwd":"/tmp/proj","hook_event_name":"PermissionRequest","tool_name":"AskUserQuestion","tool_input":{"questions":%s}}`,
		questions)))
	if err != nil {
		t.Fatal(err)
	}
	id, ch, _, err := svc.Open(in)
	if err != nil {
		t.Fatal(err)
	}

	// What the dashboard reads and answers by.
	reqJSON, _ := storedCopies(t, db, id)
	_, shown, err := parseQuestions(reqJSON)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(reqJSON, gh) || strings.Contains(shown[0].Question, gh) {
		t.Fatal("the stored question holds the secret")
	}
	answers := map[string]json.RawMessage{
		shown[0].Question: json.RawMessage(`"Yes"`),
		shown[1].Question: json.RawMessage(`["Apple"]`),
	}
	if err := svc.Answer(id, answers); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	select {
	case d := <-ch:
		var ui struct {
			Questions json.RawMessage            `json:"questions"`
			Answers   map[string]json.RawMessage `json:"answers"`
		}
		if err := json.Unmarshal(d.UpdatedInput, &ui); err != nil {
			t.Fatal(err)
		}
		if string(ui.Questions) != string(questions) {
			t.Errorf("questions not verbatim:\n got %s\nwant %s", ui.Questions, questions)
		}
		if string(ui.Answers[rawQuestion]) != `"Yes"` || string(ui.Answers["Pick fruits"]) != `["Apple"]` || len(ui.Answers) != 2 {
			t.Errorf("answers = %v, want them keyed by the questions the hook asked", ui.Answers)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the answer never reached the hook")
	}
	var reason string
	if err := db.QueryRow(`SELECT reason FROM permission_requests WHERE id = ?`, id).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(reason, gh) {
		t.Error("the stored answer summary holds the secret")
	}
}
