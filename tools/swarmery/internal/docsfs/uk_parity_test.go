package docsfs

// Guards the Ukrainian doc translations against STRUCTURAL drift from their
// English originals: for every content/uk/<f>.md with an English pair
// content/<f>.md, the two must carry the same number of `## ` and `### `
// headings, fenced blocks and table separator rows; every `<a id="…">` of the
// English file must survive; and every anchor the English docs (and the
// glossary's "Read more →" links) point into <f> must still resolve in the
// translation — through the heading's slug or an explicit ` {#id}` suffix
// (web/src/lib/headingId.ts), since a translated heading slugifies to a
// different id than the English one.
//
// Text drift (an English paragraph rewritten, its translation not) stays a
// human's job; web/README.md ("Translating a doc") says what to do.
//
// It reads the `make copy-docs` snapshot (content/ and content/uk/, both
// gitignored), so like a fresh clone or CI it SKIPS when content/ holds no
// markdown, and passes trivially while content/uk/ holds none.

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const (
	enContentDir = "content"
	ukContentDir = "content/uk"
)

// `### Title` — exactly three hashes (a fourth fails the \s+).
var heading3Re = regexp.MustCompile(`(?m)^###\s+(.+?)\s*$`)

// `#` to `####` headings: every level the docs renderer stamps an id on.
var anyHeadingRe = regexp.MustCompile(`(?m)^#{1,4}\s+(.+?)\s*$`)

// `<a id="x">` — an explicit HTML anchor in the English source.
var htmlAnchorRe = regexp.MustCompile(`<a\s+id="([^"]+)"`)

// `](target#frag)` — a markdown link with a fragment; target may be empty
// (same page) or a path whose basename names another doc.
var fragLinkRe = regexp.MustCompile(`\]\(([^)\s#]*)#([^)\s]+)\)`)

// ` {#forecast}` at the end of a heading — mirrors EXPLICIT_ID in headingId.ts.
var explicitIDRe = regexp.MustCompile(`\s+\{#([A-Za-z0-9_-]+)\}\s*$`)

// headingID mirrors headingId() in web/src/lib/headingId.ts: the explicit
// ` {#id}` when the heading has one, the slug of its text otherwise.
func headingID(raw string) string {
	if m := explicitIDRe.FindStringSubmatch(raw); m != nil {
		return m[1]
	}
	return slugify(raw)
}

// mdNames lists the .md file names directly inside dir (none when it is absent).
func mdNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			out = append(out, e.Name())
		}
	}
	return out
}

// fenceBlocks counts fenced blocks with the same open/close rules as
// fenceStripped: a block closes only on a fence of its own character that is
// at least as long; an unclosed one still counts.
func fenceBlocks(md string) int {
	n := 0
	open := ""
	for _, ln := range strings.Split(md, "\n") {
		m := fenceMarker(strings.TrimSpace(ln))
		switch {
		case open == "" && m != "":
			open = m
			n++
		case open != "" && m != "" && m[0] == open[0] && len(m) >= len(open):
			open = ""
		}
	}
	return n
}

// tableSeparators counts `|---|---|` rows, with the renderer's own test
// (isTableSeparator in web/src/lib/markdown.tsx). Expects fence-stripped input.
func tableSeparators(stripped string) int {
	n := 0
	sepRe := regexp.MustCompile(`^\|?[\s:|-]+\|?$`)
	for _, ln := range strings.Split(stripped, "\n") {
		s := strings.TrimSpace(ln)
		if s != "" && sepRe.MatchString(s) && strings.Contains(s, "-") && strings.Contains(s, "|") {
			n++
		}
	}
	return n
}

// definedIDs is every heading id a doc renders with (fence-stripped input).
func definedIDs(stripped string) map[string]bool {
	ids := map[string]bool{}
	for _, m := range anyHeadingRe.FindAllStringSubmatch(stripped, -1) {
		ids[headingID(m[1])] = true
	}
	return ids
}

// inboundAnchors maps each English doc (by file name) to the fragments that
// other English docs, the doc itself and the glossary link to — the anchors a
// translation must keep resolving. Links to docs outside the snapshot, and
// fragments the English target does not define either, are not its concern.
func inboundAnchors(t *testing.T, enNames []string) map[string]map[string]bool {
	t.Helper()
	byLower := map[string]string{}
	stripped := map[string]string{}
	for _, n := range enNames {
		byLower[strings.ToLower(n)] = n
		stripped[n] = fenceStripped(readAll(t, filepath.Join(enContentDir, n)))
	}
	in := map[string]map[string]bool{}
	add := func(target, frag string) {
		if in[target] == nil {
			in[target] = map[string]bool{}
		}
		in[target][strings.ToLower(frag)] = true
	}
	for _, src := range enNames {
		for _, m := range fragLinkRe.FindAllStringSubmatch(stripped[src], -1) {
			target := src
			if m[1] != "" {
				var ok bool
				if target, ok = byLower[strings.ToLower(filepath.Base(m[1]))]; !ok {
					continue
				}
			}
			add(target, m[2])
		}
	}
	if concepts, ok := byLower[conceptsSlug+".md"]; ok {
		for _, a := range matches(anchorRe, readAll(t, glossaryPath)) {
			add(concepts, a)
		}
	}
	for target, frags := range in {
		ids := definedIDs(stripped[target])
		for f := range frags {
			if !ids[f] {
				delete(frags, f) // already broken in English: not a translation defect
			}
		}
	}
	return in
}

func TestUkParity(t *testing.T) {
	enNames := mdNames(t, enContentDir)
	if len(enNames) == 0 {
		t.Skip("internal/docsfs/content has no .md — run `make copy-docs` to check the translations")
	}
	ukNames := mdNames(t, ukContentDir)
	if len(ukNames) == 0 {
		return // nothing translated yet: nothing can drift
	}
	inbound := inboundAnchors(t, enNames)

	for _, name := range ukNames {
		t.Run(name, func(t *testing.T) {
			if !slices.Contains(enNames, name) {
				t.Fatalf("content/uk/%s has no English pair content/%s — it is never served; "+
					"rename it with its English original or delete it", name, name)
			}
			enRaw := readAll(t, filepath.Join(enContentDir, name))
			ukRaw := readAll(t, filepath.Join(ukContentDir, name))
			en, uk := fenceStripped(enRaw), fenceStripped(ukRaw)

			counts := []struct {
				what   string
				en, uk int
			}{
				{"`## ` headings", len(headingRe.FindAllString(en, -1)), len(headingRe.FindAllString(uk, -1))},
				{"`### ` headings", len(heading3Re.FindAllString(en, -1)), len(heading3Re.FindAllString(uk, -1))},
				{"fenced blocks", fenceBlocks(enRaw), fenceBlocks(ukRaw)},
				{"table separator rows", tableSeparators(en), tableSeparators(uk)},
			}
			for _, c := range counts {
				if c.en != c.uk {
					t.Errorf("%s: English has %d, Ukrainian %d — add or drop the same section/block in the translation",
						c.what, c.en, c.uk)
				}
			}

			for _, m := range htmlAnchorRe.FindAllStringSubmatch(enRaw, -1) {
				if !strings.Contains(ukRaw, m[0]) {
					t.Errorf("English anchor %s is missing from the translation", m[0])
				}
			}

			ids := definedIDs(uk)
			want := map[string]bool{}
			for f := range inbound[name] {
				want[f] = true
			}
			// The translation's own in-page links must land too.
			for _, m := range fragLinkRe.FindAllStringSubmatch(uk, -1) {
				if m[1] == "" {
					want[strings.ToLower(m[2])] = true
				}
			}
			var missing []string
			for f := range want {
				if !ids[f] {
					missing = append(missing, f)
				}
			}
			slices.Sort(missing)
			for _, f := range missing {
				t.Errorf("anchor #%s is linked to but no translated heading renders with it — "+
					"append ` {#%s}` to the heading it belongs to", f, f)
			}
		})
	}
}

// TestContentEmbedsUkSubdir: the `all:content` embed carries content/uk/, at
// the "uk/<file>" path internal/api/docs.go reads a translation from.
func TestContentEmbedsUkSubdir(t *testing.T) {
	c, err := Content()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Stat(c, "uk/.gitkeep"); err != nil {
		t.Errorf("embed lacks uk/.gitkeep: %v", err)
	}
	for _, name := range mdNames(t, ukContentDir) {
		if _, err := fs.ReadFile(c, "uk/"+name); err != nil {
			t.Errorf("embed lacks uk/%s: %v", name, err)
		}
	}
}

// TestUkParityHelpers pins the counting rules on small inputs, so the parity
// test above cannot pass by counting nothing.
func TestUkParityHelpers(t *testing.T) {
	md := "## A\n```\n## not a heading\n|---|\n```\n| a | b |\n|---|:-:|\n---\n### B {#bee}\n#### Як справи\n~~~~\nx\n~~~~\n"
	stripped := fenceStripped(md)
	if got := len(headingRe.FindAllString(stripped, -1)); got != 1 {
		t.Errorf("## headings = %d, want 1", got)
	}
	if got := len(heading3Re.FindAllString(stripped, -1)); got != 1 {
		t.Errorf("### headings = %d, want 1", got)
	}
	if got := fenceBlocks(md); got != 2 {
		t.Errorf("fenced blocks = %d, want 2", got)
	}
	if got := tableSeparators(stripped); got != 1 {
		t.Errorf("table separators = %d, want 1 (the fenced one and the `---` rule do not count)", got)
	}
	ids := definedIDs(stripped)
	for _, id := range []string{"a", "bee", "як-справи"} {
		if !ids[id] {
			t.Errorf("defined ids %v lack %q", ids, id)
		}
	}
	if ids["b"] {
		t.Errorf("an explicit {#bee} must replace the slug, not add to it: %v", ids)
	}
}
