package approvals

import (
	"database/sql"
	"encoding/json"
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
