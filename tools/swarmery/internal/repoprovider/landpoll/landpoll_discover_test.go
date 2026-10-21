package landpoll

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Change-request discovery (land-criteria UX plan, phase 4, D7/D8): a PR opened
// outside the dashboard for a phase's run branch is found by RunOnce (throttled,
// capped) or by RefreshOne (on demand), recorded by a guarded UPDATE, and read at
// once. Same fake provider as landpoll_test.go, plus a scripted `gh pr list`.

// discoverHarness is harness plus scripted `gh pr list --head <branch>` answers.
type discoverHarness struct {
	*harness
	lmu   sync.Mutex
	lists map[string]reply // run branch → reply; unscripted ⇒ "[]"
	// onList, when set, runs inside a `gh pr list` call (a racing writer).
	onList func(branch string)
}

func newDiscoverHarness(t *testing.T) *discoverHarness {
	t.Helper()
	d := &discoverHarness{harness: newHarness(t), lists: map[string]reply{}}
	prev := d.fake.Fn
	d.fake.Fn = func(dir string, env []string, name string, args []string) (string, string, error, bool) {
		if name == "gh" && len(args) >= 4 && args[0] == "pr" && args[1] == "list" && args[2] == "--head" {
			branch := args[3]
			d.lmu.Lock()
			r, ok := d.lists[branch]
			hook := d.onList
			d.lmu.Unlock()
			if hook != nil {
				hook(branch)
			}
			switch {
			case !ok:
				return "[]", "", nil, true
			case r.stderr != "":
				return "", r.stderr, errors.New("exit status 1"), true
			default:
				return r.out, "", nil, true
			}
		}
		return prev(dir, env, name, args)
	}
	return d
}

// list scripts the `gh pr list` answer for branch.
func (d *discoverHarness) list(branch string, r reply) {
	d.lmu.Lock()
	d.lists[branch] = r
	d.lmu.Unlock()
}

// prItem is one `gh pr list --json number,url,state,mergedAt` entry.
func prItem(n int, state, mergedAt string) string {
	merged := "null"
	if mergedAt != "" {
		merged = fmt.Sprintf("%q", mergedAt)
	}
	return fmt.Sprintf(`{"number":%d,"url":"https://github.com/acme/widgets/pull/%d","state":%q,"mergedAt":%s}`, n, n, state, merged)
}

func prList(items ...string) string { return "[" + strings.Join(items, ",") + "]" }

// cand inserts a phase of task taskID in landing_state state with run_state
// runState, run branch branch ("" ⇒ NULL), landOpen open [LAND] criteria and
// last asked at checkedAt ("" ⇒ never).
func (d *discoverHarness) cand(taskID int64, seq int, state, runState, branch string, landOpen int, checkedAt string) int64 {
	d.t.Helper()
	var br, chk any
	if branch != "" {
		br = branch
	}
	if checkedAt != "" {
		chk = checkedAt
	}
	res := d.exec(`INSERT INTO epic_phases
		(workspace_task_id, seq, name, doc_path, depends_on, checkboxes_total, checkboxes_done,
		 landing_state, run_state, run_branch, criteria_land_open, pr_checked_at)
		VALUES (?, ?, ?, ?, '[]', 2, 1, ?, ?, ?, ?, ?)`,
		taskID, seq, fmt.Sprintf("Phase %d", seq), fmt.Sprintf("/plan/phase-%d.md", seq),
		state, runState, br, landOpen, chk)
	id, _ := res.LastInsertId()
	return id
}

// ready is a ready phase of the harness task: none + done, one open [LAND].
func (d *discoverHarness) ready(seq int) (int64, string) {
	branch := fmt.Sprintf("swarm/phase-%d", seq)
	return d.cand(d.taskID, seq, StateNone, "done", branch, 1, ""), branch
}

// ref is a phase's change-request columns as stored.
type ref struct {
	state, url, provider string
	number               sql.NullInt64
	checkedAt, status    sql.NullString
	landingError         sql.NullString
}

func (d *discoverHarness) ref(id int64) ref {
	d.t.Helper()
	var r ref
	var url, provider sql.NullString
	if err := d.db.QueryRow(`SELECT landing_state, pr_url, pr_provider, pr_number, pr_checked_at, pr_status, landing_error
		FROM epic_phases WHERE id = ?`, id).Scan(&r.state, &url, &provider, &r.number, &r.checkedAt, &r.status, &r.landingError); err != nil {
		d.t.Fatal(err)
	}
	r.url, r.provider = url.String, provider.String
	return r
}

func (d *discoverHarness) listCalls() []string {
	var out []string
	for _, c := range d.fake.Calls {
		if strings.HasPrefix(c, "gh pr list") {
			out = append(out, c)
		}
	}
	return out
}

func TestDiscoverReadyPhaseRecordsOpenPR(t *testing.T) {
	d := newDiscoverHarness(t)
	id, branch := d.ready(1)
	d.list(branch, reply{out: prList(prItem(21, "OPEN", ""), prItem(20, "CLOSED", ""))})
	d.reply(21, reply{out: prJSON("OPEN", "", "REVIEW_REQUIRED", runPending)})

	checked, changed := d.runOnce()
	if checked != 1 || changed != 1 {
		t.Fatalf("checked/changed = %d/%d, want 1/1", checked, changed)
	}
	r := d.ref(id)
	if r.state != StatePROpen || r.url != "https://github.com/acme/widgets/pull/21" ||
		r.number.Int64 != 21 || r.provider != "github" {
		t.Fatalf("stored = %+v, want pr_open #21 on github", r)
	}
	if !r.status.Valid || !strings.Contains(r.status.String, `"state":"open"`) || r.landingError.Valid {
		t.Errorf("the discovered PR was not read at once: status=%v error=%v", r.status, r.landingError)
	}
	want := "gh pr list --head " + branch + " --state all --json number,url,state,mergedAt --limit 20 --repo acme/widgets"
	if calls := d.listCalls(); len(calls) != 1 || calls[0] != want {
		t.Errorf("list calls = %q, want [%q]", calls, want)
	}
	wantLog := fmt.Sprintf("landpoll: phase %d discovered https://github.com/acme/widgets/pull/21 for %s", id, branch)
	if len(d.logs) == 0 || d.logs[0] != wantLog {
		t.Errorf("logs = %q, want %q first", d.logs, wantLog)
	}
	if d.publishedCount() == 0 {
		t.Error("a discovery was not published")
	}

	// The next tick reads it as an ordinary open PR and never lists again.
	d.now = t0.Add(2 * time.Hour)
	d.runOnce()
	if calls := d.listCalls(); len(calls) != 1 {
		t.Errorf("a recorded phase was listed again: %q", calls)
	}
}

func TestDiscoverMergedPRReachesMergedAndTicksLand(t *testing.T) {
	d := newDiscoverHarness(t)
	id, branch := d.ready(1)
	path := filepath.Join(d.dir, "phase-1.md")
	if err := os.WriteFile(path, []byte(landDoc), 0o640); err != nil {
		t.Fatal(err)
	}
	d.exec(`UPDATE epic_phases SET doc_path = ? WHERE id = ?`, path, id)
	d.list(branch, reply{out: prList(prItem(22, "MERGED", "2026-10-09T11:00:00Z"))})
	d.reply(22, reply{out: prJSON("MERGED", "2026-10-09T11:00:00Z", "APPROVED", runOK)})

	d.runOnce()
	if l := d.landing(id); l.state != StateMerged || l.landedAt.String != "2026-10-09T11:00:00Z" {
		t.Fatalf("landing = %+v, want merged with landed_at at the merge time", l)
	}
	if got := readDoc(t, path); got != landDocMerged {
		t.Fatalf("doc after a discovered merge:\n%s\nwant:\n%s", got, landDocMerged)
	}
}

func TestDiscoverNothingFoundOnlyStampsAndThrottles(t *testing.T) {
	d := newDiscoverHarness(t)
	id, _ := d.ready(1)

	if checked, changed := d.runOnce(); checked != 1 || changed != 0 {
		t.Fatalf("checked/changed = %d/%d, want 1/0", checked, changed)
	}
	r := d.ref(id)
	if r.state != StateNone || r.url != "" || r.number.Valid || r.status.Valid || r.landingError.Valid {
		t.Fatalf("not-found wrote more than pr_checked_at: %+v", r)
	}
	if r.checkedAt.String != t0.Format(time.RFC3339) {
		t.Fatalf("pr_checked_at = %v, want %s", r.checkedAt, t0.Format(time.RFC3339))
	}
	if d.publishedCount() != 0 {
		t.Errorf("a not-found published: %v", d.published)
	}

	// Within DiscoverMinAge: not asked again.
	d.now = t0.Add(DiscoverMinAge - time.Minute)
	if checked, _ := d.runOnce(); checked != 0 {
		t.Errorf("asked again within DiscoverMinAge: checked = %d", checked)
	}
	// Past it: asked again.
	d.now = t0.Add(DiscoverMinAge + time.Minute)
	if checked, _ := d.runOnce(); checked != 1 {
		t.Errorf("not asked again after DiscoverMinAge: checked = %d", checked)
	}
	if n := len(d.listCalls()); n != 2 {
		t.Errorf("list calls = %d, want 2", n)
	}
}

func TestDiscoverLimitAndOrder(t *testing.T) {
	d := newDiscoverHarness(t)
	for i := 1; i <= 7; i++ {
		d.ready(i)
	}
	if checked, _ := d.runOnce(); checked != DefaultDiscoverMax {
		t.Fatalf("checked = %d, want DefaultDiscoverMax %d", checked, DefaultDiscoverMax)
	}
	// The two never asked come first on the next tick, even before the hour.
	d.now = t0.Add(time.Minute)
	if checked, _ := d.runOnce(); checked != 2 {
		t.Fatalf("second tick checked = %d, want the 2 never-asked phases", checked)
	}
	calls := d.listCalls()
	for _, want := range []string{"swarm/phase-6 ", "swarm/phase-7 "} {
		if !strings.Contains(calls[len(calls)-1]+calls[len(calls)-2], "--head "+want) {
			t.Errorf("the second tick did not ask about %s: %q", want, calls)
		}
	}

	d.poller.DiscoverMax = 2
	d.now = t0.Add(3 * time.Hour)
	if checked, _ := d.runOnce(); checked != 2 {
		t.Errorf("DiscoverMax=2: checked = %d", checked)
	}
}

func TestDiscoverCandidateFilter(t *testing.T) {
	d := newDiscoverHarness(t)
	res := d.exec(`INSERT INTO tasks (project_id, title, prompt, status, created_at, started_at, source, external_id, archived_at)
		VALUES (7, 'Old', 'goal', 'done', '2026-09-01T00:00:00Z', '2026-09-01T00:00:00Z', 'workspace', '2026-09-01-old', '2026-09-30T00:00:00Z')`)
	archived, _ := res.LastInsertId()
	recent := t0.Add(-time.Minute).Format(time.RFC3339)
	stale := t0.Add(-2 * time.Hour).Format(time.RFC3339)

	asked := map[string]bool{
		"swarm/ready":          true,  // none + done + open [LAND]
		"swarm/pushed-no-land": true,  // pushed counts without an open [LAND]
		"swarm/stale":          true,  // asked over an hour ago
		"swarm/archived":       false, // archived plan
		"swarm/no-land":        false, // ready, but nothing left to land
		"swarm/partial":        false, // the run did not finish
		"swarm/returned":       false, // returned to the agent
		"swarm/recent":         false, // asked a minute ago
	}
	d.cand(d.taskID, 1, StateNone, "done", "swarm/ready", 1, "")
	d.cand(d.taskID, 2, StatePushed, "done", "swarm/pushed-no-land", 0, "")
	d.cand(d.taskID, 3, StateNone, "done", "swarm/stale", 2, stale)
	d.cand(archived, 1, StateNone, "done", "swarm/archived", 1, "")
	d.cand(d.taskID, 4, StateNone, "done", "swarm/no-land", 0, "")
	d.cand(d.taskID, 5, StateNone, "partial", "swarm/partial", 1, "")
	d.cand(d.taskID, 6, "returned", "done", "swarm/returned", 1, "")
	d.cand(d.taskID, 7, StateNone, "done", "swarm/recent", 1, recent)
	d.cand(d.taskID, 8, StateNone, "done", "", 1, "") // no run branch
	// A phase with a change request recorded (but somehow not pr_open) is never listed.
	withPR := d.cand(d.taskID, 9, StatePushed, "done", "swarm/has-pr", 1, "")
	d.exec(`UPDATE epic_phases SET pr_url = 'https://github.com/acme/widgets/pull/1' WHERE id = ?`, withPR)

	d.poller.DiscoverMax = 50
	d.runOnce()
	calls := strings.Join(d.listCalls(), "\n")
	for branch, want := range asked {
		if got := strings.Contains(calls, "--head "+branch+" "); got != want {
			t.Errorf("%s: asked = %v, want %v", branch, got, want)
		}
	}
	if strings.Contains(calls, "swarm/has-pr") || strings.Count(calls, "gh pr list") != 3 {
		t.Errorf("list calls:\n%s\nwant exactly the 3 candidates", calls)
	}
}

func TestDiscoverGuardedUpdateLosesToARacingWriter(t *testing.T) {
	d := newDiscoverHarness(t)
	id, branch := d.ready(1)
	d.list(branch, reply{out: prList(prItem(23, "OPEN", ""))})
	const theirs = "https://github.com/acme/widgets/pull/99"
	// The land handler records its own PR while the host is being asked.
	d.onList = func(string) {
		d.exec(`UPDATE epic_phases SET landing_state = 'pr_open', pr_url = ?, pr_number = 99 WHERE id = ?`, theirs, id)
	}

	_, changed := d.runOnce()
	r := d.ref(id)
	if r.url != theirs || r.number.Int64 != 99 {
		t.Fatalf("the guarded UPDATE overwrote the racing writer: %+v", r)
	}
	if changed != 0 || len(d.viewCalls()) != 0 {
		t.Errorf("a lost race still counted or read: changed=%d views=%q", changed, d.viewCalls())
	}
}

// A failed background lookup advances the throttle and is logged, but writes no
// landing_error: a ready phase has no landing to fail, and a not-authenticated
// stamp would keep the project's auth banner up (internal/api/vcs.go, rule 3)
// until a land cleared it.
func TestDiscoverBackgroundFailureOnlyThrottles(t *testing.T) {
	d := newDiscoverHarness(t)
	id, branch := d.ready(1)
	d.list(branch, reply{stderr: "HTTP 401: Bad credentials " + ghToken})

	if checked, changed := d.runOnce(); checked != 1 || changed != 0 {
		t.Fatalf("checked/changed = %d/%d, want 1/0", checked, changed)
	}
	r := d.ref(id)
	if r.landingError.Valid {
		t.Errorf("landing_error = %q, want NULL after a background discovery failure", r.landingError.String)
	}
	if r.state != StateNone || r.checkedAt.String != t0.Format(time.RFC3339) {
		t.Errorf("stored = %+v, want state none and pr_checked_at stamped", r)
	}
	if d.publishedCount() != 0 {
		t.Errorf("a background discovery failure published: %v", d.published)
	}
	if len(d.expired) != 1 || d.expired[0] != d.projectID {
		t.Errorf("expired = %v, want the project", d.expired)
	}
	logs := strings.Join(d.logs, "\n")
	if !strings.Contains(logs, "discovery failed: "+CodeNotAuthenticated+": ") || strings.Contains(logs, ghToken) {
		t.Errorf("logs = %q, want a redacted not-authenticated discovery failure", d.logs)
	}

	// The throttle advanced: not asked again within DiscoverMinAge.
	d.now = t0.Add(time.Minute)
	if checked, _ := d.runOnce(); checked != 0 {
		t.Errorf("a failed phase was asked again within DiscoverMinAge: checked = %d", checked)
	}
}

// The land handler's own error on a pushed phase (the push went through, the
// PR did not open) is the operator's to read; a later not-found or failed
// discovery tick must not clear or overwrite it.
func TestDiscoverKeepsTheLandHandlersError(t *testing.T) {
	const landErr = "open-failed: gh pr create: GraphQL: base branch not found"
	for _, c := range []struct {
		name string
		r    reply
	}{
		{"not found", reply{out: "[]"}},
		{"host failure", reply{stderr: "HTTP 502: Bad gateway"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := newDiscoverHarness(t)
			id := d.cand(d.taskID, 1, StatePushed, "done", "swarm/phase-1", 1, "")
			d.exec(`UPDATE epic_phases SET landing_error = ? WHERE id = ?`, landErr, id)
			d.list("swarm/phase-1", c.r)

			if checked, _ := d.runOnce(); checked != 1 {
				t.Fatalf("checked = %d, want the pushed phase asked", checked)
			}
			r := d.ref(id)
			if r.landingError.String != landErr {
				t.Errorf("landing_error = %v, want the land handler's %q kept", r.landingError, landErr)
			}
			if r.state != StatePushed || r.checkedAt.String != t0.Format(time.RFC3339) {
				t.Errorf("stored = %+v, want pushed with pr_checked_at stamped", r)
			}
		})
	}
}

func TestRefreshOneDiscovers(t *testing.T) {
	ctx := context.Background()

	t.Run("found", func(t *testing.T) {
		d := newDiscoverHarness(t)
		// Pushed, no open [LAND], asked a minute ago: a click ignores the throttle.
		id := d.cand(d.taskID, 1, StatePushed, "done", "swarm/phase-1", 0, t0.Add(-time.Minute).Format(time.RFC3339))
		d.list("swarm/phase-1", reply{out: prList(prItem(31, "OPEN", ""))})
		d.reply(31, reply{out: prJSON("OPEN", "", "", runOK)})
		st, state, err := d.poller.RefreshOne(ctx, id)
		if err != nil || state != StatePROpen || st.State != "open" {
			t.Fatalf("RefreshOne = %+v %q %v, want open / pr_open", st, state, err)
		}
		if r := d.ref(id); r.number.Int64 != 31 {
			t.Errorf("stored = %+v", r)
		}
	})

	t.Run("not found", func(t *testing.T) {
		d := newDiscoverHarness(t)
		id, _ := d.ready(1)
		if _, state, err := d.poller.RefreshOne(ctx, id); !errors.Is(err, ErrNoChangeRequestFound) || state != StateNone {
			t.Fatalf("RefreshOne = %q %v, want ErrNoChangeRequestFound", state, err)
		}
	})

	t.Run("host failure", func(t *testing.T) {
		d := newDiscoverHarness(t)
		id, branch := d.ready(1)
		d.list(branch, reply{stderr: "HTTP 401: Bad credentials"})
		if _, _, err := d.poller.RefreshOne(ctx, id); err == nil || errors.Is(err, ErrNoChangeRequestFound) {
			t.Fatalf("RefreshOne err = %v, want the classified host failure", err)
		}
		// The click is answered with the failure and, unlike a background tick,
		// stamps it on the phase.
		if r := d.ref(id); !strings.HasPrefix(r.landingError.String, CodeNotAuthenticated+": ") {
			t.Errorf("landing_error = %v, want the click's not-authenticated stamp", r.landingError)
		}
	})

	t.Run("found but its status read fails", func(t *testing.T) {
		d := newDiscoverHarness(t)
		id, branch := d.ready(1)
		d.list(branch, reply{out: prList(prItem(32, "OPEN", ""))}) // no `gh pr view 32` scripted
		if _, state, err := d.poller.RefreshOne(ctx, id); err != nil || state != StatePROpen {
			t.Fatalf("RefreshOne = %q %v, want pr_open recorded without an error", state, err)
		}
		if r := d.ref(id); r.number.Int64 != 32 || !strings.HasPrefix(r.landingError.String, CodeStatusFailed) {
			t.Errorf("stored = %+v, want #32 with the status failure stamped", r)
		}
	})

	t.Run("not discoverable", func(t *testing.T) {
		d := newDiscoverHarness(t)
		for i, c := range []struct{ state, runState, branch string }{
			{"returned", "done", "swarm/r"},
			{StateNone, "partial", "swarm/p"},
			{StatePushed, "done", ""},
		} {
			id := d.cand(d.taskID, i+1, c.state, c.runState, c.branch, 1, "")
			if _, _, err := d.poller.RefreshOne(ctx, id); !errors.Is(err, ErrNoChangeRequest) {
				t.Errorf("%+v: err = %v, want ErrNoChangeRequest", c, err)
			}
		}
		if n := len(d.fake.Calls); n != 0 {
			t.Errorf("a refusal reached the host: %q", d.fake.Calls)
		}
	})
}
