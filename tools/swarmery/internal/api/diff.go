package api

// phase 4: system — the canonical Myers unified diff lives in
// internal/textdiff since step-08 (the Stage 2 write base reuses it for 409
// conflict bodies without importing the HTTP layer). This delegate keeps the
// api-local name used by the version-diff endpoints.

import "github.com/atretyak1985/swarmery/tools/swarmery/internal/textdiff"

// UnifiedDiff renders a unified diff (3 lines of context) turning aText into
// bText; "" when identical. Canonical implementation: internal/textdiff.
func UnifiedDiff(aName, bName, aText, bText string) string {
	return textdiff.UnifiedDiff(aName, bName, aText, bText)
}
