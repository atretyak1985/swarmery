package link

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/turnforecast"
)

// Turn-forecast kinds (mods-swarmery-link phase 10): the mod's prediction of a
// main-loop turn and what its tool.call journal saw. Neither touches
// link_status. Contract: docs/mods-link-protocol.md, Forecast.
const (
	KindTurnForecast = "turn.forecast"
	KindTurnActual   = "turn.actual"
)

// turnForecasts scores turn_forecasts rows with the default weights; the link
// only needs UpsertTx, which never touches Store.DB.
var turnForecasts = &turnforecast.Store{}

// turnHalf is a validated turn.forecast / turn.actual event, ready to upsert.
type turnHalf struct {
	half turnforecast.Half
	seq  int
	raw  []byte
}

// validateTurn checks a turn event's payload; a failure rejects that one event.
func validateTurn(kind string, raw []byte) (*turnHalf, error) {
	switch kind {
	case KindTurnForecast:
		f, err := turnforecast.ParseForecast(raw)
		if err != nil {
			return nil, fmt.Errorf("%s payload: %v", kind, err)
		}
		return &turnHalf{half: turnforecast.HalfForecast, seq: f.TurnSeq, raw: raw}, nil
	case KindTurnActual:
		a, err := turnforecast.ParseActual(raw)
		if err != nil {
			return nil, fmt.Errorf("%s payload: %v", kind, err)
		}
		return &turnHalf{half: turnforecast.HalfActual, seq: a.TurnSeq, raw: raw}, nil
	}
	return nil, nil
}

// applyTurn stores a stored turn event's half inside the batch transaction.
// It returns the turn_forecasts.id when the row carries a score afterwards
// (0 otherwise), so Ingest can publish turn_surprise after commit.
func applyTurn(ctx context.Context, tx *sql.Tx, sid int64, n normalized) (int64, error) {
	if n.turn == nil {
		return 0, nil
	}
	u, err := turnForecasts.UpsertTx(ctx, tx, sid, n.turn.seq, n.turn.half, n.at, n.turn.raw)
	if err != nil {
		return 0, fmt.Errorf("link ingest: %w", err)
	}
	if !u.Scored {
		return 0, nil
	}
	return u.ID, nil
}
