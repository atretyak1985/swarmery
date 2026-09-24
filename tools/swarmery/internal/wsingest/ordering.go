// Ordering check for a POSTERIOR forecast — "was this still a prediction when
// it was written?", answered from the run TRANSCRIPT rather than from the doc.
//
// WHY NOT FROM THE DOC. wsingest keeps a content hash and an mtime per artifact
// and nothing else, so a phase doc can say that it carries a posterior but never
// when the block appeared in it. `written_at` inside the block is agent-reported:
// useful as a fallback and worthless as evidence, since the agent that wrote the
// forecast late is the one that would date it early. The transcript is the only
// independent record of the order in which the run did things.
//
// THE CHECK. Inside the phase's run session (`epic_phases.run_session_uuid`):
//
//   - POSTERIOR AT — the earliest Edit/Write on the PHASE DOC whose patch adds a
//     line containing `kind: posterior`;
//   - FIRST OTHER CHANGE — the earliest file change on any OTHER path.
//
// Posterior later than first other change → the forecast was written after the
// run had already started producing work, so it is a report, not a prediction,
// and calibration must not score it. Earlier (or the only change) → a genuine
// prediction. NO EVIDENCE IS NOT EVIDENCE: a phase with no run session, a
// session not ingested yet, or a posterior the transcript does not show is left
// UNFLAGGED. The false positive here would silently delete a real prediction
// from the learning loop's training set, which is strictly worse than scoring an
// occasional late one.
//
// SUBAGENT SESSIONS ARE INCLUDED for free: ingest stores a sidechain's records
// under the PARENT session_id (their uuid space restarts per file, so the dedup
// key is prefixed with the agent id instead — internal/ingest, C3). A file
// change made by a dispatched executor is therefore already a row with this
// session_id, and an executor that delegates its edits cannot dodge the check.
//
// MATCHING THE DOC BY BASENAME, not by absolute path, because the phase doc the
// executor edits is usually NOT the workspace file: phaserun LENDS a copy into
// the isolated worktree and copies edits back when the run ends
// (worktree.LendPlanDoc), so the transcript records a worktree path. The
// basename survives that round trip; the directory does not.
package wsingest

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Post-hoc reasons — the values of phase_forecasts.post_hoc_reason. They name
// WHICH observation set post_hoc; the flag itself means the same thing for both:
// this forecast cannot have been a prediction, so calibration skips it.
const (
	// PostHocReportFilled is 0079's producer: a PRIOR in a doc whose
	// `## Completion Report` was already filled (derived by the doc scan).
	PostHocReportFilled = "report-filled"
	// PostHocAfterFirstEdit is this file's producer: a POSTERIOR written to the
	// doc after the run's first change to some other file (derived from the
	// transcript).
	PostHocAfterFirstEdit = "after-first-edit"
)

// posteriorMarker is the text a patch line must contain for the edit to count as
// "this is where the posterior appeared". Matched against the whole stored patch
// rather than parsed, deliberately: the patch is a diff, the block may be split
// across hunks, and a substring of the one key that identifies the kind is a
// cheaper and more robust signal than re-parsing yaml out of a diff.
const posteriorMarker = "kind: posterior"

// DocEditOrdering is the transcript evidence about one phase run: when the
// posterior appeared in the phase doc, and when the run first changed anything
// else. A zero time means "the transcript does not show this".
type DocEditOrdering struct {
	PosteriorAt        time.Time
	FirstOtherChangeAt time.Time
}

// PostHoc reports whether the posterior was written too late to be a prediction.
//
// Pure, and deliberately conservative on both zero values: with no posterior
// edit there is nothing to judge, and with no other change the posterior WAS the
// run's first edit, which is exactly what the contract asks for. Strictly After,
// not !Before: two changes stamped at the same instant are the same turn, and a
// tie must not cost the run its forecast.
func (o DocEditOrdering) PostHoc() bool {
	if o.PosteriorAt.IsZero() || o.FirstOtherChangeAt.IsZero() {
		return false
	}
	return o.PosteriorAt.After(o.FirstOtherChangeAt)
}

// hasPosterior reports whether any of a doc's forecasts is a posterior — the
// only case in which the ordering query is worth running.
func hasPosterior(fs []Forecast) bool {
	for _, f := range fs {
		if f.Kind == ForecastPosterior {
			return true
		}
	}
	return false
}

// rowQueryer is the read surface forecastOrdering needs: satisfied by *sql.DB
// and *sql.Tx alike, so the scan can call it inside its transaction and a test
// can call it without one.
type rowQueryer interface {
	QueryRow(query string, args ...any) *sql.Row
}

// forecastOrdering reads the two instants for one phase's run session.
//
// docPath is the phase doc's path as epic_phases stores it (absolute, in the
// workspace); only its basename is matched — see the package comment. sessionUUID
// is epic_phases.run_session_uuid; "" yields zero evidence without a query.
//
// Errors are returned, never swallowed, but every caller in the scan treats one
// as "no evidence": a forecast is data, and a transient read failure must not
// take a plan's whole ingest down with it.
func forecastOrdering(q rowQueryer, sessionUUID, docPath string) (DocEditOrdering, error) {
	var out DocEditOrdering
	base := docBase(docPath)
	if strings.TrimSpace(sessionUUID) == "" || base == "" {
		return out, nil
	}
	pattern := "%/" + escapeLike(base)

	posteriorAt, err := minChangeTS(q, sessionUUID, `
		SELECT MIN(e.ts) FROM file_changes fc
		  JOIN events   e ON e.id = fc.event_id
		  JOIN sessions s ON s.id = fc.session_id
		 WHERE s.session_uuid = ?
		   AND (fc.file_path = ? OR fc.file_path LIKE ? ESCAPE '\')
		   AND fc.diff LIKE ? ESCAPE '\'`,
		docPath, pattern, "%"+escapeLike(posteriorMarker)+"%")
	if err != nil {
		return out, err
	}
	firstOther, err := minChangeTS(q, sessionUUID, `
		SELECT MIN(e.ts) FROM file_changes fc
		  JOIN events   e ON e.id = fc.event_id
		  JOIN sessions s ON s.id = fc.session_id
		 WHERE s.session_uuid = ?
		   AND fc.file_path <> ?
		   AND fc.file_path NOT LIKE ? ESCAPE '\'`,
		docPath, pattern)
	if err != nil {
		return out, err
	}
	out.PosteriorAt, out.FirstOtherChangeAt = posteriorAt, firstOther
	return out, nil
}

// minChangeTS runs one of the two MIN(ts) queries and parses the result.
//
// An unparseable or NULL timestamp is the zero time and NOT an error: events.ts
// is text the ingest wrote from a transcript, and one malformed row must cost
// this signal rather than the scan.
func minChangeTS(q rowQueryer, sessionUUID, query string, args ...any) (time.Time, error) {
	var ts sql.NullString
	err := q.QueryRow(query, append([]any{sessionUUID}, args...)...).Scan(&ts)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	if !ts.Valid || strings.TrimSpace(ts.String) == "" {
		return time.Time{}, nil
	}
	t, perr := time.Parse(time.RFC3339, strings.TrimSpace(ts.String))
	if perr != nil {
		return time.Time{}, nil
	}
	return t, nil
}

// docBase is the file name of a doc path, for either separator — a plan scanned
// on one platform can be read on another, and filepath.Base only knows the
// local one.
func docBase(p string) string {
	p = strings.TrimSpace(p)
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		p = p[i+1:]
	}
	return p
}

// The LIKE patterns above go through wsingest.go's escapeLike, the package's one
// wildcard-neutralizer. `_` is the metacharacter that actually bites here: it
// matches any single character, so an unescaped `phase-4-line_items.md` would
// also match `phase-4-lineXitems.md` and read another phase's forecast timing.
