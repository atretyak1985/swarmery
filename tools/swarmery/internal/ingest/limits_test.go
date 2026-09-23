package ingest

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const limitText = "You've hit your session limit · resets 1:30am (Europe/Kiev)"

// limitTranscript writes a transcript under <tmp>/.claude-work/projects/<dir>/
// and returns (file, projectsRoot). flagged controls isApiErrorMessage on the
// limit record; the text is identical either way.
func limitTranscript(t *testing.T, session string, flagged bool) (string, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".claude-work", "projects")
	dir := filepath.Join(root, "-p")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	flag := ""
	if flagged {
		flag = `"isApiErrorMessage":true,`
	}
	lines := []string{
		`{"type":"user","uuid":"u-1","timestamp":"2026-09-14T10:00:00Z","sessionId":"` + session + `","cwd":"/p","message":{"role":"user","content":"hello"}}`,
		`{"type":"assistant","uuid":"a-limit-` + session + `",` + flag + `"timestamp":"2026-09-14T10:00:05Z","sessionId":"` + session + `","cwd":"/p","message":{"id":"msg-1","model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"` + limitText + `"}]}}`,
	}
	f := filepath.Join(dir, session+".jsonl")
	if err := os.WriteFile(f, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return f, root
}

func limitRows(t *testing.T, db *sql.DB) []struct{ account, scope, source, session, rec, ts string } {
	t.Helper()
	rows, err := db.Query(`SELECT account, scope, source, session_uuid, record_uuid, observed_at FROM account_limit_hits ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []struct{ account, scope, source, session, rec, ts string }
	for rows.Next() {
		var r struct{ account, scope, source, session, rec, ts string }
		if err := rows.Scan(&r.account, &r.scope, &r.source, &r.session, &r.rec, &r.ts); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// TestLimitHitRecordedOnce: a flagged limit record lands exactly one row with
// the account of the projects root it was found under and the right scope; a
// second ingest of the same file adds nothing.
func TestLimitHitRecordedOnce(t *testing.T) {
	db := testDB(t)
	f, root := limitTranscript(t, "sess-limit", true)
	if _, err := fileFrom(db, f, root); err != nil {
		t.Fatal(err)
	}
	got := limitRows(t, db)
	if len(got) != 1 {
		t.Fatalf("rows = %d, want 1", len(got))
	}
	r := got[0]
	if r.account != "work" || r.scope != "session" || r.source != "transcript" ||
		r.session != "sess-limit" || r.rec != "a-limit-sess-limit" || r.ts != "2026-09-14T10:00:05Z" {
		t.Errorf("row = %+v", r)
	}

	// Re-ingest from scratch (offset reset) — the record_uuid index holds.
	if _, err := db.Exec(`DELETE FROM file_offsets`); err != nil {
		t.Fatal(err)
	}
	if _, err := fileFrom(db, f, root); err != nil {
		t.Fatal(err)
	}
	if n := len(limitRows(t, db)); n != 1 {
		t.Errorf("second ingest: rows = %d, want 1", n)
	}
}

// TestLimitHitNeedsTheFlag: the identical text without isApiErrorMessage — a
// transcript quoting the marker in prose — records nothing.
func TestLimitHitNeedsTheFlag(t *testing.T) {
	db := testDB(t)
	f, root := limitTranscript(t, "sess-quote", false)
	if _, err := fileFrom(db, f, root); err != nil {
		t.Fatal(err)
	}
	if n := len(limitRows(t, db)); n != 0 {
		t.Errorf("unflagged record inserted %d rows, want 0", n)
	}
}

// TestLimitHitNeedsAUUID: a flagged limit record with no uuid records nothing —
// the unique index cannot dedupe an empty uuid, so a re-tail would double it.
func TestLimitHitNeedsAUUID(t *testing.T) {
	db := testDB(t)
	f, root := limitTranscript(t, "sess-nouuid", true)
	b, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	stripped := strings.Replace(string(b), `"uuid":"a-limit-sess-nouuid",`, "", 1)
	if stripped == string(b) {
		t.Fatal("fixture uuid not found")
	}
	if err := os.WriteFile(f, []byte(stripped), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := db.Exec(`DELETE FROM file_offsets`); err != nil {
			t.Fatal(err)
		}
		if _, err := fileFrom(db, f, root); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(limitRows(t, db)); n != 0 {
		t.Errorf("uuid-less record inserted %d rows, want 0", n)
	}
	in := &ingester{originRoot: "/x/.claude/projects"}
	if err := in.recordLimitHit(&record{IsAPIErrorMessage: true,
		Message: []byte(`{"content":"` + limitText + `"}`)}); err != nil {
		t.Errorf("uuid-less record: %v", err)
	}
}

// TestLimitHitNoRootContext: without a projects root the account is unknown,
// so nothing is recorded (a later tail that knows the root records it).
func TestLimitHitNoRootContext(t *testing.T) {
	db := testDB(t)
	f, _ := limitTranscript(t, "sess-noroot", true)
	if _, err := File(db, f); err != nil {
		t.Fatal(err)
	}
	if n := len(limitRows(t, db)); n != 0 {
		t.Errorf("rootless ingest inserted %d rows, want 0", n)
	}
}

func TestLimitHitAPIErrorText(t *testing.T) {
	cases := map[string]string{
		`{"content":"plain string"}`: "plain string",
		`{"content":[{"type":"text","text":"a"},{"type":"thinking","thinking":"x"},{"type":"text","text":"b"}]}`: "a\nb",
		`{"content":42}`: "",
		`not json`:       "",
	}
	for msg, want := range cases {
		r := &record{Message: []byte(msg)}
		if got := apiErrorText(r); got != want {
			t.Errorf("apiErrorText(%s) = %q, want %q", msg, got, want)
		}
	}
	// A flagged record whose text is not a limit shape records nothing.
	in := &ingester{originRoot: "/x/.claude/projects"}
	if err := in.recordLimitHit(&record{IsAPIErrorMessage: true, Message: []byte(`{"content":"Not logged in"}`)}); err != nil {
		t.Errorf("non-limit error record: %v", err)
	}
}
