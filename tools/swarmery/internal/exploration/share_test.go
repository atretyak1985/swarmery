package exploration

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

const tsFmt = "2006-01-02T15:04:05.000Z"

// shareFixture plants two projects' tool calls across two in-range local days
// plus one call far outside the window, so the tests can prove day grouping,
// the share math, the project filter and the range bound at once.
func shareFixture(t *testing.T) (*sql.DB, Range) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "exploration.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	at := func(d time.Time) string { return d.UTC().Format(tsFmt) }
	today := at(todayStart.Add(12 * time.Hour))
	day3 := at(todayStart.AddDate(0, 0, -3).Add(12 * time.Hour))
	day20 := at(todayStart.AddDate(0, 0, -20).Add(12 * time.Hour))

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v\n%s", err, q)
		}
	}

	mustExec(`INSERT INTO projects (id, path, slug, name, first_seen) VALUES
		(1, '/work/alpha', '-work-alpha', 'Alpha', ?),
		(2, '/work/beta',  '-work-beta',  'Beta',  ?)`, day20, day20)
	mustExec(`INSERT INTO sessions (id, project_id, session_uuid, status, started_at) VALUES
		(1, 1, 'u1', 'active',    ?),
		(2, 2, 'u2', 'completed', ?)`, day20, day20)

	// alpha today: 3 explore (Read, Grep, `cd … && grep`), 1 run (`go test`),
	// 1 edit, 1 other → 6 calls, share 0.5.
	mustExec(`INSERT INTO events (session_id, ts, type, tool_name, payload, dedup_key) VALUES
		(1, ?, 'tool_call', 'Read',  '{"input":{"file_path":"a.go"}}',                 'a1'),
		(1, ?, 'tool_call', 'Grep',  '{"input":{"pattern":"func"}}',                   'a2'),
		(1, ?, 'tool_call', 'Bash',  '{"input":{"command":"cd tools && grep -rn x"}}', 'a3'),
		(1, ?, 'tool_call', 'Bash',  '{"input":{"command":"go test ./..."}}',          'a4'),
		(1, ?, 'tool_call', 'Edit',  '{"input":{"file_path":"a.go"}}',                 'a5'),
		(1, ?, 'tool_call', 'Task',  '{"input":{}}',                                   'a6')`,
		today, today, today, today, today, today)

	// alpha day3: 1 explore (`cat`), 1 edit → 2 calls, share 0.5.
	mustExec(`INSERT INTO events (session_id, ts, type, tool_name, payload, dedup_key) VALUES
		(1, ?, 'tool_call', 'Bash',  '{"input":{"command":"cat README.md"}}', 'a7'),
		(1, ?, 'tool_call', 'Write', '{"input":{"file_path":"b.go"}}',        'a8')`,
		day3, day3)

	// beta today: 2 runs — in range, but excluded once the filter is applied.
	mustExec(`INSERT INTO events (session_id, ts, type, tool_name, payload, dedup_key) VALUES
		(2, ?, 'tool_call', 'Bash', '{"input":{"command":"npm run build"}}', 'b1'),
		(2, ?, 'tool_call', 'Bash', '{"input":{"command":"git diff"}}',      'b2')`,
		today, today)

	// Outside the window entirely — must not reach any bucket.
	mustExec(`INSERT INTO events (session_id, ts, type, tool_name, payload, dedup_key) VALUES
		(1, ?, 'tool_call', 'Grep', '{"input":{"pattern":"old"}}', 'a9')`, day20)

	// A non-tool_call event on an in-range day — the query must ignore it.
	mustExec(`INSERT INTO events (session_id, ts, type, payload, dedup_key) VALUES
		(1, ?, 'subagent_start', '{"subagent_type":"core:tech-lead"}', 'a10')`, today)

	return db, rangeOf(todayStart.AddDate(0, 0, -3), todayStart)
}

// rangeOf builds the Range the api handler would build from parseRange:
// inclusive local days plus the UTC bounds of [from, to+1).
func rangeOf(from, to time.Time) Range {
	const bound = "2006-01-02T15:04:05"
	r := Range{Start: from.UTC().Format(bound), End: to.AddDate(0, 0, 1).UTC().Format(bound)}
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		r.Days = append(r.Days, d.Format(dayFmt))
	}
	return r
}

func TestShareGroupsByLocalDay(t *testing.T) {
	db, rng := shareFixture(t)

	res, err := Share(db, rng, "", nil)
	if err != nil {
		t.Fatalf("Share: %v", err)
	}
	if len(res.Days) != 4 {
		t.Fatalf("days = %d, want 4 (one row per local day in range)", len(res.Days))
	}

	first, last := res.Days[0], res.Days[3]
	if first.Day != rng.Days[0] || last.Day != rng.Days[3] {
		t.Fatalf("day keys = %q…%q, want %q…%q", first.Day, last.Day, rng.Days[0], rng.Days[3])
	}
	// day3 (oldest bucket): cat + Write.
	if first.Calls != 2 || first.Explore != 1 || first.Edit != 1 || first.Share != 0.5 {
		t.Errorf("oldest day = %+v, want calls=2 explore=1 edit=1 share=0.5", first)
	}
	// The two empty days in between must still be present, as zeros.
	for _, d := range res.Days[1:3] {
		if d.Calls != 0 || d.Share != 0 {
			t.Errorf("empty day %s = %+v, want all zeros", d.Day, d)
		}
	}
	// today: alpha's 6 + beta's 2 runs.
	if last.Calls != 8 || last.Explore != 3 || last.Edit != 1 || last.Run != 3 || last.Other != 1 {
		t.Errorf("today = %+v, want calls=8 explore=3 edit=1 run=3 other=1", last)
	}
	if last.Share != 3.0/8.0 {
		t.Errorf("today share = %v, want %v", last.Share, 3.0/8.0)
	}

	// Range totals are explore/calls over the whole window (4/10), NOT the
	// mean of the daily shares — the day20 row and the subagent_start are out.
	if res.Totals.Calls != 10 || res.Totals.Explore != 4 {
		t.Fatalf("totals = %+v, want calls=10 explore=4", res.Totals)
	}
	if res.Totals.Share != 0.4 {
		t.Errorf("totals share = %v, want 0.4", res.Totals.Share)
	}
}

func TestShareProjectFilter(t *testing.T) {
	db, rng := shareFixture(t)

	res, err := Share(db, rng, ` AND p.slug = ?`, []any{"-work-alpha"})
	if err != nil {
		t.Fatalf("Share: %v", err)
	}
	if res.Totals.Calls != 8 || res.Totals.Explore != 4 {
		t.Fatalf("alpha totals = %+v, want calls=8 explore=4", res.Totals)
	}
	if res.Totals.Share != 0.5 {
		t.Errorf("alpha share = %v, want 0.5", res.Totals.Share)
	}

	// Explore-only ranking, with Bash broken out by the command it ran.
	want := []ToolCount{{"Grep", 1}, {"Read", 1}, {"cat", 1}, {"grep", 1}}
	if len(res.Top) != len(want) {
		t.Fatalf("top = %+v, want %+v", res.Top, want)
	}
	for i, w := range want {
		if res.Top[i] != w {
			t.Errorf("top[%d] = %+v, want %+v", i, res.Top[i], w)
		}
	}
}

func TestShareEmptyRange(t *testing.T) {
	db, _ := shareFixture(t)

	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	// A window with no events at all: days -10..-8.
	res, err := Share(db, rangeOf(todayStart.AddDate(0, 0, -10), todayStart.AddDate(0, 0, -8)), "", nil)
	if err != nil {
		t.Fatalf("Share: %v", err)
	}
	if len(res.Days) != 3 {
		t.Fatalf("days = %d, want 3", len(res.Days))
	}
	for _, d := range res.Days {
		if d.Calls != 0 || d.Explore != 0 || d.Share != 0 {
			t.Errorf("day %s = %+v, want zeros", d.Day, d)
		}
	}
	if res.Totals != (Totals{}) {
		t.Errorf("totals = %+v, want zero value", res.Totals)
	}
	// Non-nil so the endpoint serves [] rather than null.
	if res.Top == nil || len(res.Top) != 0 {
		t.Errorf("top = %v, want an empty non-nil slice", res.Top)
	}
}
