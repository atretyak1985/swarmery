package wtjanitor

import (
	"database/sql"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/procwatch"
)

// nonTerminalStatuses are the sessions.status values that mean "this session may
// still be using its checkout". Verified against the live DB (2026-08-05: the
// column set is active | idle | completed | killed).
//
// The list is deliberately of the LIVE states, not the terminal ones, but a new
// status appearing upstream would then read as terminal — so widen this list,
// never the terminal side, when statuses change. An unknown status treated as
// live costs one skipped sweep; treated as dead it costs a deleted worktree.
// awaiting_reply (2026-10-06): an interactive session waiting for the
// operator's typed reply — its process and checkout are very much in use.
var nonTerminalStatuses = []any{"active", "idle", "awaiting_reply"}

// ProcLiveness answers Busy from two independent sources, either of which is
// enough to veto: a live process cwd'd inside the path (procwatch's
// MatchByDir — the same primitive procwatch's own pid-binding heuristic uses),
// and a non-terminal session row whose cwd is that path.
//
// Both are best-effort by design. A process-table read that fails must not make
// a worktree look idle, so an error from MatchByDir falls through to the DB
// check rather than being reported — but a DB error IS returned, because that
// one means the caller cannot know.
type ProcLiveness struct {
	Proc procwatch.Provider
	DB   *sql.DB
}

func (l ProcLiveness) Busy(path string) (bool, error) {
	if l.Proc != nil {
		if pids, err := l.Proc.MatchByDir(path); err == nil && len(pids) > 0 {
			return true, nil
		}
	}
	if l.DB == nil {
		return false, nil
	}
	var n int
	err := l.DB.QueryRow(
		`SELECT COUNT(*) FROM sessions WHERE cwd = ? AND status IN (?`+
			strings.Repeat(", ?", len(nonTerminalStatuses)-1)+`)`,
		append([]any{path}, nonTerminalStatuses...)...).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
