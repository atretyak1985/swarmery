package api

import "database/sql"

// sessionTerminalCols are the four term_* columns sessionSelect appends;
// their order matches sessionTerminalScan.dest().
const sessionTerminalCols = `,
	       s.term_program, s.term_focus_url, s.term_bundle_id, s.term_tty`

// sessionTerminalDTO is the terminal tab that owned a session at SessionStart
// (migration 0068). Fields are individually omitted when empty; the whole
// object is null (sessionDTO.Terminal is nil) when none of the four columns
// were ever set — a daemon-spawned run (`claude -p`), a pre-0068 row, or a
// hook that never reached a live daemon.
type sessionTerminalDTO struct {
	Program  string `json:"program,omitempty"`
	FocusURL string `json:"focusUrl,omitempty"`
	BundleID string `json:"bundleId,omitempty"`
	TTY      string `json:"tty,omitempty"`
}

// sessionTerminalScan holds the sessionTerminalCols tail of a session row, so
// the scan order and the projection order are declared in one file (same
// convention as sessionPlanGroupScan).
type sessionTerminalScan struct {
	program  sql.NullString
	focusURL sql.NullString
	bundleID sql.NullString
	tty      sql.NullString
}

// dest returns the scan targets in sessionTerminalCols order.
func (t *sessionTerminalScan) dest() []any {
	return []any{&t.program, &t.focusURL, &t.bundleID, &t.tty}
}

// dto folds the tail into the DTO, or nil when every column is NULL/empty.
func (t *sessionTerminalScan) dto() *sessionTerminalDTO {
	out := sessionTerminalDTO{
		Program:  t.program.String,
		FocusURL: t.focusURL.String,
		BundleID: t.bundleID.String,
		TTY:      t.tty.String,
	}
	if out.Program == "" && out.FocusURL == "" && out.BundleID == "" && out.TTY == "" {
		return nil
	}
	return &out
}
