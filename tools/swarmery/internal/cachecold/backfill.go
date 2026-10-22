package cachecold

import (
	"database/sql"
	"time"
)

// CompareFactors are the factors the backfill reports side by side, so the
// operator can see how sensitive the mark count is before moving Factor.
var CompareFactors = []float64{0.7, 0.8, 0.9}

// FactorCount is how many turns one factor would mark, and what they cost.
type FactorCount struct {
	Factor  float64
	Marked  int
	CostUSD float64
}

// BackfillResult is one Backfill run.
type BackfillResult struct {
	// Evaluated is the number of main-thread assistant turns in the window.
	Evaluated int
	// Marked is how many of them are cold restarts at the shipped Factor, and
	// MarkedCostUSD their summed cost_usd.
	Marked        int
	MarkedCostUSD float64
	// Changed is how many rows this run actually rewrote — 0 on a re-run.
	Changed int
	// ByFactor is the mark count per CompareFactors entry.
	ByFactor []FactorCount
}

// BackfillTurn is one stored main-thread assistant turn, in session order.
type backfillTurn struct {
	id, sessionID int64
	account       string
	turn          Turn
	cost          float64
	cold          bool
	inWindow      bool
}

// Backfill re-derives turns.cold_restart for every main-thread assistant turn
// that started at or after since, using the same rule ingest applies live
// (IsColdRestart at the account kind's TTL and the shipped Factor). It sets the
// column in BOTH directions, so it is idempotent: a second run changes nothing.
// A turn's predecessor may lie before the window — the whole history of every
// session with a turn in the window is read, only the window is written.
func Backfill(db *sql.DB, since time.Time, cfg Config) (BackfillResult, error) {
	res := BackfillResult{}
	for _, f := range CompareFactors {
		res.ByFactor = append(res.ByFactor, FactorCount{Factor: f})
	}
	cutoff := since.UTC().Format(time.RFC3339)
	rows, err := db.Query(`
		SELECT t.id, t.session_id, COALESCE(s.account, ''), t.started_at, COALESCE(t.ended_at, t.started_at),
		       COALESCE(t.tokens_in, 0), COALESCE(t.tokens_cache_read, 0), COALESCE(t.tokens_cache_write, 0),
		       t.cache_write_5m_tokens, t.cache_write_1h_tokens, COALESCE(t.cost_usd, 0), t.cold_restart,
		       t.started_at >= ?
		FROM turns t
		JOIN sessions s ON s.id = t.session_id
		WHERE t.role = 'assistant' AND t.agent_name IS NULL
		  AND (t.tokens_in IS NOT NULL OR t.tokens_cache_read IS NOT NULL OR t.tokens_cache_write IS NOT NULL)
		  AND t.session_id IN (SELECT DISTINCT session_id FROM turns
		                       WHERE started_at >= ? AND role = 'assistant' AND agent_name IS NULL)
		ORDER BY t.session_id, t.seq`, cutoff, cutoff)
	if err != nil {
		return res, err
	}
	var all []backfillTurn
	for rows.Next() {
		var (
			b              backfillTurn
			started, ended string
			w5, w1h        sql.NullInt64
			cold, inWindow int
		)
		if err := rows.Scan(&b.id, &b.sessionID, &b.account, &started, &ended,
			&b.turn.TokensIn, &b.turn.CacheRead, &b.turn.CacheWrite, &w5, &w1h, &b.cost, &cold, &inWindow); err != nil {
			rows.Close()
			return res, err
		}
		b.turn.StartedAt, b.turn.EndedAt = parseTS(started), parseTS(ended)
		b.turn.Split = w5.Valid || w1h.Valid
		b.turn.CacheWrite5m, b.turn.CacheWrite1h = w5.Int64, w1h.Int64
		b.cold, b.inWindow = cold != 0, inWindow != 0
		all = append(all, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}

	type change struct {
		id   int64
		cold int
	}
	var changes []change
	kinds := map[string]Kind{}
	for i, b := range all {
		if !b.inWindow {
			continue
		}
		res.Evaluated++
		want := false
		if i > 0 && all[i-1].sessionID == b.sessionID {
			k, ok := kinds[b.account]
			if !ok {
				k = cfg.Kind(b.account)
				kinds[b.account] = k
			}
			r := cfg.Rule(k)
			prev := all[i-1].turn
			want = IsColdRestart(prev, b.turn, r)
			for j := range res.ByFactor {
				if IsColdRestart(prev, b.turn, Rule{TTL: r.TTL, Factor: res.ByFactor[j].Factor}) {
					res.ByFactor[j].Marked++
					res.ByFactor[j].CostUSD += b.cost
				}
			}
		}
		if want {
			res.Marked++
			res.MarkedCostUSD += b.cost
		}
		if want != b.cold {
			v := 0
			if want {
				v = 1
			}
			changes = append(changes, change{id: b.id, cold: v})
		}
	}

	if len(changes) == 0 {
		return res, nil
	}
	tx, err := db.Begin()
	if err != nil {
		return res, err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`UPDATE turns SET cold_restart = ? WHERE id = ?`)
	if err != nil {
		return res, err
	}
	defer stmt.Close()
	for _, c := range changes {
		if _, err := stmt.Exec(c.cold, c.id); err != nil {
			return res, err
		}
	}
	if err := tx.Commit(); err != nil {
		return res, err
	}
	res.Changed = len(changes)
	return res, nil
}
