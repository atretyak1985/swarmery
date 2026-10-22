#!/bin/bash
# guard-parse.sh — read a phase doc's `kind: guard` block. Sourced by
# phase-guard.sh and by scripts/tests/phase-guard.test.sh; also runnable as
# `bash guard-parse.sh <doc>` (same contract, as the exit code).
#
# THE CONTRACT is the Go parser's, tools/swarmery/internal/mdfence/guard.go
# (ParseGuard), held to the same golden corpus in
# tools/swarmery/testdata/fixtures/guard-blocks/:
#
#   guard_parse <doc>
#     0  parsed — stdout: every protected_paths glob, one per line, then
#        every entry of the optional `allow` list prefixed with "!" (paths the
#        operator let back into scope by answering a guard decision)
#     1  absent — the doc carries no ```yaml block whose top-level kind is guard
#     3  unparsable — stdout: ONE line, "unparsable (line N): <reason>", the
#        document line and the reason, exactly ParseError.Error()'s shape
#   Warnings (a glob naming a directory, a second guard block) go to stderr,
#   one "phase-guard: warning: …" line each, and never change the exit code.
#
# Absent and unparsable stay apart for the same reason they do in Go: "the
# planner wrote no guard" is an old plan; "the planner wrote a guard nobody can
# read" is a defect, and the hook blocks on it instead of guessing.
#
# WHAT IS READ. Fences follow mdfence.Blocks: ``` or ~~~, three or more, up to
# three leading spaces, closed only by the same character at least as long. A
# block counts when its info string is yaml/yml and its first top-level
# `kind:` line says guard; a bare ``` block (the copy-paste agent prompt) never
# does. Inside the block a small yaml subset is parsed: a top-level mapping
# whose protected_paths / stop_if / allow values are a block list, a one-line or
# multi-line flow list, a bare or quoted scalar, or null. Unknown keys are
# skipped. Anything else under those two keys — an alias (an unquoted glob
# that starts with *), an anchor, a tag, a block scalar, a nested list or map,
# a tab in the indentation — is unparsable. Where this subset is stricter than
# yaml.v3 it errs toward unparsable: the hook then blocks and says why, which
# is the direction a guard can afford to be wrong in.
#
# bash 3.2 + POSIX awk only (stock macOS awk is BWK awk: no gensub, no
# intervals); no yq.

_GUARD_PARSE_AWK=$(cat <<'AWK'
function trim(s) { sub(/^[ \t]+/, "", s); sub(/[ \t]+$/, "", s); return s }

# fence(line): M_C / M_N / M_REST — the fence character, its run length and
# the rest of the line; M_N is 0 when the line opens or closes no fence.
function fence(line,    s, lead, c, n) {
	M_C = ""; M_N = 0; M_REST = ""
	s = line; lead = 0
	while (substr(s, 1, 1) == " ") { s = substr(s, 2); lead++ }
	if (lead > 3 || s == "") return
	c = substr(s, 1, 1)
	if (c != "`" && c != "~") return
	n = 0
	while (substr(s, n + 1, 1) == c) n++
	if (n < 3) return
	M_C = c; M_N = n; M_REST = substr(s, n + 1)
}

# endblock(): file the block just closed if it is a yaml guard block.
function endblock(    info, i, k, l, v) {
	info = M_INFO
	while (length(info) > 0 && substr(info, length(info), 1) == FC) info = substr(info, 1, length(info) - 1)
	info = tolower(trim(info))
	for (i = 1; i <= length(info); i++) {
		if (index(" \t{", substr(info, i, 1)) > 0) { info = substr(info, 1, i - 1); break }
	}
	if (info != "yaml" && info != "yml") return
	for (k = 1; k <= NB; k++) {
		l = B[k]
		sub(/[ \t]+$/, "", l)
		if (l !~ /^kind:[ \t]*["']?[A-Za-z_-]+["']?[ \t]*(#.*)?$/) continue
		v = l
		sub(/^kind:[ \t]*["']?/, "", v)
		match(v, /^[A-Za-z_-]+/)
		v = tolower(substr(v, RSTART, RLENGTH))
		if (v == "guard") {
			NGB++
			if (NGB == 1) {
				GSTART = BSTART
				GN = NB
				for (i = 1; i <= NB; i++) G[i] = B[i]
			} else {
				EXTRA[NGB] = BSTART
			}
		}
		break # the first top-level kind line decides
	}
}

function syn(line, why) { if (SYNERR == "") { SYNERR = "not valid yaml: " why; SYNLINE = line } }
function shape(key, line, why) {
	if (!(key in PERR)) { PERR[key] = key ": " why; PERRLINE[key] = line }
}
function known(key) { return key == "protected_paths" || key == "stop_if" || key == "allow" }
function add(key, s) { if (known(key)) { NV[key]++; VAL[key, NV[key]] = s } }

# quoted(s, pos): decode the quoted scalar opening at s[pos]. Sets Q_VAL and
# Q_END (the index just past the closing quote); Q_END = 0 when it never closes,
# Q_END = -1 on an escape yaml does not know.
function quoted(s, pos,    q, i, c, n, out) {
	q = substr(s, pos, 1); out = ""; n = length(s)
	for (i = pos + 1; i <= n; i++) {
		c = substr(s, i, 1)
		if (q == "'" && c == "'") {
			if (substr(s, i + 1, 1) == "'") { out = out "'"; i++; continue }
			Q_VAL = out; Q_END = i + 1; return
		}
		if (q == "\"" && c == "\"") { Q_VAL = out; Q_END = i + 1; return }
		if (q == "\"" && c == "\\") {
			c = substr(s, i + 1, 1); i++
			if (c == "\\" || c == "\"" || c == "/") out = out c
			else if (c == "t") out = out "\t"
			else if (c == "n") out = out "\n"
			else if (c == " ") out = out " "
			else { Q_END = -1; return }
			continue
		}
		out = out c
	}
	Q_END = 0
}

# rest_ok(s, pos): what follows a value on its line is blank or a comment.
function rest_ok(s, pos) { return substr(s, pos) ~ /^[ \t]*(#.*)?$/ }

# plain(s): a plain scalar with its trailing " # comment" cut, trimmed.
function plain(s,    i) {
	i = index(s, " #"); if (i > 0) s = substr(s, 1, i - 1)
	i = index(s, "\t#"); if (i > 0) s = substr(s, 1, i - 1)
	return trim(s)
}
function isnull(s) { return s == "~" || tolower(s) == "null" }
function ismap(s) { return s ~ /:([ \t]|$)/ }
function indicator(c) { return index("&!|>@`", c) > 0 }

# entry(key, text, line): one block-list entry, the text after "- ".
function entry(key, text, line,    c, s) {
	text = trim(text)
	if (text == "" || substr(text, 1, 1) == "#") { shape(key, line, "every entry must be a plain string"); return }
	c = substr(text, 1, 1)
	if (c == "\"" || c == "'") {
		quoted(text, 1)
		if (Q_END == -1) { syn(line, "found unknown escape character"); return }
		if (Q_END == 0) { syn(line, "found unexpected end of stream (an unclosed quote)"); return }
		if (!rest_ok(text, Q_END)) { syn(line, "did not find expected key after the quoted entry"); return }
		s = trim(Q_VAL)
		if (s == "") { shape(key, line, "empty entry"); return }
		add(key, s); return
	}
	if (c == "*") { syn(line, "an unquoted value that starts with * is a yaml alias — quote the glob"); return }
	if (indicator(c)) { syn(line, "an unquoted value that starts with " c " is not a plain string here — quote it"); return }
	if (c == "[" || c == "{" || text ~ /^-([ \t]|$)/) { shape(key, line, "every entry must be a plain string"); return }
	s = plain(text)
	if (isnull(s) || ismap(s)) { shape(key, line, "every entry must be a plain string"); return }
	add(key, s)
}

# flow(key, text, line): a flow list "[a, "b", 'c']". Returns 2 while the
# list is still open (the caller feeds it the next line), 0 once it is read.
function flow(key, text, line,    n, i, c, s, want, cnt, tmp, j, depth) {
	n = length(text); i = 2; want = 1; cnt = 0
	# A list nobody closed yet — wait for more lines before judging it.
	depth = 0
	for (j = 1; j <= n; j++) {
		c = substr(text, j, 1)
		if (c == "\"" || c == "'") { quoted(text, j); if (Q_END <= 0) break; j = Q_END - 1; continue }
		if (c == "[") depth++
		if (c == "]") { depth--; if (depth == 0) break }
	}
	if (depth > 0 && Q_END != -1) return 2
	while (i <= n) {
		c = substr(text, i, 1)
		if (c == " " || c == "\t") { i++; continue }
		if (c == "]") {
			if (!rest_ok(text, i + 1)) { syn(line, "did not find expected key after the flow list"); return 0 }
			for (j = 1; j <= cnt; j++) add(key, tmp[j])
			return 0
		}
		if (c == ",") {
			if (want) { syn(line, "did not find expected node content (an empty flow entry)"); return 0 }
			want = 1; i++; continue
		}
		if (!want) { syn(line, "did not find expected ',' or ']'"); return 0 }
		if (c == "\"" || c == "'") {
			quoted(text, i)
			if (Q_END == -1) { syn(line, "found unknown escape character"); return 0 }
			if (Q_END == 0) { syn(line, "found unexpected end of stream (an unclosed quote)"); return 0 }
			s = trim(Q_VAL)
			if (s == "") { shape(key, line, "empty entry"); return 0 }
			tmp[++cnt] = s; i = Q_END; want = 0; continue
		}
		if (c == "*") { syn(line, "an unquoted value that starts with * is a yaml alias — quote the glob"); return 0 }
		if (indicator(c)) { syn(line, "an unquoted value that starts with " c " is not a plain string here — quote it"); return 0 }
		if (c == "[" || c == "{") { shape(key, line, "every entry must be a plain string"); return 0 }
		s = ""
		while (i <= n) {
			c = substr(text, i, 1)
			if (c == "," || c == "]") break
			if (c == "[" || c == "{" || c == "}") { syn(line, "did not find expected ',' or ']'"); return 0 }
			s = s c; i++
		}
		s = plain(s)
		if (isnull(s) || ismap(s)) { shape(key, line, "every entry must be a plain string"); return 0 }
		tmp[++cnt] = s; want = 0
	}
	syn(line, "did not find expected ',' or ']'")
	return 0
}

# inline(key, value, line): a value written on the key's own line.
function inline(key, value, line,    c, s) {
	c = substr(value, 1, 1)
	if (c == "[") {
		if (flow(key, value, line) == 2) { FLOWKEY = key; FLOWBUF = value; FLOWLINE = line }
		return
	}
	if (c == "{") { shape(key, line, "must be a list of strings"); return }
	if (c == "\"" || c == "'") {
		quoted(value, 1)
		if (Q_END == -1) { syn(line, "found unknown escape character"); return }
		if (Q_END == 0) { syn(line, "found unexpected end of stream (an unclosed quote)"); return }
		if (!rest_ok(value, Q_END)) { syn(line, "did not find expected key after the quoted value"); return }
		s = trim(Q_VAL)
		if (s != "") add(key, s)
		return
	}
	if (c == "*") { syn(line, "an unquoted value that starts with * is a yaml alias — quote the glob"); return }
	if (indicator(c)) { syn(line, "an unquoted value that starts with " c " is not a plain string here — quote it"); return }
	s = plain(value)
	if (ismap(s)) { syn(line, "mapping values are not allowed in this context"); return }
	if (s != "" && !isnull(s)) add(key, s)
}

function names_dir(g,    last) {
	g = trim(g)
	if (g == "") return 0
	if (substr(g, length(g), 1) == "/") return 1
	last = g
	while (index(last, "/") > 0) last = substr(last, index(last, "/") + 1)
	return last == "**"
}
function file_glob_for(g) {
	g = trim(g)
	sub(/\/$/, "", g)
	sub(/\/\*\*$/, "", g)
	if (g == "**" || g == "") return "**/*"
	return g "/**/*"
}

# parse(): walk the guard block's lines (G[1..GN]; content line k is document
# line GSTART + k, GSTART being the opening fence's line).
function parse(    k, raw, line, ind, key, value, mode, seqind, t, rc) {
	mode = ""; seqind = -1; key = ""
	for (k = 1; k <= GN && SYNERR == ""; k++) {
		raw = G[k]; line = GSTART + k
		if (FLOWKEY != "") {
			t = trim(raw)
			if (substr(t, 1, 1) != "#") FLOWBUF = FLOWBUF " " t
			rc = flow(FLOWKEY, FLOWBUF, FLOWLINE)
			if (rc != 2) FLOWKEY = ""
			continue
		}
		if (raw ~ /^[ \t]*$/ || raw ~ /^[ \t]*#/) continue
		if (raw ~ /^ *\t/) { syn(line, "found a tab character where an indentation space is expected"); break }
		match(raw, /^ */); ind = RLENGTH
		if (ind == 0 && raw !~ /^-([ \t]|$)/) {
			if (raw !~ /^[A-Za-z_][A-Za-z0-9_.-]*[ \t]*:([ \t]|$)/) { syn(line, "could not find expected ':' (line " line " is not a key)"); break }
			key = raw; sub(/[ \t]*:.*$/, "", key)
			value = raw; sub(/^[^:]*:/, "", value); value = trim(value)
			if (substr(value, 1, 1) == "#") value = ""
			if (key in SEEN) { syn(line, "mapping key \"" key "\" already defined"); break }
			SEEN[key] = 1
			seqind = -1
			if (value == "") { mode = "seq"; continue }
			mode = "inline"
			if (known(key)) inline(key, value, line)
			else if (substr(value, 1, 1) == "[" && flow(key, value, line) == 2) { FLOWKEY = key; FLOWBUF = value; FLOWLINE = line }
			continue
		}
		if (key == "") { syn(line, "did not find expected key"); break }
		if (raw ~ /^ *-([ \t]|$)/ && (mode == "seq" || mode == "items")) {
			if (seqind < 0) { seqind = ind; mode = "items" }
			if (ind != seqind) {
				if (ind < seqind) { syn(line, "did not find expected key (bad indentation of a list entry)"); break }
				if (known(key)) shape(key, line, "every entry must be a plain string")
				continue
			}
			t = raw; sub(/^ *-/, "", t)
			if (known(key)) entry(key, t, line)
			continue
		}
		if (ind == 0) { syn(line, "did not find expected key"); break }
		if (mode == "items") {
			if (ind < seqind) { syn(line, "did not find expected key (bad indentation)"); break }
			if (known(key)) shape(key, line, "every entry must be a plain string")
			continue
		}
		if (mode == "seq") {
			mode = "nested"
			if (known(key)) shape(key, line, "must be a list of strings")
			continue
		}
		# More-indented text under an inline value: a continuation yaml would
		# fold into the scalar. Under a guard key that is not a list of globs.
		if (mode == "inline" && known(key)) { syn(line, "a value that continues on the next line is not supported — write a list"); break }
	}
	if (FLOWKEY != "") { syn(FLOWLINE, "did not find expected ',' or ']'"); FLOWKEY = "" }
}

{ sub(/\r$/, "") }
{
	fence($0)
	if (M_N > 0 && FL == 0) { FC = M_C; FL = M_N; M_INFO = M_REST; BSTART = NR; NB = 0; next }
	if (M_N > 0 && M_C == FC && M_N >= FL) { endblock(); FL = 0; FC = ""; next }
	if (FL > 0) B[++NB] = $0
}
END {
	if (FL > 0) endblock()
	if (NGB == 0) exit 1
	parse()
	if (SYNERR != "") { print "unparsable (line " SYNLINE "): " SYNERR; exit 3 }
	if (!("protected_paths" in SEEN)) { print "unparsable (line " (GSTART + 1) "): missing key protected_paths"; exit 3 }
	if (!("stop_if" in SEEN)) { print "unparsable (line " (GSTART + 1) "): missing key stop_if"; exit 3 }
	if ("protected_paths" in PERR) { print "unparsable (line " PERRLINE["protected_paths"] "): " PERR["protected_paths"]; exit 3 }
	if ("stop_if" in PERR) { print "unparsable (line " PERRLINE["stop_if"] "): " PERR["stop_if"]; exit 3 }
	if ("allow" in PERR) { print "unparsable (line " PERRLINE["allow"] "): " PERR["allow"]; exit 3 }
	for (k = 1; k <= NV["protected_paths"]; k++) {
		g = VAL["protected_paths", k]
		print g
		if (names_dir(g))
			printf "phase-guard: warning: protected path \"%s\" names a directory, not files — write the file pattern (e.g. \"%s\")\n", g, file_glob_for(g) > "/dev/stderr"
	}
	for (k = 1; k <= NV["allow"]; k++) print "!" VAL["allow", k]
	for (k = 2; k <= NGB; k++)
		printf "phase-guard: warning: a second `kind: guard` block at line %d is ignored\n", EXTRA[k] > "/dev/stderr"
	exit 0
}
AWK
)

# guard_parse <doc> — see the contract at the top of this file.
guard_parse() {
	[ -f "$1" ] || return 1
	awk "$_GUARD_PARSE_AWK" "$1"
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
	guard_parse "$1"
	exit $?
fi
