package wsingest

import "testing"

// ParseEffort is ParseModel's twin, so it is tested against the same traps: the
// header-block bound (every phase doc embeds a copy-paste agent prompt further
// down, and a header line quoted inside one describes someone else's phase),
// both spellings, markdown decoration, and first-declaration-wins.
func TestParseEffort(t *testing.T) {
	for _, tc := range []struct {
		name string
		md   string
		want string
	}{
		{"prose header", "# Phase 1\n\n**Effort:** high\n", "high"},
		{"table row", "# Phase 1\n\n| **Effort** | low |\n", "low"},
		{"table row with colon", "# Phase 1\n\n| **Effort:** | max |\n", "max"},
		{"case insensitive key", "# Phase 1\n\n**effort:** medium\n", "medium"},
		{"backtick decoration", "# Phase 1\n\n**Effort:** `xhigh`\n", "xhigh"},
		{"no declaration", "# Phase 1\n\nJust prose.\n", ""},
		{
			// The bound that matters: a value quoted inside the embedded agent
			// prompt is not this phase's declaration.
			name: "stops at the first section heading",
			md:   "# Phase 1\n\n## Agent prompt\n\n**Effort:** max\n",
			want: "",
		},
		{
			name: "first declaration wins",
			md:   "# Phase 1\n\n**Effort:** low\n**Effort:** max\n",
			want: "low",
		},
		{
			// An author who left the line in with nothing after it stated no
			// opinion. The resolution site must see "" and fall through, not be
			// handed a value it would reject as unknown.
			name: "blank value is no opinion",
			md:   "# Phase 1\n\n**Effort:** ``\n",
			want: "",
		},
		{
			// Verbatim, NOT normalized and NOT rejected here: the scan's
			// contract is "degrade with a warning, never fail" (it runs on a
			// debounce over every plan on the machine), while the resolution
			// site's is the opposite. Both hold only if this parser reports
			// what the author wrote.
			name: "unknown value passes through verbatim",
			md:   "# Phase 1\n\n**Effort:** ludicrous\n",
			want: "ludicrous",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseEffort(tc.md); got != tc.want {
				t.Errorf("ParseEffort = %q, want %q", got, tc.want)
			}
		})
	}
}

// The two header parsers must not read each other's lines.
func TestParseEffort_AndParseModel_AreIndependent(t *testing.T) {
	md := "# Phase 1\n\n**Model:** opus\n**Effort:** low\n"
	if got := ParseModel(md); got != "opus" {
		t.Errorf("ParseModel = %q, want %q", got, "opus")
	}
	if got := ParseEffort(md); got != "low" {
		t.Errorf("ParseEffort = %q, want %q", got, "low")
	}
	if got := ParseEffort("# Phase 1\n\n**Model:** opus\n"); got != "" {
		t.Errorf("ParseEffort read a **Model:** line as %q", got)
	}
}
