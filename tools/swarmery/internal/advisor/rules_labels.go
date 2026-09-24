package advisor

import "database/sql"

// sessionLabels reads a session's D2 labels (session_labels, migration 0083,
// written by internal/decide in active mode) for use as finding evidence.
//
// Read WHEN PRESENT, never required: nil when the session is unlabelled, when
// every label is `unknown`, or when the read fails — a rule's finding must not
// depend on an advisory classifier having run. Queried here rather than through
// internal/decide so the advisor stays a pure SQL rule engine.
func sessionLabels(db *sql.DB, uuid string) map[string]string {
	var taskType, outcome, cause string
	err := db.QueryRow(`SELECT task_type, outcome, failure_cause FROM session_labels WHERE session_uuid=?`, uuid).
		Scan(&taskType, &outcome, &cause)
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for k, v := range map[string]string{"task_type": taskType, "outcome": outcome, "failure_cause": cause} {
		if v != "" && v != "unknown" {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
