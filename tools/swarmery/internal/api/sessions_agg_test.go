package api

import (
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

type sessionAgg struct {
	ID          int64    `json:"id"`
	SessionUUID string   `json:"sessionUuid"`
	Tokens      *int64   `json:"tokens"`
	CostUSD     *float64 `json:"costUsd"`
}

// TestSessionAggregates pins the additive parity fields on /api/sessions:
// tokens = SUM(tokens_in + tokens_out) over the session's (deduped) turns,
// costUsd = SUM(cost_usd) over its priced turns; both null when absent.
func TestSessionAggregates(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "agg.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v\n%s", err, q)
		}
	}

	mustExec(`INSERT INTO projects (id, path, slug, first_seen) VALUES
		(1, '/work/alpha', '-work-alpha', '2026-07-01T00:00:00.000Z')`)
	mustExec(`INSERT INTO sessions (id, project_id, session_uuid, status, started_at) VALUES
		(1, 1, 'ua', 'completed', '2026-07-10T10:00:00.000Z'),
		(2, 1, 'ub', 'completed', '2026-07-10T11:00:00.000Z'),
		(3, 1, 'uc', 'completed', '2026-07-10T12:00:00.000Z')`)
	// ua: priced turn + user turn (no usage) + unpriced usage turn.
	// ub: no turns at all.
	// uc: only unpriced usage turns.
	mustExec(`INSERT INTO turns (session_id, seq, role, started_at, tokens_in, tokens_out, cost_usd) VALUES
		(1, 0, 'user',      '2026-07-10T10:00:01.000Z', NULL, NULL, NULL),
		(1, 1, 'assistant', '2026-07-10T10:00:02.000Z', 100,  50,   0.5),
		(1, 2, 'assistant', '2026-07-10T10:00:03.000Z', 30,   20,   0.25),
		(3, 0, 'assistant', '2026-07-10T12:00:01.000Z', 10,   5,    NULL)`)

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	var sessions []sessionAgg
	getJSON(t, srv.URL+"/api/sessions", &sessions)
	if len(sessions) != 3 {
		t.Fatalf("sessions = %d, want 3", len(sessions))
	}
	byUUID := map[string]sessionAgg{}
	for _, s := range sessions {
		byUUID[s.SessionUUID] = s
	}

	ua := byUUID["ua"]
	if ua.Tokens == nil || *ua.Tokens != 200 { // 0+0 + 100+50 + 30+20
		t.Errorf("ua.tokens = %v, want 200", ua.Tokens)
	}
	if ua.CostUSD == nil || *ua.CostUSD != 0.75 {
		t.Errorf("ua.costUsd = %v, want 0.75", ua.CostUSD)
	}

	ub := byUUID["ub"]
	if ub.Tokens != nil {
		t.Errorf("ub.tokens = %v, want null (no turns)", *ub.Tokens)
	}
	if ub.CostUSD != nil {
		t.Errorf("ub.costUsd = %v, want null (no turns)", *ub.CostUSD)
	}

	uc := byUUID["uc"]
	if uc.Tokens == nil || *uc.Tokens != 15 {
		t.Errorf("uc.tokens = %v, want 15", uc.Tokens)
	}
	if uc.CostUSD != nil {
		t.Errorf("uc.costUsd = %v, want null (no priced turns)", *uc.CostUSD)
	}

	// The detail endpoint carries the same aggregates.
	var detail sessionAgg
	getJSON(t, srv.URL+"/api/sessions/ua", &detail)
	if detail.Tokens == nil || *detail.Tokens != 200 || detail.CostUSD == nil || *detail.CostUSD != 0.75 {
		t.Errorf("detail ua = tokens %v, costUsd %v, want 200 / 0.75", detail.Tokens, detail.CostUSD)
	}
}

// TestSessionAggregatesOnFixture cross-checks the one-JOIN aggregates
// against the per-turn rows of a real ingested transcript fixture.
func TestSessionAggregatesOnFixture(t *testing.T) {
	srv := testServer(t) // ingests testdata/fixtures/subagent-session.jsonl

	var sessions []sessionAgg
	getJSON(t, srv.URL+"/api/sessions", &sessions)
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(sessions))
	}

	var detail struct {
		sessionAgg
		Turns []struct {
			TokensIn  *int64   `json:"tokensIn"`
			TokensOut *int64   `json:"tokensOut"`
			CostUSD   *float64 `json:"costUsd"`
		} `json:"turns"`
	}
	getJSON(t, srv.URL+"/api/sessions/"+sessions[0].SessionUUID, &detail)

	var wantTokens int64
	var wantCost float64
	priced := false
	for _, turn := range detail.Turns {
		if turn.TokensIn != nil {
			wantTokens += *turn.TokensIn
		}
		if turn.TokensOut != nil {
			wantTokens += *turn.TokensOut
		}
		if turn.CostUSD != nil {
			wantCost += *turn.CostUSD
			priced = true
		}
	}

	if sessions[0].Tokens == nil || *sessions[0].Tokens != wantTokens {
		t.Errorf("fixture tokens = %v, want %d (sum over turns)", sessions[0].Tokens, wantTokens)
	}
	if priced {
		if sessions[0].CostUSD == nil || *sessions[0].CostUSD != wantCost {
			t.Errorf("fixture costUsd = %v, want %v (sum over priced turns)", sessions[0].CostUSD, wantCost)
		}
	} else if sessions[0].CostUSD != nil {
		t.Errorf("fixture costUsd = %v, want null (no priced turns)", *sessions[0].CostUSD)
	}
}
