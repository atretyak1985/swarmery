package ingest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// line builds one minimal user-prompt JSONL line.
func line(uuid, ts, text string) string {
	return fmt.Sprintf(`{"type":"user","parentUuid":null,"isSidechain":false,"promptId":"p-%s","promptSource":"typed","message":{"role":"user","content":%q},"uuid":"%s","timestamp":"%s","cwd":"/tmp/tail-proj","sessionId":"33333333-0000-4000-8000-000000000003","version":"2.1.170","gitBranch":"main"}`+"\n",
		uuid, text, uuid, ts)
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustAppend(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
}

// TestTailOffsetResume: tail, append, tail again — the second pass must
// resume from the persisted byte offset (as after a daemon restart) and only
// ingest the appended lines, with zero duplicates.
func TestTailOffsetResume(t *testing.T) {
	db := testDB(t)
	path := filepath.Join(t.TempDir(), "session.jsonl")
	mustWrite(t, path,
		line("aaaaaaaa-0000-4000-8000-000000000001", "2026-07-12T10:00:00.000Z", "first")+
			line("aaaaaaaa-0000-4000-8000-000000000002", "2026-07-12T10:00:01.000Z", "second"))

	res1, err := TailFile(db, path, Thresholds{})
	if err != nil {
		t.Fatalf("first tail: %v", err)
	}
	if res1.Lines != 2 || len(res1.NewEventIDs) != 2 || !res1.SessionCreated {
		t.Fatalf("first tail = %+v, want 2 lines / 2 events / session created", res1)
	}
	fi, _ := os.Stat(path)
	if res1.NextOffset != fi.Size() {
		t.Fatalf("NextOffset = %d, want file size %d", res1.NextOffset, fi.Size())
	}

	// "Restart": a fresh TailFile call reads the offset from file_offsets.
	mustAppend(t, path, line("aaaaaaaa-0000-4000-8000-000000000003", "2026-07-12T10:00:02.000Z", "third"))
	res2, err := TailFile(db, path, Thresholds{})
	if err != nil {
		t.Fatalf("second tail: %v", err)
	}
	if res2.StartOffset != res1.NextOffset {
		t.Errorf("resume offset = %d, want %d", res2.StartOffset, res1.NextOffset)
	}
	if res2.Lines != 1 || len(res2.NewEventIDs) != 1 || res2.SessionCreated {
		t.Errorf("second tail = %+v, want exactly the 1 appended line", res2)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM events`); got != 3 {
		t.Errorf("events = %d, want 3", got)
	}
	// The session title stays the FIRST prompt — later batches must not
	// overwrite it with their own first line (only ai-title may).
	var title string
	if err := db.QueryRow(`SELECT title FROM sessions`).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "first" {
		t.Errorf("title = %q, want %q (tail batches must not rewrite it)", title, "first")
	}
	// Third pass with nothing new is a no-op.
	res3, err := TailFile(db, path, Thresholds{})
	if err != nil {
		t.Fatal(err)
	}
	if res3.Lines != 0 || res3.StartOffset != res3.NextOffset {
		t.Errorf("no-op tail = %+v, want zero lines", res3)
	}
}

// TestTailPartialLine: a trailing line without '\n' must not be consumed; it
// is ingested once the newline arrives.
func TestTailPartialLine(t *testing.T) {
	db := testDB(t)
	path := filepath.Join(t.TempDir(), "session.jsonl")
	full := line("bbbbbbbb-0000-4000-8000-000000000001", "2026-07-12T10:00:00.000Z", "complete")
	partial := line("bbbbbbbb-0000-4000-8000-000000000002", "2026-07-12T10:00:01.000Z", "in flight")
	half := partial[:len(partial)/2]
	mustWrite(t, path, full+half)

	res1, err := TailFile(db, path, Thresholds{})
	if err != nil {
		t.Fatal(err)
	}
	if res1.Lines != 1 {
		t.Fatalf("lines = %d, want 1 (partial line must wait)", res1.Lines)
	}
	if res1.NextOffset != int64(len(full)) {
		t.Fatalf("NextOffset = %d, want %d (stop before the partial line)", res1.NextOffset, len(full))
	}

	mustAppend(t, path, partial[len(half):])
	res2, err := TailFile(db, path, Thresholds{})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Lines != 1 || len(res2.NewEventIDs) != 1 {
		t.Errorf("after completing the line: %+v, want 1 line / 1 event", res2)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM events`); got != 2 {
		t.Errorf("events = %d, want 2", got)
	}
}

// TestTailFileRecreated: deleting and recreating the file (new inode) must
// reset the offset to 0; dedup absorbs the re-read so no duplicates appear.
func TestTailFileRecreated(t *testing.T) {
	db := testDB(t)
	path := filepath.Join(t.TempDir(), "session.jsonl")
	content := line("cccccccc-0000-4000-8000-000000000001", "2026-07-12T10:00:00.000Z", "one") +
		line("cccccccc-0000-4000-8000-000000000002", "2026-07-12T10:00:01.000Z", "two")
	mustWrite(t, path, content)
	if _, err := TailFile(db, path, Thresholds{}); err != nil {
		t.Fatal(err)
	}

	// Recreate with the same content but a shorter tail → new inode + shrink.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	shorter := line("cccccccc-0000-4000-8000-000000000001", "2026-07-12T10:00:00.000Z", "one")
	mustWrite(t, path, shorter)

	res, err := TailFile(db, path, Thresholds{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Reset {
		t.Errorf("Reset = false, want true (inode changed / size shrank)")
	}
	if res.StartOffset != 0 {
		t.Errorf("StartOffset = %d, want 0", res.StartOffset)
	}
	if len(res.NewEventIDs) != 0 {
		t.Errorf("new events on replay = %d, want 0 (dedup)", len(res.NewEventIDs))
	}
	if got := count(t, db, `SELECT COUNT(*) FROM events`); got != 2 {
		t.Errorf("events = %d, want 2 (no duplicates)", got)
	}
}

// fixtureRoot builds a temp projects root mirroring ~/.claude/projects layout
// from the committed fixtures (main transcripts + sidechain companion).
func fixtureRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	slug := filepath.Join(root, "-tmp-fixture-proj")
	copyFile := func(src, dst string) {
		t.Helper()
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		mustWrite(t, dst, string(b))
	}
	copyFile(filepath.Join(fixtures, "simple-session.jsonl"), filepath.Join(slug, "simple-session.jsonl"))
	copyFile(filepath.Join(fixtures, "tool-heavy-session.jsonl"), filepath.Join(slug, "tool-heavy-session.jsonl"))
	copyFile(filepath.Join(fixtures, "subagent-session.jsonl"), filepath.Join(slug, "subagent-session.jsonl"))
	copyFile(filepath.Join(fixtures, "subagent-session", "subagents", "agent-ab12cd34ef56ab78d.jsonl"),
		filepath.Join(slug, "subagent-session", "subagents", "agent-ab12cd34ef56ab78d.jsonl"))
	copyFile(filepath.Join(fixtures, "subagent-session", "subagents", "agent-ab12cd34ef56ab78d.meta.json"),
		filepath.Join(slug, "subagent-session", "subagents", "agent-ab12cd34ef56ab78d.meta.json"))
	return root
}

// TestRepeatedBackfillNoDuplicates: a second (and third) full backfill of the
// same root must create exactly zero new rows.
func TestRepeatedBackfillNoDuplicates(t *testing.T) {
	db := testDB(t)
	root := fixtureRoot(t)
	p := NewPipeline(db, Config{ProjectsRoot: root}, nil)
	p.Backfill(context.Background())

	events := count(t, db, `SELECT COUNT(*) FROM events`)
	turns := count(t, db, `SELECT COUNT(*) FROM turns`)
	sessions := count(t, db, `SELECT COUNT(*) FROM sessions`)
	fileChanges := count(t, db, `SELECT COUNT(*) FROM file_changes`)
	if events == 0 || turns == 0 || sessions != 3 {
		t.Fatalf("first backfill too empty: %d events, %d turns, %d sessions", events, turns, sessions)
	}

	// Force full re-reads: wipe the offsets, as a fresh machine would replay.
	if _, err := db.Exec(`DELETE FROM file_offsets`); err != nil {
		t.Fatal(err)
	}
	p2 := NewPipeline(db, Config{ProjectsRoot: root}, nil)
	p2.Backfill(context.Background())
	p2.Backfill(context.Background())

	for name, want := range map[string]int{
		"events": events, "turns": turns, "sessions": sessions, "file_changes": fileChanges,
	} {
		if got := count(t, db, `SELECT COUNT(*) FROM `+name); got != want {
			t.Errorf("%s after re-backfill = %d, want %d (zero duplicates)", name, got, want)
		}
	}
	if m := p2.Metrics(); m.Events != 0 {
		t.Errorf("re-backfill created %d events, want 0", m.Events)
	}
}

// TestCorruptFileDoesNotStopScanner: a binary-garbage file and an unreadable
// file must be skipped/counted while every healthy file is still ingested.
func TestCorruptFileDoesNotStopScanner(t *testing.T) {
	db := testDB(t)
	root := fixtureRoot(t)
	slug := filepath.Join(root, "-tmp-fixture-proj")

	// aa- prefix sorts BEFORE the healthy files — the scanner hits them first.
	mustWrite(t, filepath.Join(slug, "aa-corrupt.jsonl"), "\x00\x01garbage not json\nstill not json {{{\n")
	unreadable := filepath.Join(slug, "aa-unreadable.jsonl")
	mustWrite(t, unreadable, line("dddddddd-0000-4000-8000-000000000001", "2026-07-12T10:00:00.000Z", "hidden"))
	if err := os.Chmod(unreadable, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(unreadable, 0o644) })

	p := NewPipeline(db, Config{ProjectsRoot: root}, nil)
	m := p.Backfill(context.Background())

	if got := count(t, db, `SELECT COUNT(*) FROM sessions`); got != 3 {
		t.Errorf("sessions = %d, want 3 (healthy files must still ingest)", got)
	}
	if m.Errors != 1 {
		t.Errorf("errors = %d, want 1 (the unreadable file)", m.Errors)
	}
	if m.SkippedLines != 2 {
		t.Errorf("skipped lines = %d, want 2 (the garbage file)", m.SkippedLines)
	}
	// The garbage file's offset advanced past the junk: rescans won't loop on it.
	var off int64
	if err := db.QueryRow(`SELECT byte_offset FROM file_offsets WHERE file_path = ?`,
		filepath.Join(slug, "aa-corrupt.jsonl")).Scan(&off); err != nil {
		t.Fatalf("offset row for corrupt file: %v", err)
	}
	if off == 0 {
		t.Errorf("corrupt file offset = 0, want advanced past the junk")
	}
}

// TestStatusRecompute: the ticker moves sessions forward through
// active → idle → completed based on last-record age, and never backwards.
func TestStatusRecompute(t *testing.T) {
	db := testDB(t)
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	insert := func(uuid, status string, age time.Duration) int64 {
		res, err := db.Exec(
			`INSERT INTO projects (path, slug, first_seen) VALUES (?, ?, ?)
			 ON CONFLICT(path) DO NOTHING`, "/tmp/s-proj", "-tmp-s-proj", "2026-07-12T00:00:00Z")
		if err != nil {
			t.Fatal(err)
		}
		_ = res
		var pid int64
		if err := db.QueryRow(`SELECT id FROM projects WHERE path='/tmp/s-proj'`).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		r, err := db.Exec(
			`INSERT INTO sessions (project_id, session_uuid, status, started_at, ended_at)
			 VALUES (?, ?, ?, ?, ?)`,
			pid, uuid, status, now.Add(-2*time.Hour).Format(time.RFC3339),
			now.Add(-age).UTC().Format("2006-01-02T15:04:05.000Z"))
		if err != nil {
			t.Fatal(err)
		}
		id, _ := r.LastInsertId()
		return id
	}

	fresh := insert("s-fresh", "active", 30*time.Second)    // stays active
	toIdle := insert("s-idle", "active", 10*time.Minute)    // active → idle
	toDone := insert("s-done", "idle", 45*time.Minute)      // idle → completed
	done := insert("s-already", "completed", 5*time.Second) // completed is terminal for the ticker

	changed, err := RecomputeStatuses(db, Thresholds{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 2 {
		t.Errorf("changed = %v, want exactly 2 transitions", changed)
	}
	wantStatus := map[int64]string{fresh: "active", toIdle: "idle", toDone: "completed", done: "completed"}
	for id, want := range wantStatus {
		var got string
		if err := db.QueryRow(`SELECT status FROM sessions WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("session %d status = %q, want %q", id, got, want)
		}
	}
	// Vocabulary guard: only the three MVP statuses may ever appear.
	if got := count(t, db,
		`SELECT COUNT(*) FROM sessions WHERE status NOT IN ('active','idle','completed')`); got != 0 {
		t.Errorf("forbidden statuses present: %d", got)
	}
}

// TestBusFanout: subscribers receive published notifications; a full buffer
// drops instead of blocking; cancel unsubscribes.
func TestBusFanout(t *testing.T) {
	bus := NewBus()
	ch, cancel := bus.Subscribe(1)
	bus.Publish(Notification{Type: NoteSessionStarted, SessionID: 1})
	bus.Publish(Notification{Type: NoteEventAppended, SessionID: 1, EventID: 9}) // dropped: buffer full

	got := <-ch
	if got.Type != NoteSessionStarted || got.SessionID != 1 {
		t.Errorf("got %+v", got)
	}
	select {
	case n := <-ch:
		t.Errorf("expected drop, got %+v", n)
	default:
	}
	cancel()
	bus.Publish(Notification{Type: NoteSessionUpdated, SessionID: 2}) // must not panic
	if _, ok := <-ch; ok {
		t.Error("channel should be closed after cancel")
	}
}

// TestPipelineLiveTail: end-to-end Run() against a temp root — appended lines
// must be picked up (via fsnotify or the rescan net) and emitted on the bus.
func TestPipelineLiveTail(t *testing.T) {
	db := testDB(t)
	root := t.TempDir()
	path := filepath.Join(root, "-tmp-live-proj", "session.jsonl")
	mustWrite(t, path, line("eeeeeeee-0000-4000-8000-000000000001", "2026-07-12T10:00:00.000Z", "hello"))

	bus := NewBus()
	ch, cancel := bus.Subscribe(64)
	defer cancel()

	p := NewPipeline(db, Config{ProjectsRoot: root, RescanInterval: 200 * time.Millisecond}, bus)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go p.Run(ctx)

	// Backfill result: session_started for the pre-existing file.
	waitFor(t, ch, NoteSessionStarted, 5*time.Second)

	mustAppend(t, path, line("eeeeeeee-0000-4000-8000-000000000002", "2026-07-12T10:00:05.000Z", "again"))
	start := time.Now()
	waitFor(t, ch, NoteEventAppended, 5*time.Second)
	if lag := time.Since(start); lag > 3*time.Second {
		t.Errorf("live pickup lag = %s, want < 3s", lag)
	}
}

func waitFor(t *testing.T, ch <-chan Notification, typ string, timeout time.Duration) Notification {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case n := <-ch:
			if n.Type == typ {
				return n
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s notification", typ)
		}
	}
}
