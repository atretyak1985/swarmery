package api

import (
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// linkTS formats a link_seen_at the way internal/link writes it (fixed-width
// UTC, ms).
func linkTS(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// linkFixture seeds one project and four sessions, one per link shape:
//
//	1 — no mod ever reported (every link_* NULL)        → link: null
//	2 — fresh mod report, full usage                    → live: true
//	3 — fresh report whose rate_limits is not JSON      → rateLimits: [], no 500
//	4 — report older than 2×AwaitAfter                  → live: false
func linkFixture(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "link.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	now := time.Now()
	stmts := []struct {
		q    string
		args []any
	}{
		{`INSERT INTO projects (id, path, slug, name, first_seen) VALUES (1, '/work/p', '-work-p', 'P', '2026-10-01T00:00:00Z')`, nil},
		{`INSERT INTO sessions (id, project_id, session_uuid, status, started_at) VALUES
			(1, 1, 'u-nolink', 'active', '2026-10-14T10:00:00.000Z'),
			(2, 1, 'u-live',   'active', '2026-10-14T09:00:00.000Z'),
			(3, 1, 'u-badrl',  'active', '2026-10-14T08:00:00.000Z'),
			(4, 1, 'u-stale',  'idle',   '2026-10-14T07:00:00.000Z')`, nil},
		{`UPDATE sessions SET link_status = 'working', link_seen_at = ?, link_context_pct = 78,
			link_rate_limits = ?, link_cost_usd = 1.25 WHERE id = 2`,
			[]any{linkTS(now.Add(-30 * time.Second)),
				`[{"kind":"five_hour","percentUsed":64,"resetsAt":"2026-10-14T18:00:00.000Z"},` +
					`{"kind":"seven_day","percentUsed":12.5},{"percentUsed":99}]`}},
		{`UPDATE sessions SET link_status = 'waiting', link_seen_at = ?, link_rate_limits = '{not json'
			WHERE id = 3`, []any{linkTS(now.Add(-time.Minute))}},
		{`UPDATE sessions SET link_status = 'working', link_seen_at = ?, link_cost_usd = 0.5
			WHERE id = 4`, []any{linkTS(now.Add(-time.Hour))}},
	}
	for _, s := range stmts {
		if _, err := db.Exec(s.q, s.args...); err != nil {
			t.Fatalf("exec: %v\n%s", err, s.q)
		}
	}
	return db
}

// linkProbe is the slice of a session row this test reads. Link stays raw so
// the test can tell an explicit null from an omitted key.
type linkProbe struct {
	ID   int64           `json:"id"`
	Link json.RawMessage `json:"link"`
}

func decodeLink(t *testing.T, raw json.RawMessage) *sessionLinkDTO {
	t.Helper()
	if raw == nil {
		t.Fatal("link key missing — want an explicit null or an object")
	}
	if string(raw) == "null" {
		return nil
	}
	var d sessionLinkDTO
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("decode link %s: %v", raw, err)
	}
	if d.RateLimits == nil {
		t.Errorf("link.rateLimits = null in %s, want an array", raw)
	}
	return &d
}

// TestSessionLinkDTO pins sessionDTO.link on all three session read paths —
// GET /api/sessions, GET /api/sessions/{id}, and sessionByID (the WS
// session_updated payload) — for null, filled, unparsable rate limits, and a
// stale report.
func TestSessionLinkDTO(t *testing.T) {
	AttachStatusThresholds(ingest.Thresholds{})
	db := linkFixture(t)
	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	var page struct {
		Sessions []linkProbe `json:"sessions"`
	}
	getJSON(t, srv.URL+"/api/sessions", &page)
	list := map[int64]json.RawMessage{}
	for _, s := range page.Sessions {
		list[s.ID] = s.Link
	}

	ws := &Handler{DB: db}
	for id := int64(1); id <= 4; id++ {
		var detail linkProbe
		getJSON(t, srv.URL+"/api/sessions/"+strconv.FormatInt(id, 10), &detail)
		viaWS, err := ws.sessionByID(id)
		if err != nil || viaWS == nil {
			t.Fatalf("sessionByID(%d) = %v, %v", id, viaWS, err)
		}
		wsRaw, _ := json.Marshal(viaWS.Link)
		for path, raw := range map[string]json.RawMessage{"list": list[id], "detail": detail.Link, "ws": wsRaw} {
			got := decodeLink(t, raw)
			checkLink(t, id, path, got)
		}
	}
}

func checkLink(t *testing.T, id int64, path string, got *sessionLinkDTO) {
	t.Helper()
	switch id {
	case 1:
		if got != nil {
			t.Errorf("%s session 1 link = %+v, want null (no mod reported)", path, got)
		}
	case 2:
		if got == nil {
			t.Fatalf("%s session 2 link = null, want filled", path)
		}
		if !got.Live || got.Status == nil || *got.Status != "working" {
			t.Errorf("%s session 2 live=%v status=%v, want live working", path, got.Live, got.Status)
		}
		if got.ContextPct == nil || *got.ContextPct != 78 || got.CostUSD == nil || *got.CostUSD != 1.25 {
			t.Errorf("%s session 2 contextPct=%v costUsd=%v, want 78 / 1.25", path, got.ContextPct, got.CostUSD)
		}
		if len(got.RateLimits) != 2 {
			t.Fatalf("%s session 2 rateLimits = %+v, want 2 (the kind-less entry dropped)", path, got.RateLimits)
		}
		if r := got.RateLimits[0]; r.Kind != "five_hour" || r.PercentUsed != 64 || r.ResetsAt == nil {
			t.Errorf("%s session 2 rateLimits[0] = %+v", path, r)
		}
		if r := got.RateLimits[1]; r.Kind != "seven_day" || r.PercentUsed != 12.5 || r.ResetsAt != nil {
			t.Errorf("%s session 2 rateLimits[1] = %+v, want resetsAt null", path, r)
		}
	case 3:
		if got == nil || len(got.RateLimits) != 0 || !got.Live {
			t.Errorf("%s session 3 link = %+v, want live with rateLimits [] (bad JSON)", path, got)
		}
	case 4:
		if got == nil || got.Live || got.CostUSD == nil {
			t.Errorf("%s session 4 link = %+v, want present but live=false (seen 1h ago > 2×AwaitAfter)", path, got)
		}
	}
}

// TestSessionLinkLiveUsesAttachedThresholds: live follows the daemon's
// configured AwaitAfter (AttachStatusThresholds), not a hard-coded window —
// the same rule ingest.RecomputeStatuses applies.
func TestSessionLinkLiveUsesAttachedThresholds(t *testing.T) {
	t.Cleanup(func() { AttachStatusThresholds(ingest.Thresholds{}) })
	now := time.Now()
	scan := sessionLinkScan{seenAt: sql.NullString{String: linkTS(now.Add(-time.Hour)), Valid: true}}

	AttachStatusThresholds(ingest.Thresholds{})
	if scan.dto(now).Live {
		t.Error("default thresholds: a report 1h old is live, want stale (2×3m)")
	}
	AttachStatusThresholds(ingest.Thresholds{AwaitAfter: time.Hour})
	if !scan.dto(now).Live {
		t.Error("AwaitAfter=1h: a report 1h old is stale, want live (2×1h)")
	}
	if got := ingest.LinkFresh(linkTS(now.Add(-time.Hour)), now, ingest.Thresholds{AwaitAfter: time.Hour}); !got {
		t.Error("ingest.LinkFresh disagrees with link.live")
	}
}

func TestSessionLinkDTOUnparseableSeenAt(t *testing.T) {
	scan := sessionLinkScan{
		status: sql.NullString{String: "working", Valid: true},
		seenAt: sql.NullString{String: "not-a-time", Valid: true},
	}
	got := scan.dto(time.Now())
	if got == nil || got.Live {
		t.Errorf("dto = %+v, want present and live=false for an unparseable link_seen_at", got)
	}
}
