package approvals

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestParseRulePattern(t *testing.T) {
	cases := []struct {
		in       string
		tool     string
		inner    string
		hasInner bool
		ok       bool
	}{
		{"Read", "Read", "", false, true},
		{"  Bash(git *)  ", "Bash", "git *", true, true},
		{"Bash(npm run test*)", "Bash", "npm run test*", true, true},
		{"mcp__ide__getDiagnostics", "mcp__ide__getDiagnostics", "", false, true}, // MCP tool names are legal tool_names
		{"WebFetch(https://docs.*/*)", "WebFetch", "https://docs.*/*", true, true},
		{"", "", "", false, false},
		{"*", "", "", false, false},               // wildcard tool part forbidden
		{"Ba*sh(x)", "", "", false, false},        // wildcard inside tool part forbidden
		{"Bash()", "", "", false, false},          // empty inner forbidden
		{"(x)", "", "", false, false},             // missing tool part
		{"AskUserQuestion", "", "", false, false}, // never auto-approvable (E12d)
		{"AskUserQuestion(*)", "", "", false, false},
	}
	for _, c := range cases {
		got, err := ParseRulePattern(c.in)
		if (err == nil) != c.ok {
			t.Errorf("ParseRulePattern(%q) err = %v, want ok=%v", c.in, err, c.ok)
			continue
		}
		if c.ok && (got.Tool != c.tool || got.Inner != c.inner || got.HasInner != c.hasInner) {
			t.Errorf("ParseRulePattern(%q) = %+v", c.in, got)
		}
	}
}

func TestRulePatternMatches(t *testing.T) {
	mustParse := func(s string) RulePattern {
		p, err := ParseRulePattern(s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		return p
	}
	cases := []struct {
		pattern string
		tool    string
		input   string
		want    bool
	}{
		// bare tool: any input
		{"Read", "Read", `{"file_path":"/anything"}`, true},
		{"Read", "Write", `{"file_path":"/x"}`, false},
		// Bash prefix semantics
		{"Bash(git *)", "Bash", `{"command":"git status"}`, true},
		{"Bash(git *)", "Bash", `{"command":"git status && rm -rf /"}`, true}, // documented caveat
		{"Bash(git *)", "Bash", `{"command":"gitk"}`, false},
		{"Bash(git *)", "Bash", `{"command":"git"}`, false}, // needs the space
		{"Bash(git *)", "Bash", `{"command":"sudo git push"}`, false},
		// '*' crosses '/' and spaces (custom glob, NOT path.Match)
		{"Bash(cat *)", "Bash", `{"command":"cat /etc/hosts"}`, true},
		{"Read(/workspace/*)", "Read", `{"file_path":"/workspace/a/b/c.go"}`, true},
		{"Read(/workspace/*)", "Read", `{"file_path":"/etc/passwd"}`, false},
		// exact inner (no '*')
		{"Bash(make test)", "Bash", `{"command":"make test"}`, true},
		{"Bash(make test)", "Bash", `{"command":"make test-e2e"}`, false},
		// middle + suffix segments
		{"WebFetch(https://*.ntfy.sh/*)", "WebFetch", `{"url":"https://docs.ntfy.sh/publish"}`, true},
		{"Bash(git * --force)", "Bash", `{"command":"git push --force"}`, true},
		{"Bash(git * --force)", "Bash", `{"command":"git push"}`, false},
		// deny-by-default: unmapped tool with an inner pattern never matches
		{"Task(deploy*)", "Task", `{"prompt":"deploy prod"}`, false},
		// missing / malformed input never matches an inner pattern
		{"Bash(git *)", "Bash", `{}`, false},
		{"Bash(git *)", "Bash", ``, false},
	}
	for _, c := range cases {
		p := mustParse(c.pattern)
		if got := p.Matches(c.tool, json.RawMessage(c.input)); got != c.want {
			t.Errorf("%q.Matches(%s, %s) = %v, want %v", c.pattern, c.tool, c.input, got, c.want)
		}
	}
}

func seedRule(t *testing.T, db *sql.DB, projectID any, pattern string) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO approval_rules (project_id, tool_pattern, created_at)
		 VALUES (?, ?, '2026-07-16T00:00:00.000Z')`, projectID, pattern)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

// TestOpenAutoApprovesByRule: a matching enabled rule resolves the fresh row
// as approved/resolved_via='rule' — the waiter wakes immediately, the audit
// row and both events exist, and the session never sticks in
// waiting_approval.
func TestOpenAutoApprovesByRule(t *testing.T) {
	db := testDB(t)
	sid := seedSession(t, db, "uuid-rule")
	ruleID := seedRule(t, db, nil, "Bash(git *)")
	svc := New(db, nil, Options{})

	id, ch, isNew, err := svc.Open(hookInput(t, "uuid-rule", "Bash", "git push origin main"))
	if err != nil || !isNew {
		t.Fatalf("Open: id=%d isNew=%v err=%v", id, isNew, err)
	}
	select {
	case d := <-ch:
		if d.Status != StatusApproved {
			t.Fatalf("decision = %+v, want approved", d)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter not woken by rule auto-approve")
	}

	var status, via, reason string
	if err := db.QueryRow(
		`SELECT status, resolved_via, reason FROM permission_requests WHERE id = ?`, id).
		Scan(&status, &via, &reason); err != nil {
		t.Fatal(err)
	}
	if status != StatusApproved || via != "rule" {
		t.Errorf("row = (%s, %s), want (approved, rule)", status, via)
	}
	if want := fmt.Sprintf("auto-approved by rule #%d", ruleID); reason != want {
		t.Errorf("reason = %q, want %q", reason, want)
	}
	// Audit trail: both events exist.
	for _, typ := range []string{"permission_request", "permission_resolved"} {
		var n int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM events WHERE session_id = ? AND type = ?`, sid, typ).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%s events = %d, want 1", typ, n)
		}
	}
	if got := sessionStatus(t, db, sid); got == "waiting_approval" {
		t.Errorf("session stuck in waiting_approval after auto-approve")
	}
}

// TestOpenRuleScopeAndMisses: disabled rules, other-project rules and
// non-matching patterns leave the request pending.
func TestOpenRuleScopeAndMisses(t *testing.T) {
	db := testDB(t)
	seedSession(t, db, "uuid-rule-miss") // project id 1 (/tmp/proj)
	// Another project the rule is scoped to — must NOT apply here.
	if _, err := db.Exec(
		`INSERT INTO projects (path, slug, name, first_seen)
		 VALUES ('/tmp/other', '-tmp-other', 'other', '2026-07-16T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	var otherID int64
	if err := db.QueryRow(`SELECT id FROM projects WHERE path = '/tmp/other'`).Scan(&otherID); err != nil {
		t.Fatal(err)
	}
	seedRule(t, db, otherID, "Bash(git *)") // scoped to the wrong project
	seedRule(t, db, nil, "Bash(npm *)")     // enabled but pattern misses
	disabledID := seedRule(t, db, nil, "Bash(git *)")
	if _, err := db.Exec(`UPDATE approval_rules SET enabled = 0 WHERE id = ?`, disabledID); err != nil {
		t.Fatal(err) // would match, but disabled
	}

	id, ch, _, err := svcOpenPending(t, db, "uuid-rule-miss", "Bash", "git push")
	if err != nil {
		t.Fatal(err)
	}
	if got := requestStatus(t, db, id); got != StatusPending {
		t.Errorf("request status = %s, want pending (no rule may match)", got)
	}
	select {
	case d := <-ch:
		t.Fatalf("unexpected decision %+v for a pending request", d)
	default:
	}
}

// svcOpenPending opens one request on a fresh Service and returns it.
func svcOpenPending(t *testing.T, db *sql.DB, uuid, tool, command string) (int64, chan Decision, bool, error) {
	t.Helper()
	svc := New(db, nil, Options{})
	return svc.Open(hookInput(t, uuid, tool, command))
}

// ── production-deploy class (needs-you-queue phase 2) ────────────────────────

func prodGuardSvc(t *testing.T, db *sql.DB) *Service {
	t.Helper()
	g, err := NewProdGuard(nil)
	if err != nil {
		t.Fatal(err)
	}
	return New(db, nil, Options{ProdGuard: g})
}

type prodRow struct{ status, via, reason, risk string }

func readProdRow(t *testing.T, db *sql.DB, id int64) prodRow {
	t.Helper()
	var r prodRow
	if err := db.QueryRow(
		`SELECT status, COALESCE(resolved_via,''), COALESCE(reason,''), risk_class
		 FROM permission_requests WHERE id = ?`, id).Scan(&r.status, &r.via, &r.reason, &r.risk); err != nil {
		t.Fatal(err)
	}
	return r
}

// TestOpenProdDeployHandsOffToTerminal: a matching request is recorded as
// risk_class='prod-deploy' and resolved_elsewhere via 'local-only' at once —
// the waiter wakes with resolved_elsewhere (the long-poll answers 204).
func TestOpenProdDeployHandsOffToTerminal(t *testing.T) {
	db := testDB(t)
	sid := seedSession(t, db, "uuid-prod")
	svc := prodGuardSvc(t, db)

	id, ch, isNew, err := svc.Open(hookInput(t, "uuid-prod", "Bash", "acme-cli deploy --prod"))
	if err != nil || !isNew {
		t.Fatalf("Open: id=%d isNew=%v err=%v", id, isNew, err)
	}
	select {
	case d := <-ch:
		if d.Status != StatusResolvedElsewhere || d.Reason != LocalOnlyReason {
			t.Fatalf("decision = %+v, want resolved_elsewhere with the local-only reason", d)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter not woken by the prod-deploy hand-off")
	}
	got := readProdRow(t, db, id)
	want := prodRow{StatusResolvedElsewhere, ViaLocalOnly, LocalOnlyReason, RiskProdDeploy}
	if got != want {
		t.Errorf("row = %+v, want %+v", got, want)
	}
	if s := sessionStatus(t, db, sid); s == "waiting_approval" {
		t.Error("session stuck in waiting_approval after the hand-off")
	}

	// An ordinary command on the same service stays an ordinary pending row.
	id2, _, _, err := svc.Open(hookInput(t, "uuid-prod", "Bash", "npm ci --production"))
	if err != nil {
		t.Fatal(err)
	}
	if r := readProdRow(t, db, id2); r.status != StatusPending || r.risk != "" {
		t.Errorf("npm ci --production row = %+v, want pending with no risk class", r)
	}
}

// TestOpenProdDeployIgnoresApproveRules: an enabled `Bash(*)` rule and a
// global bare `Bash` rule exist — the prod-deploy row is STILL not approved.
func TestOpenProdDeployIgnoresApproveRules(t *testing.T) {
	db := testDB(t)
	seedSession(t, db, "uuid-prod-rules")
	seedRule(t, db, nil, "Bash(*)")
	seedRule(t, db, nil, "Bash")
	svc := prodGuardSvc(t, db)

	id, ch, _, err := svc.Open(hookInput(t, "uuid-prod-rules", "Bash", "kubectl --context prod apply -f app.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-ch:
		if d.Status == StatusApproved {
			t.Fatalf("prod-deploy request auto-approved by a rule: %+v", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("prod-deploy request was never handed off (no decision within 5s)")
	}
	if r := readProdRow(t, db, id); r.status == StatusApproved || r.via == "rule" || r.risk != RiskProdDeploy {
		t.Errorf("row = %+v, want an unapproved prod-deploy row", r)
	}

	// Control: the same rules DO approve an ordinary command.
	id2, ch2, _, err := svc.Open(hookInput(t, "uuid-prod-rules", "Bash", "ls -la"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-ch2:
		if d.Status != StatusApproved {
			t.Fatalf("control: ordinary command decision = %+v, want approved by rule", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("control: ordinary command got no decision within 5s")
	}
	if r := readProdRow(t, db, id2); r.via != "rule" {
		t.Errorf("control row = %+v, want resolved via rule", r)
	}
}

// TestDecideProdDeployRefusesApprove: a pending prod-deploy row (seeded
// directly, with a live waiter) refuses approval with ErrLocalOnly; the row
// stays pending and deny still works.
func TestDecideProdDeployRefusesApprove(t *testing.T) {
	db := testDB(t)
	sid := seedSession(t, db, "uuid-prod-decide")
	svc := prodGuardSvc(t, db)
	res, err := db.Exec(
		`INSERT INTO permission_requests (session_id, tool_name, request_json, status, requested_at, expires_at, risk_class)
		 VALUES (?, 'Bash', '{}', 'pending', '2026-10-06T00:00:00.000Z', '2099-01-01T00:00:00.000Z', ?)`,
		sid, RiskProdDeploy)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	svc.mu.Lock()
	ch := svc.attachLocked(id)
	svc.mu.Unlock()

	if err := svc.Decide(id, StatusApproved, "dashboard", ""); !errors.Is(err, ErrLocalOnly) {
		t.Fatalf("Decide(approved) err = %v, want ErrLocalOnly", err)
	}
	if err := svc.Resolve(id, StatusApproved, "rule", ""); !errors.Is(err, ErrLocalOnly) {
		t.Fatalf("Resolve(approved) err = %v, want ErrLocalOnly", err)
	}
	if got := requestStatus(t, db, id); got != StatusPending {
		t.Fatalf("status after refused approvals = %s, want pending", got)
	}
	if err := svc.Decide(id, StatusDenied, "dashboard", "no"); err != nil {
		t.Fatalf("Decide(denied) on a prod-deploy row: %v", err)
	}
	if d := <-ch; d.Status != StatusDenied {
		t.Errorf("decision = %+v, want denied", d)
	}
}
