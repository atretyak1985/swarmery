package claudeproj

import (
	"math"
	"strings"
	"testing"
)

// TestSlug pins the encoder that the auto-memory readers and the worktree
// helpers all route through.
//
// The authority for every row below is the shipped Claude Code binary, not the
// set of directory names that happen to exist: `strings` over
// ~/.local/share/claude/versions/2.1.278 recovers
// `function k(e){return e.replace(/[^a-zA-Z0-9]/g,"-")}` — see the package doc
// for the re-derivation command and the six near-miss hits it excludes. Rows
// that a real directory ALSO corroborates carry that path in the case name, so
// the counter-evidence shows up in the failure line; rows the estate cannot
// reach are marked EXTRAPOLATED.
//
// The whole file is hermetic — no filesystem, no $HOME — so it goes red on a
// machine that has never run Claude Code. internal/worktree's
// TestProjectSlugMatchesRealClaudeProjectDirs is the complementary check that
// can only run where Claude Code has.
func TestSlug(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{"repo checkout", "/Volumes/Work/swarmery", "-Volumes-Work-swarmery"},
		{"dot-directory doubles the dash", "/Users/dev/.local/src/acme", "-Users-dev--local-src-acme"},
		{
			"daemon worktree of a plan run",
			"/Users/dev/.swarmery/worktrees/-Volumes-Work-swarmery/plan-530",
			"-Users-dev--swarmery-worktrees--Volumes-Work-swarmery-plan-530",
		},
		{"hyphen survives", "/Volumes/Work/english-grammar", "-Volumes-Work-english-grammar"},
		{"digits survive", "/Volumes/Work/repo2/v3", "-Volumes-Work-repo2-v3"},
		{"uppercase survives — there is no case folding", "/Users/Dev/SRC/AcmeApp", "-Users-Dev-SRC-AcmeApp"},
		{"root", "/", "-"},
		{"trailing separator is cleaned away", "/Volumes/Work/swarmery/", "-Volumes-Work-swarmery"},
		{"empty stays empty", "", ""},
		{"last segment is non-alphanumeric — Clean runs before the map", "/Users/dev/src/.env", "-Users-dev-src--env"},

		// Rewritten, and corroborated by a directory that exists on the probe
		// machine. These three are what retracted the old rows that copied the
		// character straight into the slug.
		{"underscore is rewritten (real: ~/projects/am/am_dbt)", "/Users/dev/projects/am/am_dbt", "-Users-dev-projects-am-am-dbt"},
		{"two underscores (real: ~/projects/am/mk_lookup_new)", "/Users/dev/projects/am/mk_lookup_new", "-Users-dev-projects-am-mk-lookup-new"},
		{
			"plus is rewritten — Claude Code's own worktree dir (real)",
			"/Users/dev/src/php/.claude/worktrees/feature+PRPT-8373-to-master",
			"-Users-dev-src-php--claude-worktrees-feature-PRPT-8373-to-master",
		},

		// Rewritten, seen in a recorded cwd but never in a directory name: a
		// Next.js dynamic-route segment. This is the single input on which the
		// old enumerated rule and the binary's rule disagree, and nothing on
		// disk decides it — the binary does.
		{"bracket segment of a dynamic route", "/Users/dev/src/app/api/missions/[id]", "-Users-dev-src-app-api-missions--id-"},

		// EXTRAPOLATED: read out of the binary's character class, unexercised by
		// this estate (no recorded cwd contains a space or a non-ASCII byte).
		{"space is rewritten (EXTRAPOLATED)", "/Users/dev/My Projects/acme", "-Users-dev-My-Projects-acme"},
		{
			// Written as a Repeat rather than a dash literal so the UNIT is
			// legible: 13 runes of Cyrillic become 13 dashes because the Go
			// substitution is rune-wise. Anyone changing this must say which
			// unit they mean.
			"non-ASCII is rewritten rune-wise (EXTRAPOLATED)",
			"/Users/dev/проєкти/акме",
			"-Users-dev" + strings.Repeat("-", 13),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Slug(tc.path); got != tc.want {
				t.Fatalf("Slug(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

// TestSlugUnitDivergenceAboveBMP records the one input class where this port
// knowingly differs from the binary, so the difference is a decision with a
// test behind it rather than an accident.
//
// The substitution here is rune-wise (strings.Map); the binary's regex is
// UTF-16-code-unit-wise. For an astral character that is one rune and two code
// units, so Go emits ONE dash where node emits TWO: node's `ok()` for the input
// below yields "-Users-dev----x". Nothing on the BMP is affected, and no
// recorded cwd on the probe machine contains an astral character.
func TestSlugUnitDivergenceAboveBMP(t *testing.T) {
	const path = "/Users/dev/\U0001F600/x"
	const want = "-Users-dev---x" // node: "-Users-dev----x"
	if got := Slug(path); got != want {
		t.Fatalf("Slug(%q) = %q, want %q (rune-wise; the binary is UTF-16-code-unit-wise here)", path, got, want)
	}
}

// TestSlugCapAndHash pins the 200-character cap and the int32 hash suffix.
//
// This estate CANNOT validate the cap: the longest real project-directory name
// under either config dir is 120 characters, so no directory exercises the
// truncation branch. A cross-engine vector is therefore the only check
// available, and the golden below was produced by running the binary's own
// `ok()` under node:
//
//	node -e 'const A=200;function H(e){let r=0;for(let n=0;n<e.length;n++)r=(r<<5)-r+e.charCodeAt(n)|0;return r}function k(e){return e.replace(/[^a-zA-Z0-9]/g,"-")}function ok(e){let n=k(e);if(n.length<=A)return n;return `${n.slice(0,A)}-${Math.abs(H(e)).toString(36)}`}console.log(ok("/Users/nazarsalo/projects/"+"averylongsegment/".repeat(14)+"leaf"))'
//
// If this test and node ever disagree, node is right and this port is wrong.
func TestSlugCapAndHash(t *testing.T) {
	t.Run("268-char vector cross-checked against node", func(t *testing.T) {
		in := "/Users/nazarsalo/projects/" + strings.Repeat("averylongsegment/", 14) + "leaf"
		if len(in) != 268 {
			t.Fatalf("vector length drifted: %d, want 268", len(in))
		}
		const want = "-Users-nazarsalo-projects-averylongsegment-averylongsegment-averylongsegment-" +
			"averylongsegment-averylongsegment-averylongsegment-averylongsegment-" +
			"averylongsegment-averylongsegment-averylongsegment-aver-g5rrtj"
		got := Slug(in)
		if got != want {
			t.Fatalf("Slug(268-char vector) = %q, want %q", got, want)
		}
		if head, _, _ := strings.Cut(got, "-g5rrtj"); len(head) != maxSlugLen {
			t.Fatalf("truncated head is %d characters, want %d", len(head), maxSlugLen)
		}
	})

	t.Run("exactly 200 characters carries no suffix", func(t *testing.T) {
		in := "/" + strings.Repeat("a", 199)
		want := "-" + strings.Repeat("a", 199)
		got := Slug(in)
		if got != want {
			t.Fatalf("Slug(200-char boundary) = %q, want the unsuffixed %q", got, want)
		}
		if len(got) != maxSlugLen {
			t.Fatalf("boundary slug is %d characters, want exactly %d", len(got), maxSlugLen)
		}
	})

	t.Run("201 characters is the first suffixed one", func(t *testing.T) {
		// node: ok("/" + "a".repeat(200)) ends "-b6ymvl", hash -676821585.
		got := Slug("/" + strings.Repeat("a", 200))
		want := "-" + strings.Repeat("a", 199) + "-b6ymvl"
		if got != want {
			t.Fatalf("Slug(201-char input) = %q, want %q", got, want)
		}
	})

	t.Run("jsStringHash wraps like JS |0", func(t *testing.T) {
		// Goldens from the same node function above. The first two are
		// negative, i.e. the int32 accumulator has already wrapped; a uint32 or
		// int64 accumulator produces different values for both.
		for _, tc := range []struct {
			in   string
			want int32
		}{
			{"/Users/dev/src/acme", -60604811},
			{"/Users/nazarsalo/projects/ae/deployment/src/php", -1037571435},
			{"a", 97},
			{"/", 47},
			{"проєкти", 469631769},
			{"", 0},
		} {
			if got := jsStringHash(tc.in); got != tc.want {
				t.Errorf("jsStringHash(%q) = %d, want %d", tc.in, got, tc.want)
			}
		}
	})

	t.Run("absBase36 survives MinInt32", func(t *testing.T) {
		// JS `Math.abs(-2147483648)` is 2147483648 → "zik0zk". Negating in
		// int32 would wrap back to MinInt32 and emit "-zik0zk", a suffix no
		// real directory can carry.
		if got := absBase36(math.MinInt32); got != "zik0zk" {
			t.Fatalf("absBase36(MinInt32) = %q, want %q", got, "zik0zk")
		}
		if got := absBase36(math.MaxInt32); got != "zik0zj" {
			t.Fatalf("absBase36(MaxInt32) = %q, want %q", got, "zik0zj")
		}
		if got := absBase36(0); got != "0" {
			t.Fatalf("absBase36(0) = %q, want %q", got, "0")
		}
	})
}

// TestSlugIsNotInvertible pins the many-to-one property ON PURPOSE.
//
// The encoder collapses '_', '+', '.' and '-' onto one character, so two
// distinct project paths can name one directory. That is Claude Code's own
// behaviour — it would file both projects in the same directory too — and
// modelling it faithfully is the correct thing for a LOOKUP key to do.
func TestSlugIsNotInvertible(t *testing.T) {
	for _, pair := range [][2]string{
		{"/p/am_dbt", "/p/am-dbt"},
		{"/p/feature+x", "/p/feature-x"},
		{"/p/a.b", "/p/a-b"},
	} {
		a, b := Slug(pair[0]), Slug(pair[1])
		if a != b {
			t.Fatalf("Slug(%q) = %q and Slug(%q) = %q — the encoder became INJECTIVE. "+
				"If that is intended, the plan's worktree-to-source mapping must be revisited: "+
				"it maps back through the worktree's own .git gitdir line precisely BECAUSE a "+
				"slug cannot be inverted to a path.", pair[0], a, pair[1], b)
		}
	}
}
