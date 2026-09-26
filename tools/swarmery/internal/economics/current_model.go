package economics

import (
	"database/sql"
	"fmt"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/modelid"
)

// CurrentModelWindowDays is how far back "what are we actually running on?"
// looks. Thirty days spans a model cutover without being decided by it: a week
// would flip the answer the day a new model lands and before any of the
// evidence a model-upgrade routine needs exists, and a quarter would keep
// naming the previous generation for weeks after the fleet moved.
const CurrentModelWindowDays = 30

// CurrentModel returns the model id the fleet has actually run the most
// assistant turns on inside the window, with its turn count.
//
// It exists because config/routines/model-upgrade.json has been calling
// `swarmery economics --current-model` — a flag that did not exist — since it
// was written. The call was wrapped in `2>/dev/null || …`, so the routine
// silently substituted its fallback on every single run and the operator saw a
// working routine that had never once read the fleet's real model.
//
// TURNS, not sessions and not cost. A session's `model` column is only its
// FIRST model (see internal/api/session_model.go), cost would let one expensive
// model outvote the one doing the work, and turns is the grain that answers
// "which model is this fleet's output actually coming from".
//
// The context-window marker is stripped (modelid.Base), so `claude-opus-5-5[1m]`
// and `claude-opus-5-5` are one answer rather than two halves of a split vote.
// Returns ok=false when the window holds no priced-or-unpriced assistant turn
// at all — a fresh database, and a case the caller must not print an empty
// string for.
func CurrentModel(db *sql.DB) (model string, turns int, ok bool, err error) {
	rows, err := db.Query(fmt.Sprintf(`
		SELECT model, COUNT(*) AS n
		  FROM turns
		 WHERE role = 'assistant' AND model IS NOT NULL AND model <> ''
		   AND started_at >= datetime('now', '-%d days')
		 GROUP BY model`, CurrentModelWindowDays))
	if err != nil {
		return "", 0, false, fmt.Errorf("economics: current model: %w", err)
	}
	defer rows.Close()

	// Folded in Go rather than in SQL because the fold is modelid.Base, and
	// reimplementing it as a SQL expression is how the two definitions of "the
	// same model" drift apart.
	counts := map[string]int{}
	for rows.Next() {
		var raw string
		var n int
		if err := rows.Scan(&raw, &n); err != nil {
			return "", 0, false, fmt.Errorf("economics: current model scan: %w", err)
		}
		counts[modelid.Base(raw)] += n
	}
	if err := rows.Err(); err != nil {
		return "", 0, false, fmt.Errorf("economics: current model: %w", err)
	}
	for m, n := range counts {
		// Ties break on the id so the answer is stable across runs; a routine
		// that reports a different "current model" on every invocation of an
		// unchanged database is worse than no answer.
		if n > turns || (n == turns && m < model) {
			model, turns = m, n
		}
	}
	return model, turns, model != "", nil
}
