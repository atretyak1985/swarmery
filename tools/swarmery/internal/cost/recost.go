package cost

import (
	"database/sql"
	"fmt"
)

// RecostStats reports what one Recost pass did.
type RecostStats struct {
	Total    int // turns examined
	Priced   int // turns with a computed cost_usd
	Unpriced int // turns with usage but no price (unknown model) → cost_usd NULL
	NoUsage  int // turns with no usage at all (user turns) → cost_usd NULL
}

// Recost recomputes turns.cost_usd for every turn from its stored token usage
// and the given pricing table. Idempotent: it fully overwrites cost_usd from
// current DB state, so re-running (or running after a pricing.json change)
// always converges to the same values.
//
// Model resolution: turns.model (the exact per-message API model, written by
// ingest since migration 0002) with a fallback to sessions.model for rows
// ingested before the column existed.
func Recost(db *sql.DB, t *Table) (RecostStats, error) {
	var stats RecostStats

	type row struct {
		id              int64
		model           string
		in, out, cr, cw sql.NullInt64
	}

	// Buffer the rows first: the store uses a single-connection pool, so we
	// cannot UPDATE while the SELECT cursor is still open.
	rows, err := db.Query(`
		SELECT t.id, COALESCE(t.model, s.model, ''),
		       t.tokens_in, t.tokens_out, t.tokens_cache_read, t.tokens_cache_write
		FROM turns t JOIN sessions s ON s.id = t.session_id`)
	if err != nil {
		return stats, fmt.Errorf("recost: select turns: %w", err)
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.model, &r.in, &r.out, &r.cr, &r.cw); err != nil {
			rows.Close()
			return stats, err
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return stats, err
	}

	tx, err := db.Begin()
	if err != nil {
		return stats, err
	}
	defer tx.Rollback()

	for _, r := range all {
		stats.Total++
		turn := Turn{Model: r.model}
		hasUsage := false
		for _, f := range []struct {
			src sql.NullInt64
			dst **int64
		}{
			{r.in, &turn.TokensIn}, {r.out, &turn.TokensOut},
			{r.cr, &turn.TokensCacheRead}, {r.cw, &turn.TokensCacheWrite},
		} {
			if f.src.Valid {
				v := f.src.Int64
				*f.dst = &v
				hasUsage = true
			}
		}

		c := t.EnrichTurn(turn)
		switch {
		case c != nil:
			stats.Priced++
		case hasUsage:
			stats.Unpriced++
		default:
			stats.NoUsage++
		}

		var val any
		if c != nil {
			val = *c
		}
		if _, err := tx.Exec(`UPDATE turns SET cost_usd = ? WHERE id = ?`, val, r.id); err != nil {
			return stats, fmt.Errorf("recost: update turn %d: %w", r.id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return stats, err
	}
	return stats, nil
}
