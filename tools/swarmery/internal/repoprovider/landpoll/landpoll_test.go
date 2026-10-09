package landpoll

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider/providers"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// Every test drives a repoprovider.FakeExec behind the real github/gitlab
// providers: no gh, glab, git or network. The store is a migrated temp SQLite.

const (
	githubRemote = "https://github.com/acme/widgets.git"
	gitlabRemote = "https://gitlab.example.com/group/widgets.git"
	ghToken      = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
)

var t0 = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

// prJSON is a `gh pr view --json` body.
func prJSON(state, mergedAt, review string, checks ...string) string {
	return fmt.Sprintf(`{"state":%q,"isDraft":false,"mergedAt":%q,"reviewDecision":%q,"statusCheckRollup":[%s],"url":"u"}`,
		state, mergedAt, review, strings.Join(checks, ","))
}

const (
	runOK      = `{"__typename":"CheckRun","status":"COMPLETED","conclusion":"SUCCESS"}`
	runPending = `{"__typename":"CheckRun","status":"IN_PROGRESS","conclusion":""}`
)

// reply is one scripted gh/glab answer.
type reply struct {
	out, stderr string
}

// harness is a poller over a temp store and a scripted exec.
type harness struct {
	t         *testing.T
	db        *sql.DB
	projectID int64
	taskID    int64
	dir       string
	fake      *repoprovider.FakeExec
	poller    *Poller

	mu        sync.Mutex
	replies   map[string]reply // PR number → reply
	remote    string
	published []int64
	expired   []int64
	logs      []string
	now       time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "landpoll.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	h := &harness{t: t, db: db, dir: t.TempDir(), replies: map[string]reply{}, remote: githubRemote, now: t0}
	h.exec(`INSERT INTO projects(id, path, slug, first_seen) VALUES(7, ?, 'p', '2026-01-01T00:00:00Z')`, h.dir)
	h.projectID = 7
	res := h.exec(`INSERT INTO tasks (project_id, title, prompt, status, created_at, started_at, source, external_id)
		VALUES (7, 'Epic', 'goal', 'running', '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z', 'workspace', '2026-10-01-epic')`)
	h.taskID, _ = res.LastInsertId()

	h.fake = &repoprovider.FakeExec{Fn: func(_ string, _ []string, name string, args []string) (string, string, error, bool) {
		h.mu.Lock()
		defer h.mu.Unlock()
		switch {
		case name == "git" && len(args) >= 2 && args[0] == "remote":
			if h.remote == "" {
				return "", "fatal: No such remote 'origin'", errors.New("exit status 2"), true
			}
			return h.remote + "\n", "", nil, true
		case (name == "gh" || name == "glab") && len(args) >= 3 && args[1] == "view":
			r, ok := h.replies[args[2]]
			if !ok {
				return "", "no such PR " + args[2], errors.New("exit status 1"), true
			}
			if r.stderr != "" {
				return "", r.stderr, errors.New("exit status 1"), true
			}
			return r.out, "", nil, true
		}
		return "", "", nil, false
	}}
	h.poller = &Poller{
		DB: db,
		Factory: func(k repoprovider.Kind) (repoprovider.Provider, error) {
			return providers.Factory(k, h.fake, nil)
		},
		Exec:          h.fake,
		Clock:         func() time.Time { h.mu.Lock(); defer h.mu.Unlock(); return h.now },
		Publish:       func(id int64) { h.mu.Lock(); h.published = append(h.published, id); h.mu.Unlock() },
		OnAuthExpired: func(id int64) { h.mu.Lock(); h.expired = append(h.expired, id); h.mu.Unlock() },
		Logf: func(format string, args ...any) {
			h.mu.Lock()
			h.logs = append(h.logs, fmt.Sprintf(format, args...))
			h.mu.Unlock()
		},
	}
	return h
}

func (h *harness) exec(q string, args ...any) sql.Result {
	h.t.Helper()
	res, err := h.db.Exec(q, args...)
	if err != nil {
		h.t.Fatalf("exec %q: %v", q, err)
	}
	return res
}

// phase inserts a phase in landing_state state with change request number n
// (0 ⇒ no number) on provider, last checked at checkedAt ("" ⇒ never).
func (h *harness) phase(seq int, state, provider string, n int, checkedAt string) int64 {
	h.t.Helper()
	var num, url, chk any
	if n > 0 {
		num = n
		url = fmt.Sprintf("https://github.com/acme/widgets/pull/%d", n)
	}
	if checkedAt != "" {
		chk = checkedAt
	}
	res := h.exec(`INSERT INTO epic_phases
		(workspace_task_id, seq, name, doc_path, depends_on, checkboxes_total, checkboxes_done,
		 landing_state, pr_provider, pr_number, pr_url, pr_checked_at, landed_at)
		VALUES (?, ?, ?, ?, '[]', 1, 0, ?, ?, ?, ?, ?, '2026-10-08T00:00:00Z')`,
		h.taskID, seq, fmt.Sprintf("Phase %d", seq), fmt.Sprintf("/plan/phase-%d.md", seq),
		state, provider, num, url, chk)
	id, _ := res.LastInsertId()
	return id
}

func (h *harness) reply(n int, r reply) {
	h.mu.Lock()
	h.replies[fmt.Sprint(n)] = r
	h.mu.Unlock()
}

// landing is a phase's landing columns as stored.
type landing struct {
	state                                     string
	status, checkedAt, landedAt, landingError sql.NullString
}

func (h *harness) landing(id int64) landing {
	h.t.Helper()
	var l landing
	if err := h.db.QueryRow(`SELECT landing_state, pr_status, pr_checked_at, landed_at, landing_error
		FROM epic_phases WHERE id = ?`, id).Scan(&l.state, &l.status, &l.checkedAt, &l.landedAt, &l.landingError); err != nil {
		h.t.Fatal(err)
	}
	return l
}

func (h *harness) runOnce() (int, int) {
	h.t.Helper()
	checked, changed, err := h.poller.RunOnce(context.Background())
	if err != nil {
		h.t.Fatalf("RunOnce: %v", err)
	}
	return checked, changed
}

func (h *harness) publishedCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.published)
}

func (h *harness) viewCalls() []string {
	var out []string
	for _, c := range h.fake.Calls {
		if strings.HasPrefix(c, "gh pr view") || strings.HasPrefix(c, "glab mr view") {
			out = append(out, c)
		}
	}
	return out
}

func TestRunOnceStillOpenUpdatesStatusOnly(t *testing.T) {
	h := newHarness(t)
	id := h.phase(1, StatePROpen, "github", 11, "")
	h.reply(11, reply{out: prJSON("OPEN", "", "REVIEW_REQUIRED", runPending)})

	checked, changed := h.runOnce()
	if checked != 1 || changed != 1 {
		t.Fatalf("checked=%d changed=%d, want 1/1", checked, changed)
	}
	l := h.landing(id)
	if l.state != StatePROpen {
		t.Errorf("landing_state = %q, want pr_open", l.state)
	}
	want := `{"state":"open","draft":false,"ci":"pending","review":"review_required","checkedAt":"2026-10-09T12:00:00Z"}`
	if l.status.String != want {
		t.Errorf("pr_status = %s\nwant      %s", l.status.String, want)
	}
	if l.checkedAt.String != "2026-10-09T12:00:00Z" || l.landedAt.String != "2026-10-08T00:00:00Z" || l.landingError.Valid {
		t.Errorf("columns = %+v", l)
	}
	if got := h.published; len(got) != 1 || got[0] != h.taskID {
		t.Fatalf("published = %v, want [%d]", got, h.taskID)
	}
	if calls := h.viewCalls(); len(calls) != 1 || !strings.HasPrefix(calls[0], "gh pr view 11 --repo acme/widgets --json") {
		t.Fatalf("calls = %v", calls)
	}

	// The same answer again moves pr_checked_at but publishes nothing.
	h.now = t0.Add(10 * time.Minute)
	if _, changed := h.runOnce(); changed != 0 {
		t.Errorf("unchanged re-read: changed = %d", changed)
	}
	if h.publishedCount() != 1 {
		t.Errorf("unchanged re-read published: %v", h.published)
	}
	if got := h.landing(id).checkedAt.String; got != "2026-10-09T12:10:00Z" {
		t.Errorf("pr_checked_at after re-read = %q", got)
	}

	// CI turning green is a change.
	h.reply(11, reply{out: prJSON("OPEN", "", "APPROVED", runOK)})
	if _, changed := h.runOnce(); changed != 1 || h.publishedCount() != 2 {
		t.Errorf("CI change: changed=%d published=%v", changed, h.published)
	}
	if got := h.landing(id).status.String; !strings.Contains(got, `"ci":"success"`) || !strings.Contains(got, `"review":"approved"`) {
		t.Errorf("pr_status after CI change = %s", got)
	}
}

func TestRunOnceMergedFlipsStateAndPublishesOnce(t *testing.T) {
	h := newHarness(t)
	id := h.phase(1, StatePROpen, "github", 12, "2026-10-09T11:00:00Z")
	h.reply(12, reply{out: prJSON("MERGED", "2026-10-09T11:30:00Z", "APPROVED", runOK)})

	if checked, changed := h.runOnce(); checked != 1 || changed != 1 {
		t.Fatalf("checked=%d changed=%d", checked, changed)
	}
	l := h.landing(id)
	if l.state != StateMerged || l.landedAt.String != "2026-10-09T12:00:00Z" {
		t.Fatalf("after merge: state=%q landed_at=%q", l.state, l.landedAt.String)
	}
	if !strings.Contains(l.status.String, `"state":"merged"`) {
		t.Errorf("pr_status = %s", l.status.String)
	}
	if h.publishedCount() != 1 {
		t.Fatalf("published = %v, want exactly one", h.published)
	}
	// A merged phase is no longer polled.
	if checked, _ := h.runOnce(); checked != 0 || h.publishedCount() != 1 {
		t.Fatalf("merged phase re-polled: checked=%d published=%v", checked, h.published)
	}
}

func TestRunOnceClosedStaysOpenWithClosedStatus(t *testing.T) {
	h := newHarness(t)
	id := h.phase(1, StatePROpen, "github", 13, "")
	h.reply(13, reply{out: prJSON("CLOSED", "", "", runOK)})

	h.runOnce()
	l := h.landing(id)
	if l.state != StatePROpen {
		t.Errorf("closed PR moved landing_state to %q", l.state)
	}
	if !strings.Contains(l.status.String, `"state":"closed"`) {
		t.Errorf("pr_status = %s", l.status.String)
	}
	if l.landedAt.String != "2026-10-08T00:00:00Z" {
		t.Errorf("landed_at touched: %q", l.landedAt.String)
	}
}

func TestRunOnceProviderErrorIsIsolated(t *testing.T) {
	h := newHarness(t)
	a := h.phase(1, StatePROpen, "github", 21, "2026-10-09T08:00:00Z")
	b := h.phase(2, StatePROpen, "github", 22, "2026-10-09T09:00:00Z")
	c := h.phase(3, StatePROpen, "github", 23, "2026-10-09T10:00:00Z")
	h.reply(21, reply{out: prJSON("OPEN", "", "", runOK)})
	h.reply(22, reply{stderr: "HTTP 502: upstream broke while using " + ghToken})
	h.reply(23, reply{out: prJSON("MERGED", "x", "", runOK)})

	checked, changed := h.runOnce()
	if checked != 3 || changed != 3 {
		t.Fatalf("checked=%d changed=%d, want 3/3", checked, changed)
	}
	if l := h.landing(a); !strings.Contains(l.status.String, `"ci":"success"`) || l.landingError.Valid {
		t.Errorf("phase a: %+v", l)
	}
	lb := h.landing(b)
	if !strings.HasPrefix(lb.landingError.String, CodeStatusFailed+": ") {
		t.Errorf("phase b landing_error = %q", lb.landingError.String)
	}
	if strings.Contains(lb.landingError.String, ghToken) || !strings.Contains(lb.landingError.String, "***") {
		t.Errorf("token not redacted in landing_error: %q", lb.landingError.String)
	}
	if lb.status.Valid || lb.state != StatePROpen || lb.checkedAt.String != "2026-10-09T12:00:00Z" {
		t.Errorf("phase b columns: %+v", lb)
	}
	if l := h.landing(c); l.state != StateMerged {
		t.Errorf("phase c after a failing sibling: %q", l.state)
	}
	if len(h.logs) != 1 || strings.Contains(h.logs[0], ghToken) || !strings.Contains(h.logs[0], fmt.Sprintf("phase %d", b)) {
		t.Errorf("logs = %q", h.logs)
	}
	if len(h.expired) != 0 {
		t.Errorf("a 502 marked auth expired: %v", h.expired)
	}

	// The same failure again is not re-published; a recovery clears the error.
	h.now = t0.Add(time.Minute)
	if _, changed := h.runOnce(); changed != 0 {
		t.Errorf("repeat failure: changed = %d", changed)
	}
	h.reply(22, reply{out: prJSON("OPEN", "", "", runOK)})
	h.now = t0.Add(2 * time.Minute)
	if _, changed := h.runOnce(); changed != 1 {
		t.Errorf("recovery: changed = %d", changed)
	}
	if l := h.landing(b); l.landingError.Valid {
		t.Errorf("landing_error survived a good read: %q", l.landingError.String)
	}
}

func TestRunOnceUnauthorizedMarksAuthExpired(t *testing.T) {
	h := newHarness(t)
	id := h.phase(1, StatePROpen, "github", 31, "")
	h.reply(31, reply{stderr: "HTTP 401: Bad credentials (https://api.github.com/graphql)"})

	if checked, changed := h.runOnce(); checked != 1 || changed != 1 {
		t.Fatalf("checked=%d changed=%d", checked, changed)
	}
	if len(h.expired) != 1 || h.expired[0] != h.projectID {
		t.Fatalf("OnAuthExpired calls = %v, want [%d]", h.expired, h.projectID)
	}
	if got := h.landing(id).landingError.String; !strings.HasPrefix(got, "not-authenticated: ") {
		t.Errorf("landing_error = %q", got)
	}
}

func TestRunOnceOrdersByCheckedAtAndCapsAtMax(t *testing.T) {
	h := newHarness(t)
	h.phase(1, StatePROpen, "github", 41, "2026-10-09T10:00:00Z") // newest
	h.phase(2, StatePROpen, "github", 42, "")                     // never checked
	h.phase(3, StatePROpen, "github", 43, "2026-10-09T09:00:00Z") // older
	h.phase(4, "pushed", "github", 44, "")                        // not a change request
	h.phase(5, StateMerged, "github", 45, "")                     // already landed
	for n := 41; n <= 45; n++ {
		h.reply(n, reply{out: prJSON("OPEN", "", "", runOK)})
	}
	h.poller.Max = 2

	if checked, _ := h.runOnce(); checked != 2 {
		t.Fatalf("checked = %d, want Max=2", checked)
	}
	calls := h.viewCalls()
	if len(calls) != 2 || !strings.HasPrefix(calls[0], "gh pr view 42 ") || !strings.HasPrefix(calls[1], "gh pr view 43 ") {
		t.Fatalf("order = %v, want 42 (never) then 43 (oldest)", calls)
	}
	// Next tick: 41 is now the oldest.
	h.now = t0.Add(time.Minute)
	h.runOnce()
	if calls := h.viewCalls(); !strings.HasPrefix(calls[2], "gh pr view 41 ") {
		t.Fatalf("second tick = %v", calls[2:])
	}
}

func TestRunOnceDefaultMax(t *testing.T) {
	h := newHarness(t)
	for i := 1; i <= DefaultMax+5; i++ {
		h.phase(i, StatePROpen, "github", 100+i, "")
		h.reply(100+i, reply{out: prJSON("OPEN", "", "", runOK)})
	}
	if checked, changed := h.runOnce(); checked != DefaultMax || changed != DefaultMax {
		t.Fatalf("checked=%d changed=%d, want %d", checked, changed, DefaultMax)
	}
}

func TestRunOnceGitLabUsesStoredProviderAndRepoDirHook(t *testing.T) {
	h := newHarness(t)
	h.remote = gitlabRemote // a self-hosted host Detect cannot classify without a probe
	id := h.phase(1, StatePROpen, "gitlab", 51, "")
	h.reply(51, reply{out: `{"state":"opened","draft":true,"head_pipeline":{"status":"failed"},"approved":false}`})
	sub := filepath.Join(h.dir, "svc")
	h.poller.RepoDir = func(phaseID int64) (string, error) {
		if phaseID != id {
			t.Errorf("RepoDir(%d), want %d", phaseID, id)
		}
		return sub, nil
	}

	h.runOnce()
	l := h.landing(id)
	want := `{"state":"open","draft":true,"ci":"failure","review":"review_required","checkedAt":"2026-10-09T12:00:00Z"}`
	if l.status.String != want {
		t.Errorf("pr_status = %s\nwant      %s", l.status.String, want)
	}
	calls := h.viewCalls()
	if len(calls) != 1 || calls[0] != "glab mr view 51 -R gitlab.example.com/group/widgets --output json" {
		t.Errorf("calls = %v", calls)
	}
	for i, c := range h.fake.Calls {
		if h.fake.Dirs[i] != sub {
			t.Errorf("%q ran in %q, want the RepoDir %q", c, h.fake.Dirs[i], sub)
		}
	}
}

func TestRunOnceResolutionFailuresAreStamped(t *testing.T) {
	cases := []struct {
		name     string
		setup    func(h *harness)
		provider string
		n        int
		code     string
	}{
		{"no origin", func(h *harness) { h.remote = "" }, "github", 61, CodeNoRemote},
		{"unknown host, no stored provider", func(h *harness) { h.remote = "https://git.example.org/a/b.git" }, "", 62, CodeProviderUnknown},
		{"repo dir unresolvable", func(h *harness) {
			h.poller.RepoDir = func(int64) (string, error) { return "", errors.New("phase repo is outside the project") }
		}, "github", 63, CodeStatusFailed},
		{"gh missing", func(h *harness) { h.fake.Missing = map[string]bool{"gh": true} }, "github", 64, CodeBinaryMissing},
		{"no number and no url", func(h *harness) {}, "github", 0, CodeStatusFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			c.setup(h)
			id := h.phase(1, StatePROpen, c.provider, c.n, "")
			if checked, _ := h.runOnce(); checked != 1 {
				t.Fatalf("checked = %d", checked)
			}
			if got := h.landing(id).landingError.String; !strings.HasPrefix(got, c.code+": ") {
				t.Errorf("landing_error = %q, want prefix %q", got, c.code)
			}
		})
	}
}

func TestRunOnceUnreadableStoredStatusCountsAsChanged(t *testing.T) {
	h := newHarness(t)
	id := h.phase(1, StatePROpen, "github", 66, "")
	h.exec(`UPDATE epic_phases SET pr_status = 'not json' WHERE id = ?`, id)
	h.reply(66, reply{out: prJSON("OPEN", "", "", runOK)})
	if _, changed := h.runOnce(); changed != 1 {
		t.Fatalf("changed = %d, want 1", changed)
	}
	if got := h.landing(id).status.String; !strings.HasPrefix(got, `{"state":"open"`) {
		t.Errorf("pr_status = %s", got)
	}
}

func TestRunOnceProjectWithoutPath(t *testing.T) {
	h := newHarness(t)
	h.exec(`UPDATE projects SET path = '' WHERE id = 7`)
	id := h.phase(1, StatePROpen, "github", 65, "")
	h.runOnce()
	if got := h.landing(id).landingError.String; !strings.Contains(got, "no known path") {
		t.Errorf("landing_error = %q", got)
	}
}

func TestRunOnceStopsOnCancelledContext(t *testing.T) {
	h := newHarness(t)
	h.phase(1, StatePROpen, "github", 71, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	checked, _, err := h.poller.RunOnce(ctx)
	if err == nil || checked != 0 {
		t.Fatalf("cancelled ctx: checked=%d err=%v", checked, err)
	}
}

func TestRunOnceStoreFailure(t *testing.T) {
	h := newHarness(t)
	h.db.Close()
	if _, _, err := h.poller.RunOnce(context.Background()); err == nil {
		t.Fatal("closed store: no error")
	}
}

func TestRefreshOne(t *testing.T) {
	h := newHarness(t)
	open := h.phase(1, StatePROpen, "github", 81, "")
	merged := h.phase(2, StateMerged, "github", 82, "")
	pushed := h.phase(3, "pushed", "github", 0, "")
	bare := h.phase(4, StatePROpen, "github", 0, "")
	ctx := context.Background()

	// Happy path: pr_open → merged.
	h.reply(81, reply{out: prJSON("MERGED", "x", "APPROVED", runOK)})
	st, state, err := h.poller.RefreshOne(ctx, open)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != repoprovider.StateMerged || st.CI != repoprovider.CISuccess || !st.CheckedAt.Equal(t0) || state != StateMerged {
		t.Fatalf("RefreshOne = %+v %q", st, state)
	}
	if h.landing(open).state != StateMerged || h.publishedCount() != 1 {
		t.Fatalf("not stored/published: %+v %v", h.landing(open), h.published)
	}

	// A merged phase is re-read but stays merged, whatever the host says.
	h.reply(82, reply{out: prJSON("CLOSED", "", "", runOK)})
	if _, state, err := h.poller.RefreshOne(ctx, merged); err != nil || state != StateMerged {
		t.Fatalf("merged refresh: %q %v", state, err)
	}
	if l := h.landing(merged); l.state != StateMerged || !strings.Contains(l.status.String, `"state":"closed"`) {
		t.Fatalf("merged phase after refresh: %+v", l)
	}

	// Not found, and no change request.
	if _, _, err := h.poller.RefreshOne(ctx, 99999); !errors.Is(err, ErrPhaseNotFound) {
		t.Errorf("unknown phase: %v", err)
	}
	if _, state, err := h.poller.RefreshOne(ctx, pushed); !errors.Is(err, ErrNoChangeRequest) || state != "pushed" {
		t.Errorf("pushed phase: %q %v", state, err)
	}
	if _, _, err := h.poller.RefreshOne(ctx, bare); !errors.Is(err, ErrNoChangeRequest) {
		t.Errorf("pr_open without a reference: %v", err)
	}
}

func TestRefreshOneProviderErrorIsReturnedAndStamped(t *testing.T) {
	h := newHarness(t)
	id := h.phase(1, StatePROpen, "github", 91, "")
	h.reply(91, reply{stderr: "HTTP 401: Bad credentials"})
	_, state, err := h.poller.RefreshOne(context.Background(), id)
	if !errors.Is(err, repoprovider.ErrNotAuthenticated) || state != StatePROpen {
		t.Fatalf("RefreshOne 401: %q %v", state, err)
	}
	if len(h.expired) != 1 || h.expired[0] != h.projectID {
		t.Errorf("OnAuthExpired = %v", h.expired)
	}
	if got := h.landing(id).landingError.String; !strings.HasPrefix(got, "not-authenticated: ") {
		t.Errorf("landing_error = %q", got)
	}
}

func TestRefreshOneStoreFailure(t *testing.T) {
	h := newHarness(t)
	h.db.Close()
	if _, _, err := h.poller.RefreshOne(context.Background(), 1); err == nil || errors.Is(err, ErrPhaseNotFound) {
		t.Fatalf("closed store: %v", err)
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		err  error
		code string
	}{
		{repoprovider.Classify("HTTP 401", errors.New("exit status 1")), CodeNotAuthenticated},
		{repoprovider.Classify("", &execNotFound{}), CodeStatusFailed},
		{fmt.Errorf("wrap: %w", repoprovider.ErrBinaryMissing), CodeBinaryMissing},
		{fmt.Errorf("wrap: %w", repoprovider.ErrNoRemote), CodeNoRemote},
		{fmt.Errorf("wrap: %w", repoprovider.ErrUnknownProvider), CodeProviderUnknown},
		{errors.New("token " + ghToken + " broke"), CodeStatusFailed},
	}
	for _, c := range cases {
		code, detail := Classify(c.err)
		if code != c.code {
			t.Errorf("Classify(%v) code = %q, want %q", c.err, code, c.code)
		}
		if strings.Contains(detail, ghToken) {
			t.Errorf("Classify(%v) leaked the token: %q", c.err, detail)
		}
	}
}

// execNotFound is an error that is not exec.ErrNotFound, so Classify leaves it
// unclassified.
type execNotFound struct{}

func (*execNotFound) Error() string { return "something else" }

func TestDefaultsWithoutHooks(t *testing.T) {
	h := newHarness(t)
	id := h.phase(1, StatePROpen, "github", 95, "")
	h.reply(95, reply{stderr: "HTTP 401"})
	p := &Poller{DB: h.db, Factory: h.poller.Factory, Exec: h.fake} // no Clock, Publish, OnAuthExpired, Logf
	if checked, changed, err := p.RunOnce(context.Background()); err != nil || checked != 1 || changed != 1 {
		t.Fatalf("RunOnce without hooks: %d %d %v", checked, changed, err)
	}
	if l := h.landing(id); !l.checkedAt.Valid || !strings.HasPrefix(l.landingError.String, "not-authenticated") {
		t.Fatalf("columns: %+v", l)
	}
	if (&Poller{}).exec() == nil {
		t.Fatal("nil Exec has no default")
	}
}
