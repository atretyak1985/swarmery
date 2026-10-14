package approvals

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

// fakeClock is a settable Options.Now.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// modInput is the HookInput the link handler builds for one mod ask.
func modInput(uuid, tool, toolUseID string, toolInput string) HookInput {
	raw := fmt.Sprintf(`{"session_id":%q,"cwd":"/tmp/proj","tool_name":%q,"tool_input":%s,"tool_use_id":%q,"hook_event_name":"tool.check"}`,
		uuid, tool, toolInput, toolUseID)
	return HookInput{
		Raw: []byte(raw), SessionUUID: uuid, ToolName: tool,
		ToolInput: json.RawMessage(toolInput), CWD: "/tmp/proj", ToolUseID: toolUseID,
	}
}

type modRow struct {
	status, channel, via string
	toolUseID            sql.NullString
	hash                 string
}

func readModRow(t *testing.T, db *sql.DB, id int64) modRow {
	t.Helper()
	var r modRow
	var via sql.NullString
	if err := db.QueryRow(
		`SELECT status, channel, resolved_via, tool_use_id, dedup_hash FROM permission_requests WHERE id = ?`, id).
		Scan(&r.status, &r.channel, &via, &r.toolUseID, &r.hash); err != nil {
		t.Fatal(err)
	}
	r.via = via.String
	return r
}

func countRequests(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM permission_requests`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestOpenModCreatesLeasedModRow: a fresh ask makes one pending channel='mod'
// row carrying the tool_use_id, with the SAME dedup hash the shim would mint.
func TestOpenModCreatesLeasedModRow(t *testing.T) {
	db := testDB(t)
	seedSession(t, db, "uuid-mod")
	svc := New(db, nil, Options{})

	id, _, pending, err := svc.OpenMod(modInput("uuid-mod", "Bash", "toolu_1", `{"command":"ls"}`))
	if err != nil || !pending {
		t.Fatalf("OpenMod: id=%d pending=%v err=%v", id, pending, err)
	}
	r := readModRow(t, db, id)
	if r.status != StatusPending || r.channel != ChannelMod || r.toolUseID.String != "toolu_1" {
		t.Errorf("row = %+v, want pending/mod/toolu_1", r)
	}
	want, _ := DedupHash("uuid-mod", "Bash", json.RawMessage(`{ "command": "ls" }`))
	if r.hash != want {
		t.Errorf("dedup_hash = %s, want the shim's %s", r.hash, want)
	}
	svc.mu.Lock()
	live := svc.leaseLiveLocked(id)
	svc.mu.Unlock()
	if !live {
		t.Error("no live lease after OpenMod")
	}
}

// TestOpenModIdempotentByToolUseID: a re-POST of the same call returns the
// same row — pending while pending, its decision once terminal.
func TestOpenModIdempotentByToolUseID(t *testing.T) {
	db := testDB(t)
	seedSession(t, db, "uuid-idem")
	svc := New(db, nil, Options{})
	in := modInput("uuid-idem", "Bash", "toolu_2", `{"command":"make test"}`)

	id1, _, _, err := svc.OpenMod(in)
	if err != nil {
		t.Fatal(err)
	}
	id2, _, pending, err := svc.OpenMod(in)
	if err != nil || id2 != id1 || !pending {
		t.Fatalf("re-POST: id=%d (want %d) pending=%v err=%v", id2, id1, pending, err)
	}
	if err := svc.Decide(id1, StatusDenied, "dashboard", "not now"); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	id3, d, pending, err := svc.OpenMod(in)
	if err != nil || id3 != id1 || pending {
		t.Fatalf("re-POST after deny: id=%d pending=%v err=%v", id3, pending, err)
	}
	if d.Status != StatusDenied || d.Reason != "not now" {
		t.Errorf("decision = %+v, want denied/'not now'", d)
	}
	if n := countRequests(t, db); n != 1 {
		t.Errorf("rows = %d, want 1", n)
	}
}

// TestModDecideBetweenPolls: the dashboard decides while no GET is in flight —
// the live lease counts as a waiter (no ErrNoWaiter), and the next poll gets
// the decision.
func TestModDecideBetweenPolls(t *testing.T) {
	db := testDB(t)
	seedSession(t, db, "uuid-between")
	svc := New(db, nil, Options{})

	id, _, _, err := svc.OpenMod(modInput("uuid-between", "Bash", "toolu_3", `{"command":"ls"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Decide(id, StatusApproved, "dashboard", ""); err != nil {
		t.Fatalf("Decide with a live lease: %v", err)
	}
	d, pending, err := svc.WaitMod(context.Background(), id, "uuid-between", time.Second)
	if err != nil || pending || d.Status != StatusApproved {
		t.Fatalf("WaitMod = %+v pending=%v err=%v, want approved", d, pending, err)
	}
	if r := readModRow(t, db, id); r.via != "dashboard" || r.channel != ChannelMod {
		t.Errorf("row = %+v, want via dashboard, channel mod", r)
	}
}

// TestWaitModWakesOnDecision: a poll in flight is woken by the decision.
func TestWaitModWakesOnDecision(t *testing.T) {
	db := testDB(t)
	seedSession(t, db, "uuid-wake")
	svc := New(db, nil, Options{})
	id, _, _, err := svc.OpenMod(modInput("uuid-wake", "Bash", "toolu_4", `{"command":"ls"}`))
	if err != nil {
		t.Fatal(err)
	}
	type res struct {
		d       Decision
		pending bool
		err     error
	}
	out := make(chan res, 1)
	go func() {
		d, p, err := svc.WaitMod(context.Background(), id, "uuid-wake", 5*time.Second)
		out <- res{d, p, err}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		svc.mu.Lock()
		n := len(svc.waiters[id])
		svc.mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := svc.Decide(id, StatusDenied, "dashboard", "nope"); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-out:
		if r.err != nil || r.pending || r.d.Status != StatusDenied || r.d.Reason != "nope" {
			t.Fatalf("WaitMod = %+v", r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("WaitMod not woken")
	}
}

// TestWaitModTimeoutKeepsRowPending: a poll that times out detaches without
// resolving and renews the lease; a later decision still lands.
func TestWaitModTimeoutKeepsRowPending(t *testing.T) {
	db := testDB(t)
	seedSession(t, db, "uuid-timeout")
	svc := New(db, nil, Options{})
	id, _, _, err := svc.OpenMod(modInput("uuid-timeout", "Bash", "toolu_5", `{"command":"ls"}`))
	if err != nil {
		t.Fatal(err)
	}
	_, pending, err := svc.WaitMod(context.Background(), id, "uuid-timeout", 30*time.Millisecond)
	if err != nil || !pending {
		t.Fatalf("WaitMod: pending=%v err=%v, want pending", pending, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, pending, err := svc.WaitMod(ctx, id, "uuid-timeout", time.Second); err != nil || !pending {
		t.Fatalf("WaitMod on a dead ctx: pending=%v err=%v", pending, err)
	}
	if _, pending, err := svc.WaitMod(context.Background(), id, "uuid-timeout", 0); err != nil || !pending {
		t.Fatalf("WaitMod wait=0: pending=%v err=%v", pending, err)
	}
	if s := requestStatus(t, db, id); s != StatusPending {
		t.Fatalf("status after timed-out polls = %s, want pending", s)
	}
	svc.mu.Lock()
	n := len(svc.waiters[id])
	svc.mu.Unlock()
	if n != 0 {
		t.Errorf("waiters after timed-out polls = %d, want 0", n)
	}
	if err := svc.Decide(id, StatusApproved, "dashboard", ""); err != nil {
		t.Fatalf("Decide after timed-out poll: %v", err)
	}
}

// TestWaitModWrongSession: another session's id is not found.
func TestWaitModWrongSession(t *testing.T) {
	db := testDB(t)
	seedSession(t, db, "uuid-owner")
	svc := New(db, nil, Options{})
	id, _, _, err := svc.OpenMod(modInput("uuid-owner", "Bash", "toolu_6", `{"command":"ls"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.WaitMod(context.Background(), id, "uuid-other", 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("wrong session: err = %v, want ErrNotFound", err)
	}
	if _, _, err := svc.WaitMod(context.Background(), id+100, "uuid-owner", 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id: err = %v, want ErrNotFound", err)
	}
}

// TestModLeaseLapse: once the lease lapses with nobody attached, Decide is
// refused (ErrNoWaiter) and Sweep resolves the row resolved_elsewhere via
// 'terminal'; the kept decision is pruned after 2×TTL.
func TestModLeaseLapse(t *testing.T) {
	db := testDB(t)
	seedSession(t, db, "uuid-lapse")
	clock := newFakeClock()
	svc := New(db, nil, Options{Now: clock.Now})
	id, _, _, err := svc.OpenMod(modInput("uuid-lapse", "Bash", "toolu_7", `{"command":"ls"}`))
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(ModLeaseTTL + time.Second)
	if err := svc.Decide(id, StatusApproved, "dashboard", ""); !errors.Is(err, ErrNoWaiter) {
		t.Fatalf("Decide after lapse: %v, want ErrNoWaiter", err)
	}
	svc.Sweep()
	if r := readModRow(t, db, id); r.status != StatusResolvedElsewhere || r.via != "terminal" {
		t.Fatalf("row after sweep = %+v, want resolved_elsewhere/terminal", r)
	}
	d, pending, err := svc.WaitMod(context.Background(), id, "uuid-lapse", 0)
	if err != nil || pending || d.Status != StatusResolvedElsewhere {
		t.Errorf("WaitMod after lapse = %+v pending=%v err=%v", d, pending, err)
	}

	// A kept decision is pruned after 2×TTL; the row still answers.
	id2, _, _, err := svc.OpenMod(modInput("uuid-lapse", "Bash", "toolu_8", `{"command":"pwd"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Decide(id2, StatusApproved, "dashboard", ""); err != nil {
		t.Fatal(err)
	}
	clock.Advance(2*ModLeaseTTL + time.Second)
	svc.Sweep()
	svc.mu.Lock()
	_, kept := svc.modDecided[id2]
	svc.mu.Unlock()
	if kept {
		t.Error("modDecided not pruned after 2×TTL")
	}
	d, _, err = svc.WaitMod(context.Background(), id2, "uuid-lapse", 0)
	if err != nil || d.Status != StatusApproved || d.Via != "dashboard" {
		t.Errorf("decision rebuilt from the row = %+v err=%v", d, err)
	}
}

// TestShimAttachesToPendingModRow: a shim ask for a call the mod is still
// asking about waits on the mod's row — one row, both get the decision.
// The shim's Detach does not resolve the row while the mod's lease lives.
func TestShimAttachesToPendingModRow(t *testing.T) {
	db := testDB(t)
	seedSession(t, db, "uuid-both")
	svc := New(db, nil, Options{})
	modID, _, _, err := svc.OpenMod(modInput("uuid-both", "Bash", "toolu_9", `{"command":"curl x","description":"d"}`))
	if err != nil {
		t.Fatal(err)
	}
	shim := hookInput(t, "uuid-both", "Bash", "curl x")
	id, ch, isNew, err := svc.Open(shim)
	if err != nil || isNew || id != modID {
		t.Fatalf("shim Open: id=%d (mod %d) isNew=%v err=%v", id, modID, isNew, err)
	}
	// A second shim that hangs up: the row stays pending (lease live).
	_, ch2, _, err := svc.Open(shim)
	if err != nil {
		t.Fatal(err)
	}
	svc.Detach(id, ch2)
	svc.Detach(id, ch)
	if s := requestStatus(t, db, id); s != StatusPending {
		t.Fatalf("status after shim detach under a live lease = %s, want pending", s)
	}
	_, ch, _, err = svc.Open(shim)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Decide(id, StatusApproved, "dashboard", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-ch:
		if d.Status != StatusApproved {
			t.Errorf("shim decision = %+v", d)
		}
	case <-time.After(time.Second):
		t.Fatal("shim not woken")
	}
	d, _, err := svc.WaitMod(context.Background(), id, "uuid-both", 0)
	if err != nil || d.Status != StatusApproved {
		t.Errorf("mod decision = %+v err=%v", d, err)
	}
	if n := countRequests(t, db); n != 1 {
		t.Errorf("rows = %d, want 1", n)
	}
}

// TestOpenModAttachesToPendingShimRow: the mod asking about a call a shim row
// already holds attaches to it and carries its tool_use_id onto the row.
func TestOpenModAttachesToPendingShimRow(t *testing.T) {
	db := testDB(t)
	seedSession(t, db, "uuid-shimfirst")
	svc := New(db, nil, Options{})
	shimID, _, _, err := svc.Open(hookInput(t, "uuid-shimfirst", "Bash", "ls"))
	if err != nil {
		t.Fatal(err)
	}
	id, _, pending, err := svc.OpenMod(modInput("uuid-shimfirst", "Bash", "toolu_10", `{"command":"ls","description":"d"}`))
	if err != nil || id != shimID || !pending {
		t.Fatalf("OpenMod: id=%d (shim %d) pending=%v err=%v", id, shimID, pending, err)
	}
	if r := readModRow(t, db, id); r.channel != ChannelShim || r.toolUseID.String != "toolu_10" {
		t.Errorf("row = %+v, want channel shim carrying toolu_10", r)
	}
}

// TestShimAfterModWindow: a shim ask within 5 s of the mod handing the same
// call back gets resolved_elsewhere at once with no new row; after the window
// it opens its own shim row.
func TestShimAfterModWindow(t *testing.T) {
	for _, status := range []string{StatusExpired, StatusResolvedElsewhere} {
		t.Run(status, func(t *testing.T) {
			db := testDB(t)
			seedSession(t, db, "uuid-window")
			clock := newFakeClock()
			svc := New(db, nil, Options{Now: clock.Now})
			modID, _, _, err := svc.OpenMod(modInput("uuid-window", "Bash", "toolu_11", `{"command":"rm -rf build","description":"d"}`))
			if err != nil {
				t.Fatal(err)
			}
			if err := svc.Resolve(modID, status, "", ""); err != nil {
				t.Fatal(err)
			}
			clock.Advance(4 * time.Second)
			shim := hookInput(t, "uuid-window", "Bash", "rm -rf build")
			id, ch, isNew, err := svc.Open(shim)
			if err != nil || isNew || id != modID {
				t.Fatalf("shim within window: id=%d isNew=%v err=%v", id, isNew, err)
			}
			select {
			case d := <-ch:
				if d.Status != StatusResolvedElsewhere {
					t.Errorf("decision = %+v, want resolved_elsewhere", d)
				}
			default:
				t.Fatal("no immediate decision within the window")
			}
			if n := countRequests(t, db); n != 1 {
				t.Errorf("rows within window = %d, want 1", n)
			}

			clock.Advance(2 * time.Second) // 6 s after the hand-back
			id, _, isNew, err = svc.Open(shim)
			if err != nil || !isNew || id == modID {
				t.Fatalf("shim after window: id=%d isNew=%v err=%v", id, isNew, err)
			}
			if r := readModRow(t, db, id); r.channel != ChannelShim || r.toolUseID.Valid {
				t.Errorf("new row = %+v, want channel shim, no tool_use_id", r)
			}
		})
	}
}

// TestShimAfterModWindowIgnoresDecidedRows: an approved or denied mod row is
// not a hand-back — a later identical shim ask opens a row as usual.
func TestShimAfterModWindowIgnoresDecidedRows(t *testing.T) {
	db := testDB(t)
	seedSession(t, db, "uuid-decided")
	svc := New(db, nil, Options{})
	modID, _, _, err := svc.OpenMod(modInput("uuid-decided", "Bash", "toolu_12", `{"command":"ls","description":"d"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Decide(modID, StatusDenied, "dashboard", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, isNew, err := svc.Open(hookInput(t, "uuid-decided", "Bash", "ls")); err != nil || !isNew {
		t.Fatalf("shim after a denied mod row: isNew=%v err=%v", isNew, err)
	}
}

// TestOpenModProdDeployAndRule: the shared create path hands a prod deploy to
// the terminal and lets a rule approve, both at once.
func TestOpenModProdDeployAndRule(t *testing.T) {
	db := testDB(t)
	seedSession(t, db, "uuid-prodrule")
	g, err := NewProdGuard(nil)
	if err != nil {
		t.Fatal(err)
	}
	seedRule(t, db, nil, "Bash(git *)")
	svc := New(db, nil, Options{ProdGuard: g})

	id, d, pending, err := svc.OpenMod(modInput("uuid-prodrule", "Bash", "toolu_13", `{"command":"acme-cli deploy --prod"}`))
	if err != nil || pending {
		t.Fatalf("prod OpenMod: pending=%v err=%v", pending, err)
	}
	if d.Status != StatusResolvedElsewhere || d.Via != ViaLocalOnly {
		t.Errorf("prod decision = %+v, want resolved_elsewhere via local-only", d)
	}
	if r := readModRow(t, db, id); r.status != StatusResolvedElsewhere || r.via != ViaLocalOnly || r.channel != ChannelMod {
		t.Errorf("prod row = %+v", r)
	}
	if err := svc.Decide(id, StatusApproved, "dashboard", ""); !errors.Is(err, ErrAlreadyResolved) && !errors.Is(err, ErrLocalOnly) {
		t.Errorf("approve prod row: %v", err)
	}

	id, d, pending, err = svc.OpenMod(modInput("uuid-prodrule", "Bash", "toolu_14", `{"command":"git status"}`))
	if err != nil || pending || d.Status != StatusApproved || d.Via != ViaRule {
		t.Fatalf("rule OpenMod: d=%+v pending=%v err=%v", d, pending, err)
	}
	if r := readModRow(t, db, id); r.via != ViaRule {
		t.Errorf("rule row via = %q", r.via)
	}
}

// TestOpenModAnswerKeepsUpdatedInput: an AskUserQuestion answered between
// polls reaches the next poll with its {questions, answers}.
func TestOpenModAnswerKeepsUpdatedInput(t *testing.T) {
	db := testDB(t)
	seedSession(t, db, "uuid-ask")
	svc := New(db, nil, Options{AnswerDelivery: DeliveryDenyMessage})
	questions := `{"questions":[{"question":"Which?","header":"H","multiSelect":false,"options":[{"label":"A","description":"a"},{"label":"B","description":"b"}]}]}`
	id, _, _, err := svc.OpenMod(modInput("uuid-ask", AskUserQuestionTool, "toolu_15", questions))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Answer(id, map[string]json.RawMessage{"Which?": json.RawMessage(`"B"`)}); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	d, pending, err := svc.WaitMod(context.Background(), id, "uuid-ask", 0)
	if err != nil || pending || d.Status != StatusApproved {
		t.Fatalf("WaitMod = %+v pending=%v err=%v", d, pending, err)
	}
	var ui struct {
		Questions []map[string]any `json:"questions"`
		Answers   map[string]any   `json:"answers"`
	}
	if err := json.Unmarshal(d.UpdatedInput, &ui); err != nil {
		t.Fatalf("UpdatedInput %s: %v", d.UpdatedInput, err)
	}
	if len(ui.Questions) != 1 || ui.Answers["Which?"] != "B" {
		t.Errorf("UpdatedInput = %s", d.UpdatedInput)
	}
}

// TestOpenModExcludedProject: an excluded cwd with no session row is refused
// with ErrExcludedProject and persists nothing.
func TestOpenModExcludedProject(t *testing.T) {
	db := testDB(t)
	svc := New(db, nil, Options{Exclude: ingest.ParseExcludeList(ingest.DefaultExclude)})
	in := modInput("uuid-excluded", "Bash", "toolu_16", `{"command":"ls"}`)
	if _, _, _, err := svc.OpenMod(in); !errors.Is(err, ErrExcludedProject) {
		t.Fatalf("OpenMod excluded: %v", err)
	}
	if n := countRequests(t, db); n != 0 {
		t.Errorf("rows = %d, want 0", n)
	}
}
