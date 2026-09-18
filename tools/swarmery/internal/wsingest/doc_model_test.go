package wsingest

import "testing"

// TestParseModel covers the `**Model:**` header's whole contract. The two halves
// that matter most are the LAST two cases and the "unknown value" one:
//
//   - the header-block bound, because every phase doc in this workspace embeds a
//     copy-paste agent prompt that itself talks about models, and a `**Model:**`
//     line quoted inside one describes someone else's phase;
//   - an unrecognized value is returned VERBATIM rather than folded away, unlike
//     ParseDocVerify's fallback to `off`. The scan must never fail, but the RUN
//     must — so the parser reports what the author wrote and internal/phaserun is
//     the single place that judges it. Folding it to "" here would silently drop
//     the declaration, which is the bug internal/dispatch/service.go:979 records.
func TestParseModel(t *testing.T) {
	for _, tc := range []struct {
		name, doc, want string
	}{
		{"absent", "# Phase 1\n\n**Covers:** SC-1\n", ""},
		{"prose line", "# Phase 1\n\n**Model:** opus\n", "opus"},
		{"beside Covers", "# Phase 1\n\n**Covers:** SC-1\n**Model:** sonnet\n", "sonnet"},
		{"table row", "# Phase 1\n\n| **Model** | fable |\n", "fable"},
		{"table row with colon", "# Phase 1\n\n| **Model:** | fable |\n", "fable"},
		{"case-insensitive key", "# Phase 1\n\n**MODEL:** opus\n", "opus"},
		{"full id", "# Phase 1\n\n**Model:** claude-opus-5\n", "claude-opus-5"},
		{"backtick-wrapped value", "# Phase 1\n\n**Model:** `opus`\n", "opus"},
		{"first declaration wins", "# Phase 1\n\n**Model:** opus\n**Model:** sonnet\n", "opus"},
		{"empty value is no opinion", "# Phase 1\n\n**Model:**  \n", ""},
		// Verbatim, NOT normalized away — the run refuses it and names the doc.
		{"unknown value survives the parse", "# Phase 1\n\n**Model:** gpt-9\n", "gpt-9"},
		{
			name: "only the header block counts",
			doc:  "# Phase 1\n\n**Covers:** SC-1\n\n## Copy-paste agent prompt\n\n**Model:** fable\n",
			want: "",
		},
		{
			name: "past the 15-line window",
			doc:  "# Phase 1\n" + "\n\n\n\n\n\n\n\n\n\n\n\n\n\n\n" + "**Model:** opus\n",
			want: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseModel(tc.doc); got != tc.want {
				t.Errorf("ParseModel = %q, want %q", got, tc.want)
			}
		})
	}
}

// doc_model is DOC-owned, so applyEpics re-derives it on every scan instead of
// carrying it across a rename — the same rule verify_mode follows. The retraction
// half is the one worth pinning: deleting the `**Model:**` line from a doc must
// actually withdraw the declaration, or an author could never take one back.
func TestApplyEpics_DocModelIsDocOwnedAndRetractable(t *testing.T) {
	db := carryFixture(t)
	p := phase(1, "Phase 1", "/plan/p1.md")
	p.docModel = "sonnet"
	applyPhases(t, db, []epicPhase{p})

	var model any
	if err := db.QueryRow(`SELECT doc_model FROM epic_phases WHERE doc_path='/plan/p1.md'`).Scan(&model); err != nil {
		t.Fatal(err)
	}
	if got, ok := model.(string); !ok || got != "sonnet" {
		t.Errorf("doc_model = %v, want %q", model, "sonnet")
	}

	// The author deletes the line: the next scan must write NULL back, not keep the
	// stale declaration alive.
	applyPhases(t, db, []epicPhase{phase(1, "Phase 1", "/plan/p1.md")})
	if err := db.QueryRow(`SELECT doc_model FROM epic_phases WHERE doc_path='/plan/p1.md'`).Scan(&model); err != nil {
		t.Fatal(err)
	}
	if model != nil {
		t.Errorf("doc_model = %v after the declaration was removed, want NULL", model)
	}
}
