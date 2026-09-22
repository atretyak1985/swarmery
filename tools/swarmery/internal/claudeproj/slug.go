// Package claudeproj names the per-project directories Claude Code keeps under
// `<configDir>/projects`. It is a leaf: standard library only, so every package
// that needs to FIND a project's transcripts or auto-memory can depend on it
// without dragging a dependency graph behind it.
//
// This is deliberately NOT the same encoding as `ingest.SlugForPath`. There are
// two slugs in this codebase and they answer two different questions:
//
//	claudeproj.Slug      — the on-disk directory name Claude Code itself chose.
//	                       Rewrites EVERY character outside [A-Za-z0-9] to '-'.
//	                       Use it to LOCATE files under <configDir>/projects.
//	ingest.SlugForPath   — the daemon's own project identity, stored in the
//	                       projects.slug column and used in URLs. Encodes '/'
//	                       only, on purpose; changing it would rewrite identity.
//
// The two agree only for a path built entirely from [A-Za-z0-9/] — they diverge
// on '.', '_', '+', space, brackets and every non-ASCII character. Do not
// "unify" them; internal/ingest's TestSlugForPathIsTheDBSlugNotTheClaudeDirName
// exists to stop exactly that.
package claudeproj

import (
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf16"
)

// maxSlugLen is the binary's `var A7=200` — the length at which Claude Code
// truncates a slug and appends a hash suffix.
const maxSlugLen = 200

// Slug encodes an absolute path the way Claude Code names the per-project
// directory under `<configDir>/projects`: every character outside [A-Za-z0-9]
// becomes '-'. If the result is longer than 200 characters it is truncated to
// 200 and suffixed with '-' plus the base36 of the absolute value of a
// Java-style int32 string hash of the PRE-substitution input.
//
// There is no special case for '/', '.' or '_': they are three members of one
// replaced class, and '-' maps to '-' by coincidence rather than by exemption.
// The leading slash encodes too, which is why every slug opens with '-', and a
// dot-directory produces a doubled '--' (`<home>/.config/x` → `-…--config-x`).
//
// # Where the rule comes from
//
// It is READ OUT OF THE SHIPPED BINARY, not inferred from directory names.
// `~/.local/share/claude/versions/2.1.278` is a Bun single-file executable and
// the bundled JS is recoverable with `strings`. Re-derive it with (the
// `tr ';' '\n' | grep -F` form carries no regex, which matters — see below):
//
//	B=~/.local/share/claude/versions/2.1.278
//	strings -a -n 20 "$B" | tr ';' '\n' | grep -F 'function k(e){return e.replace(/[^a-zA-Z0-9]/g,"-")}'
//	strings -a -n 20 "$B" | tr ';' '\n' | grep -F 'var A7=200'
//
// The broad literal `grep -cF 'replace(/[^a-zA-Z0-9]/g'` returns SEVEN hits and
// only ONE of them is this encoder, so do not read the first line and stop. The
// other six map the SAME character class to three other replacements: a
// `.toLowerCase()` variant (itself the proof that the project-directory encoder
// does NOT case-fold), a transcript redactor building a `[CWD_SLUG]`
// placeholder, two `mcp-refresh-<x>.lock` names that replace with '_', and one
// that strips the class to the empty string. Keep the count 7 in mind: it is
// what tells a future reader that the binary was re-minified rather than that
// the rule changed. Version 2.1.275 carries the identical shape, and the binary
// holds a second, independent minification of the same function
// (`function O(e){let r=e.replace(/[^a-zA-Z0-9]/g,"-")` … with the same
// cap-then-hash tail), so this is not a recent change.
//
// The relevant JS, de-minified:
//
//	var A7 = 200;
//	function H7(e){ let r=0; for(let n=0;n<e.length;n++) r=(r<<5)-r+e.charCodeAt(n)|0; return r }
//	function Le(e){ return Math.abs(H7(e)).toString(36) }
//	function k(e){ return e.replace(/[^a-zA-Z0-9]/g, "-") }
//	function ok(e){ let n=k(e); if(n.length<=A7) return n; return `${n.slice(0,A7)}-${Le(e)}` }
//
// On grep: the machine this was derived on runs ugrep 7.8.4, and bounded
// repetition is NOT what it refuses — `grep -oE '.{0,320}'` works, and so does
// `grep -oE 'var A7=200;.{0,320}'`. What it rejects with "exceeds complexity
// limits" is a pattern carrying TWO large `.{0,N}` runs, whatever sits between
// them. The portable rule is "never two bounded-any runs in one pattern", and
// the `tr ';' '\n' | grep -F` form above sidesteps the question by carrying no
// regex at all.
//
// # What the estate says
//
// Every directory under `~/.claude/projects` and `~/.claude-insart/projects`,
// re-encoded from every distinct `cwd` AND `relocatedCwd` its transcripts
// record: on 2026-09-21, 91 directories, 67 resolvable, and this rule
// reproduced 67/67 where the old '/'-and-'.' rule reproduced 64/67; re-measured
// 2026-09-22 after three more projects appeared, 94 directories, 70 resolvable,
// 70/70 against 67/70. Observed substitutions: '/' 375, '.' 31, '_' 3, '+' 1.
// Everything else observed is [A-Za-z0-9-] and survives, uppercase included.
//
// Sample evidence alone cannot separate "map / . _ +" from this broad rule: the
// two disagree on exactly one recorded cwd (a Next.js dynamic-route directory
// `…/api/missions/[id]`) and no directory of either name exists. The binary
// settles it, and it settles it broad. internal/worktree's
// TestProjectSlugMatchesRealClaudeProjectDirs is the arbiter that goes red the
// day a real directory disagrees.
//
// Space and non-ASCII are EXTRAPOLATED from the binary and unexercised by this
// estate: no recorded cwd on the probe machine contains either.
//
// # Units
//
// The substitution here is RUNE-wise (strings.Map); the binary's
// `[^a-zA-Z0-9]` is UTF-16-code-unit-wise. They agree over the whole BMP and
// diverge only above U+FFFF, where an astral character is one rune here and two
// code units there. The hash, by contrast, iterates UTF-16 code units, because
// `charCodeAt` is what the binary iterates. The 200 is applied to bytes, which
// equals JS's `slice(0,200)` over UTF-16 code units ONLY BECAUSE the
// substitution to ASCII has already run — no conversion is needed, but do not
// move the truncation above the substitution.
//
// # Scope
//
// This ports `ok()` and nothing else. The CLI's own call site is
// `ok(NFC(realpath(resolve(p))))`; Slug deliberately stays a pure function with
// no filesystem access, so it cannot fail on a path that does not exist and so
// its test stays hermetic. Resolving an operator-typed path belongs to the
// callers that accept one — do not "complete" the port with EvalSymlinks here.
//
// The mapping is MANY-TO-ONE (`a_b`, `a-b` and `a.b` all encode to `a-b`) and
// must never be inverted back to a path. That is Claude Code's own property,
// not this port's, and internal/claudeproj's TestSlugIsNotInvertible pins it
// because the worktree-to-source mapping depends on it.
//
// # The other three implementations of this rule
//
// A statusline is a shell hook and cannot call Go, so three shell copies exist.
// Change one, change all four:
//
//	plugins/core/statusline/statusline.sh
//	scripts/tests/statusline-memory-slug.test.sh
//	scripts/tests/worktree-memory-probe.sh
//
// The shell form is `printf '%s' "$x" | tr -c 'A-Za-z0-9' '-'`: `printf` never
// `echo` (a trailing newline is outside the class and becomes a dash), and
// without `LC_ALL=C`, which would make `tr` byte-wise and break parity with the
// rune-wise Go encoder on non-ASCII.
//
// The path is cleaned first, so a trailing separator does not leak a trailing
// '-'. An empty path returns an empty slug rather than the '-' that
// filepath.Clean's "." would otherwise produce.
func Slug(path string) string {
	if path == "" {
		return ""
	}
	cleaned := filepath.Clean(path)
	mapped := strings.Map(func(r rune) rune {
		if (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			return r
		}
		return '-'
	}, cleaned)
	if len(mapped) <= maxSlugLen {
		return mapped
	}
	// Every mapped rune is one ASCII byte, so the byte cut is the same cut JS
	// makes over UTF-16 code units. The hash takes the PRE-substitution input.
	return mapped[:maxSlugLen] + "-" + absBase36(jsStringHash(cleaned))
}

// absBase36 is the binary's `Le`: `Math.abs(h).toString(36)`.
//
// The widening to int64 is the whole point. JS's `Math.abs(-2147483648)` is
// 2147483648, a value int32 negation cannot represent — negating it in int32
// would wrap straight back to itself and emit a base36 string with a leading
// '-', producing a slug that can never name a real directory.
func absBase36(h int32) string {
	v := int64(h)
	if v < 0 {
		v = -v
	}
	return strconv.FormatInt(v, 36)
}

// jsStringHash is the binary's `H7`: the Java-style `h = h*31 + c` string hash,
// written as `(h<<5)-h+c` and truncated to 32 bits by JS's `|0` on every step.
//
// Two details are load-bearing. The accumulator is int32 so Go's signed
// wrap-around reproduces `|0` exactly. And the iteration is over UTF-16 code
// units rather than runes, because `charCodeAt` is what the binary reads: an
// astral character contributes TWO units, and its surrogate halves are what get
// hashed.
//
// The caller takes the absolute value in int64, because JS's
// `Math.abs(-2147483648)` is 2147483648 — a value int32 negation cannot hold.
func jsStringHash(s string) int32 {
	var h int32
	for _, u := range utf16.Encode([]rune(s)) {
		h = (h << 5) - h + int32(u)
	}
	return h
}
