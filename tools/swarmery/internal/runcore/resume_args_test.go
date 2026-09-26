package runcore

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// TestArgsResumeSwapsTheHeadAndKeepsEveryFlag is the guard on the one thing that
// makes a continuation a continuation. Omitting --effort here would NOT inherit a
// cheap default: the CLI's own is xhigh.
func TestArgsResumeSwapsTheHeadAndKeepsEveryFlag(t *testing.T) {
	spec := Spec{
		Prompt:         "carry on",
		SessionUUID:    "uuid-1",
		Resume:         true,
		Model:          "claude-opus-5-5",
		Effort:         "high",
		Agent:          "core:implementation-agent",
		PermissionMode: "acceptEdits",
		SettingsFile:   "/proj/.claude/settings.json",
		SettingSources: "project,local",
	}
	got := strings.Join(Args(spec), " ")

	if !strings.HasPrefix(got, "-r uuid-1 -p carry on") {
		t.Errorf("resume argv head = %q, want `-r <uuid> -p <prompt>`", got)
	}
	if strings.Contains(got, "--session-id") {
		t.Errorf("a resume must not pass --session-id (it would mint a new session): %s", got)
	}
	for _, want := range []string{
		"--model claude-opus-5-5",
		"--effort high",
		"--agent core:implementation-agent",
		"--permission-mode acceptEdits",
		"--settings /proj/.claude/settings.json",
		"--setting-sources project,local",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("resume argv dropped %q: %s", want, got)
		}
	}
}

// TestArgsWithoutResumeIsUnchanged: every existing engine's argv must be
// byte-identical to what it emitted before Resume existed.
func TestArgsWithoutResumeIsUnchanged(t *testing.T) {
	got := Args(Spec{Prompt: "do it", SessionUUID: "uuid-1", Model: "m"})
	want := []string{"-p", "do it", "--session-id", "uuid-1", "--model", "m"}
	if len(got) != len(want) {
		t.Fatalf("argv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv = %v, want %v", got, want)
		}
	}
}

func TestRunEventsRoundTripAndClear(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	RecordRunEvent(db, "phaserun", 7, "uuid-1", EventContinuation, 1, "2 criteria left", "2026-09-23T10:00:00Z")
	RecordRunEvent(db, "phaserun", 7, "uuid-1", EventPartial, 2, "1 of 3 ticked", "2026-09-23T10:05:00Z")
	// A different subject must not bleed into the timeline.
	RecordRunEvent(db, "phaserun", 8, "uuid-2", EventBlocked, 0, "other phase", "2026-09-23T10:06:00Z")
	// Nor must a different engine sharing the same numeric id.
	RecordRunEvent(db, "planrun", 7, "uuid-3", EventBlocked, 0, "other engine", "2026-09-23T10:07:00Z")

	got := RunEvents(db, "phaserun", 7)
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2: %+v", len(got), got)
	}
	if got[0].Kind != EventContinuation || got[0].Attempt != 1 {
		t.Errorf("first event = %+v, want the continuation, oldest first", got[0])
	}
	if got[1].Kind != EventPartial || got[1].Detail != "1 of 3 ticked" {
		t.Errorf("second event = %+v", got[1])
	}

	ClearRunEvents(db, "phaserun", 7)
	if n := len(RunEvents(db, "phaserun", 7)); n != 0 {
		t.Errorf("after clear: %d events, want 0", n)
	}
	if n := len(RunEvents(db, "planrun", 7)); n != 1 {
		t.Errorf("clearing phaserun:7 must not touch planrun:7 (got %d)", n)
	}
}

// TestRunEventsDegradeWithoutADB: every write here is best-effort observability
// and must never panic a run goroutine.
func TestRunEventsDegradeWithoutADB(t *testing.T) {
	RecordRunEvent(nil, "phaserun", 1, "u", EventDone, 0, "", "")
	ClearRunEvents(nil, "phaserun", 1)
	if got := RunEvents(nil, "phaserun", 1); got != nil {
		t.Errorf("RunEvents(nil) = %v, want nil", got)
	}
	if got := LastAssistantText(nil, "u"); got != "" {
		t.Errorf("LastAssistantText(nil) = %q, want empty", got)
	}
}

func TestLastAssistantTextReadsTheNewestTurn(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "turns.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`INSERT INTO projects(id, path, slug, first_seen)
		VALUES(1,'/repo/p','p','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO sessions (project_id, session_uuid, started_at)
		VALUES (1, 'uuid-1', '2026-09-23T10:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	sid, _ := res.LastInsertId()
	for _, row := range []struct {
		seq  int
		role string
		text string
	}{
		{1, "user", "do it"},
		{2, "assistant", "starting"},
		{3, "assistant", "PHASE BLOCKED: the schema is missing"},
	} {
		if _, err := db.Exec(`INSERT INTO turns (session_id, seq, role, text, started_at)
			VALUES (?, ?, ?, ?, '2026-09-23T10:00:00Z')`, sid, row.seq, row.role, row.text); err != nil {
			t.Fatal(err)
		}
	}

	if got := LastAssistantText(db, "uuid-1"); got != "PHASE BLOCKED: the schema is missing" {
		t.Errorf("LastAssistantText = %q, want the newest ASSISTANT turn", got)
	}
	if got := LastAssistantText(db, "uuid-unknown"); got != "" {
		t.Errorf("an un-ingested session must read as no evidence, got %q", got)
	}
}
