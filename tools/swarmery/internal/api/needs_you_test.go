package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// needsYouServer plants one blocker of every kind (distinct timestamps, so
// the expected order is unambiguous) plus the rows that must NOT surface:
// a prod deploy with later transcript activity, one older than 24 h, a
// failure older than 24 h, a soft-hidden awaiting session, and a resolved
// ordinary approval. Session 8 lives in an ARCHIVED project.
func needsYouServer(t *testing.T) *httptest.Server {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "needs_you.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	now := time.Now().UTC()
	ago := func(d time.Duration) string { return now.Add(-d).Format(needsYouTSFormat) }
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v\n%s", err, q)
		}
	}

	mustExec(`INSERT INTO projects (id, path, slug, name, first_seen, archived) VALUES
		(1, '/work/alpha', '-work-alpha', 'Alpha', ?, 0),
		(2, '/work/beta',  '-work-beta',  'Beta',  ?, 1)`, ago(72*time.Hour), ago(72*time.Hour))

	insertSession := func(id, project int64, status string, ended time.Duration, title, custom, procState, outcome, focus any, hidden int) {
		t.Helper()
		mustExec(`INSERT INTO sessions (id, project_id, session_uuid, status, started_at, ended_at,
			title, custom_title, proc_state, outcome, term_focus_url, hidden, entrypoint)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'cli')`,
			id, project, "uuid-"+string(rune('a'+id-1))+"-0000-1111", status,
			ago(ended+time.Hour), ago(ended), title, custom, procState, outcome, focus, hidden)
	}
	insertSession(1, 1, "active", 59*time.Minute, "T1", "Custom one", "running", nil, nil, 0)
	insertSession(2, 1, "active", 49*time.Minute, nil, nil, "running", nil, nil, 0)
	insertSession(3, 1, "active", 41*time.Minute, "Deployer", nil, "running", nil, nil, 0)
	insertSession(4, 1, "active", 30*time.Minute, "Moved on", nil, "running", nil, nil, 0)
	insertSession(5, 1, "awaiting_reply", 30*time.Minute, "Asker", nil, "running", nil, "warp://focus/s5", 0)
	insertSession(6, 1, "awaiting_reply", 20*time.Minute, "Crashed waiter", nil, "dead", nil, nil, 0)
	insertSession(7, 1, "completed", 10*time.Minute, "Judged fail", nil, nil, "fail", nil, 0)
	insertSession(8, 2, "active", 4*time.Minute, "Archived blocker", nil, "running", nil, nil, 0)
	insertSession(9, 1, "completed", 30*time.Hour, "Old fail", nil, nil, "fail", nil, 0)
	insertSession(10, 1, "awaiting_reply", 15*time.Minute, "Hidden", nil, "running", nil, nil, 1)
	insertSession(11, 1, "active", 26*time.Hour, "Old deploy", nil, "running", nil, nil, 0)

	bash := func(cmd string) string {
		b, _ := json.Marshal(map[string]any{"tool_name": "Bash", "tool_input": map[string]string{"command": cmd}})
		return string(b)
	}
	ask := `{"tool_name":"AskUserQuestion","tool_input":{"questions":[` +
		`{"question":"Which database?","header":"DB","options":[{"label":"sqlite"},{"label":"pg"}]},` +
		`{"question":"Which port?","header":"Port","options":[{"label":"80"},{"label":"8080"}]}]}}`

	mustExec(`INSERT INTO permission_requests
		(id, session_id, tool_name, request_json, status, requested_at, resolved_at, resolved_via, risk_class) VALUES
		(101, 1, 'Bash', ?, 'pending', ?, NULL, NULL, ''),
		(102, 2, 'AskUserQuestion', ?, 'pending', ?, NULL, NULL, ''),
		(103, 3, 'Bash', ?, 'resolved_elsewhere', ?, ?, 'local-only', 'prod-deploy'),
		(104, 4, 'Bash', ?, 'resolved_elsewhere', ?, ?, 'local-only', 'prod-deploy'),
		(105, 8, 'Bash', ?, 'pending', ?, NULL, NULL, ''),
		(106, 11, 'Bash', ?, 'resolved_elsewhere', ?, ?, 'local-only', 'prod-deploy'),
		(107, 1, 'Bash', ?, 'approved', ?, ?, 'dashboard', '')`,
		bash("ls -la"), ago(60*time.Minute),
		ask, ago(50*time.Minute),
		bash("make deploy-prod"), ago(40*time.Minute), ago(40*time.Minute),
		bash("make deploy-prod"), ago(35*time.Minute), ago(35*time.Minute),
		bash("rm -rf build"), ago(5*time.Minute),
		bash("make deploy-prod"), ago(25*time.Hour), ago(25*time.Hour),
		bash("echo done"), ago(70*time.Minute), ago(69*time.Minute))

	// Session 5: the newest main-thread assistant prose ends on a question; a
	// LATER subagent turn and a later tool-only turn must not shadow it.
	mustExec(`INSERT INTO turns (session_id, seq, role, started_at, text, agent_name, stop_reason) VALUES
		(5, 0, 'user',      ?, 'please do the thing', NULL, NULL),
		(5, 1, 'assistant', ?, 'Old prose.', NULL, 'tool_use'),
		(5, 2, 'assistant', ?, ?, NULL, 'end_turn'),
		(5, 3, 'assistant', ?, 'subagent chatter?', 'worker', 'end_turn'),
		(5, 4, 'assistant', ?, NULL, NULL, 'end_turn')`,
		ago(40*time.Minute), ago(35*time.Minute), ago(31*time.Minute),
		"I migrated the schema.\n\nShould I also drop the old table?  \n\n",
		ago(30*time.Minute), ago(30*time.Minute))

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func getNeedsYou(t *testing.T, url string) needsYouResponse {
	t.Helper()
	var out needsYouResponse
	getJSON(t, url, &out)
	return out
}

func TestNeedsYouOrderAndKinds(t *testing.T) {
	srv := needsYouServer(t)
	resp := getNeedsYou(t, srv.URL+"/api/needs-you")

	if resp.GeneratedAt == "" {
		t.Error("generatedAt is empty")
	}
	type want struct {
		kind    string
		session int64
		request int64 // 0 = none
	}
	wants := []want{
		{needsYouApproval, 1, 101},
		{needsYouQuestion, 2, 102},
		{needsYouProdDeployLocal, 3, 103},
		{needsYouAwaitingReply, 5, 0},
		{needsYouFailed, 6, 0},
		{needsYouFailed, 7, 0},
		{needsYouApproval, 8, 105},
	}
	if len(resp.Items) != len(wants) {
		t.Fatalf("items = %d, want %d: %+v", len(resp.Items), len(wants), resp.Items)
	}
	for i, w := range wants {
		got := resp.Items[i]
		if got.Kind != w.kind || got.SessionID != w.session {
			t.Errorf("item %d = (%s, session %d), want (%s, session %d)", i, got.Kind, got.SessionID, w.kind, w.session)
		}
		switch {
		case w.request == 0 && got.RequestID != nil:
			t.Errorf("item %d requestId = %d, want null", i, *got.RequestID)
		case w.request != 0 && (got.RequestID == nil || *got.RequestID != w.request):
			t.Errorf("item %d requestId = %v, want %d", i, got.RequestID, w.request)
		}
		if got.Suggestion != nil {
			t.Errorf("item %d suggestion = %+v, want null (phase 4)", i, got.Suggestion)
		}
		if got.BlockingSince == "" || got.BlockingSeconds <= 0 {
			t.Errorf("item %d blockingSince=%q blockingSeconds=%d, want set and positive", i, got.BlockingSince, got.BlockingSeconds)
		}
		if i > 0 && got.BlockingSince < resp.Items[i-1].BlockingSince {
			t.Errorf("item %d blockingSince %s is older than item %d's %s", i, got.BlockingSince, i-1, resp.Items[i-1].BlockingSince)
		}
	}

	approval, question, prod, awaiting := resp.Items[0], resp.Items[1], resp.Items[2], resp.Items[3]
	if approval.SessionName != "Custom one" || approval.ProjectSlug != "-work-alpha" {
		t.Errorf("approval name/slug = %q/%q, want custom title and -work-alpha", approval.SessionName, approval.ProjectSlug)
	}
	if approval.ToolName != "Bash" || approval.Preview != "ls -la" {
		t.Errorf("approval tool/preview = %q/%q, want Bash/ls -la", approval.ToolName, approval.Preview)
	}
	if question.Preview != "Which database? (+1 more)" {
		t.Errorf("question preview = %q", question.Preview)
	}
	if question.SessionName != "uuid-b-0" {
		t.Errorf("untitled session name = %q, want the uuid's first 8 chars", question.SessionName)
	}
	if prod.Preview != "make deploy-prod" || prod.SessionName != "Deployer" {
		t.Errorf("prod preview/name = %q/%q", prod.Preview, prod.SessionName)
	}
	if awaiting.Question != "Should I also drop the old table?" || !awaiting.AsksQuestion {
		t.Errorf("awaiting question = %q asks=%v", awaiting.Question, awaiting.AsksQuestion)
	}
	if awaiting.TermFocusURL == nil || *awaiting.TermFocusURL != "warp://focus/s5" {
		t.Errorf("awaiting termFocusUrl = %v, want warp://focus/s5", awaiting.TermFocusURL)
	}
	if approval.TermFocusURL != nil {
		t.Errorf("approval termFocusUrl = %q, want null", *approval.TermFocusURL)
	}
}

func TestNeedsYouJSONShape(t *testing.T) {
	srv := needsYouServer(t)
	res, err := http.Get(srv.URL + "/api/needs-you?project=-work-beta")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	t.Logf("example response: %s", body)
	for _, key := range []string{`"items":`, `"generatedAt":`, `"kind":"approval"`, `"requestId":105`,
		`"suggestion":null`, `"termFocusUrl":null`, `"asksQuestion":false`, `"blockingSeconds":`} {
		if !strings.Contains(string(body), key) {
			t.Errorf("response lacks %s: %s", key, body)
		}
	}
}

func TestNeedsYouProjectScope(t *testing.T) {
	srv := needsYouServer(t)

	beta := getNeedsYou(t, srv.URL+"/api/needs-you?project=-work-beta")
	if len(beta.Items) != 1 || beta.Items[0].SessionID != 8 || beta.Items[0].Kind != needsYouApproval {
		t.Errorf("archived project scope = %+v, want only session 8's pending approval", beta.Items)
	}
	for _, scope := range []string{"alpha", "1"} {
		alpha := getNeedsYou(t, srv.URL+"/api/needs-you?project="+scope)
		if len(alpha.Items) != 6 {
			t.Errorf("project=%s: items = %d, want 6", scope, len(alpha.Items))
		}
		for _, it := range alpha.Items {
			if it.ProjectSlug != "-work-alpha" {
				t.Errorf("project=%s leaked %s item from %s", scope, it.Kind, it.ProjectSlug)
			}
		}
	}
	none := getNeedsYou(t, srv.URL+"/api/needs-you?project=nope")
	if none.Items == nil || len(none.Items) != 0 {
		t.Errorf("unknown project = %+v, want an empty (non-null) list", none.Items)
	}
}

func TestNeedsYouExclusions(t *testing.T) {
	srv := needsYouServer(t)
	resp := getNeedsYou(t, srv.URL+"/api/needs-you")
	seen := map[int64][]string{}
	for _, it := range resp.Items {
		seen[it.SessionID] = append(seen[it.SessionID], it.Kind)
	}
	cases := map[int64]string{
		4:  "prod deploy with later transcript activity",
		9:  "failure older than 24h",
		10: "soft-hidden awaiting session",
		11: "prod deploy older than 24h",
	}
	for id, why := range cases {
		if len(seen[id]) != 0 {
			t.Errorf("session %d (%s) listed as %v", id, why, seen[id])
		}
	}
	if got := seen[6]; len(got) != 1 || got[0] != needsYouFailed {
		t.Errorf("failed+awaiting session 6 = %v, want exactly [failed]", got)
	}
	if got := seen[1]; len(got) != 1 {
		t.Errorf("session 1 = %v, want only the pending approval (resolved row excluded)", got)
	}
}

func TestNeedsYouLastParagraph(t *testing.T) {
	long := strings.Repeat("a", 700) + "?"
	cases := []struct{ name, in, want string }{
		{"empty", "", ""},
		{"no blank line", "line one\nline two?", "line one\nline two?"},
		{"trailing whitespace", "First.\n\nSecond?  \n \n\t\n", "Second?"},
		{"crlf", "First.\r\n\r\nSecond.", "Second."},
		{"whitespace-only separator", "First.\n   \nSecond.", "Second."},
		{"long paragraph", "intro\n\n" + long, long},
	}
	for _, c := range cases {
		if got := lastParagraph(c.in); got != c.want {
			t.Errorf("%s: lastParagraph = %q, want %q", c.name, got, c.want)
		}
	}

	clipped := clipTail(long, needsYouQuestionMax)
	if n := len([]rune(clipped)); n != needsYouQuestionMax {
		t.Errorf(">600 chars: clipped to %d runes, want %d", n, needsYouQuestionMax)
	}
	if !strings.HasPrefix(clipped, "…") || !strings.HasSuffix(clipped, "a?") {
		t.Errorf(">600 chars: clipTail must keep the tail behind an ellipsis, got %q…", clipped[:10])
	}
	if got := clipHead(strings.Repeat("é", 300), needsYouPreviewMax); len([]rune(got)) != needsYouPreviewMax || !strings.HasSuffix(got, "…") {
		t.Errorf("clipHead: %d runes, want %d ending in an ellipsis", len([]rune(got)), needsYouPreviewMax)
	}
	if got := clipHead("short", needsYouPreviewMax); got != "short" {
		t.Errorf("clipHead(short) = %q", got)
	}
}

func TestNeedsYouSortTieBreak(t *testing.T) {
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	id := func(n int64) *int64 { return &n }
	items := []needsYouItem{
		{Kind: needsYouFailed, SessionID: 1, at: at},
		{Kind: needsYouAwaitingReply, SessionID: 2, at: at},
		{Kind: needsYouApproval, SessionID: 3, RequestID: id(9), at: at},
		{Kind: needsYouApproval, SessionID: 4, RequestID: id(7), at: at},
		{Kind: needsYouProdDeployLocal, SessionID: 5, RequestID: id(1), at: at},
		{Kind: needsYouQuestion, SessionID: 6, RequestID: id(2), at: at},
		{Kind: needsYouFailed, SessionID: 7, at: at.Add(-time.Second)},
	}
	sortNeedsYou(items)
	var got []string
	for _, it := range items {
		got = append(got, it.Kind+"#"+strconv.FormatInt(needsYouItemID(it), 10))
	}
	want := "failed#7 approval#7 approval#9 question#2 prod_deploy_local#1 awaiting_reply#2 failed#1"
	if strings.Join(got, " ") != want {
		t.Errorf("order = %s\nwant    %s", strings.Join(got, " "), want)
	}
}

func TestNeedsYouDedupe(t *testing.T) {
	items := dedupeNeedsYou([]needsYouItem{
		{Kind: needsYouAwaitingReply, SessionID: 1},
		{Kind: needsYouFailed, SessionID: 1},
		{Kind: needsYouAwaitingReply, SessionID: 2},
		{Kind: needsYouApproval, SessionID: 1},
	})
	var got []string
	for _, it := range items {
		got = append(got, it.Kind+"#"+strconv.FormatInt(it.SessionID, 10))
	}
	if want := "failed#1 awaiting_reply#2 approval#1"; strings.Join(got, " ") != want {
		t.Errorf("dedupe = %s, want %s", strings.Join(got, " "), want)
	}
}

func TestNeedsYouHelpers(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	if got := blockingSeconds(time.Time{}, now); got != 0 {
		t.Errorf("blockingSeconds(zero) = %d, want 0", got)
	}
	if got := blockingSeconds(now.Add(time.Minute), now); got != 0 {
		t.Errorf("blockingSeconds(future) = %d, want 0", got)
	}
	if got := blockingSeconds(now.Add(-90*time.Second), now); got != 90 {
		t.Errorf("blockingSeconds(-90s) = %d, want 90", got)
	}
	if !parseNeedsYouTS("not a time").IsZero() {
		t.Error("parseNeedsYouTS(garbage) is not zero")
	}
	for _, c := range []struct{ tool, req, want string }{
		{"Bash", `not json`, ""},
		{"Bash", `{"tool_input":{}}`, ""},
		{"Unknown", `{"tool_input":{"x":"y"}}`, ""},
		{"AskUserQuestion", `{"tool_input":{"questions":[]}}`, ""},
		{"AskUserQuestion", `{"tool_input":{"questions":[{"question":"  Only one?  "}]}}`, "Only one?"},
		{"Read", `{"tool_input":{"file_path":"/etc/hosts"}}`, "/etc/hosts"},
	} {
		if got := requestPreview(c.tool, c.req); got != c.want {
			t.Errorf("requestPreview(%s, %s) = %q, want %q", c.tool, c.req, got, c.want)
		}
	}
}
