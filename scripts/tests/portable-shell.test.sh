#!/bin/bash
# Portability scanner: shell in this repo must run on Linux, not only on macOS.
#
# WHY THIS EXISTS. Two suites in this repo were green on macOS for months and
# had never run in CI. The day they did, both failed for the same reason — and
# then a brand-new hook, written the same week, failed for it a third time. The
# bug is not obvious enough to catch by review:
#
#   `stat` is TWO different tools wearing one name. BSD/macOS spells mtime
#   `-f %m`; GNU spells it `-c %Y`. The trap is that GNU's `-f` is NOT an error —
#   it means "show filesystem status", prints a multi-line `File: …` block, and
#   EXITS 0. So the natural-looking `stat -f %m x || stat -c %Y x` never reaches
#   its fallback on Linux and hands that block to whatever consumes it: into
#   `$(( … ))` it becomes an unbound-variable crash under `set -u`; into a
#   numeric guard it silently disables the check.
#
# A comment explaining this in three files would go stale. This fails the build
# instead. Same for the `shasum` (BSD) / `sha256sum` (GNU) split, and for
# `date -v` (BSD) / `date -d` (GNU).
#
# The rule in each case is the same: never let ONE spelling's exit code decide.
# Validate the OUTPUT, or try both.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT" || exit 1

pass=0
fail=0
ok() { pass=$((pass + 1)); }
bad() { fail=$((fail + 1)); printf '  ✗ %s\n' "$1"; }

# files — every shell script this repo ships or tests with.
files=()
while IFS= read -r f; do files+=("$f"); done < <(find plugins scripts -name '*.sh' | sort)

# code_of <file> — the file with comment-only lines removed. A comment that
# EXPLAINS the trap (this suite's own header does) is not the trap, and scanning
# raw text would make documenting the hazard a build failure.
code_of() { sed -E 's/^[[:space:]]*#.*$//' "$1"; }

# has <file> <ERE> — true when the file's code (comments stripped) matches.
# The strip runs inside a substitution, never as `code_of … | grep -q`: under
# `pipefail`, grep -q exits on its first match, sed then takes SIGPIPE writing
# its next block, and the pipeline reports 141 — a random failure on any file
# large enough that sed is still writing when grep is already done.
has() { grep -qE -- "$2" <<<"$(code_of "$1")"; }

# ── stat ──────────────────────────────────────────────────────────
# A file using `stat -f` must also use `stat -c`, and must not rely on `-f`
# failing: the `||` chain with `-f` FIRST is the exact broken shape.
for f in "${files[@]}"; do
  has "$f" 'stat -f' || continue
  if ! has "$f" 'stat -c'; then
    bad "$f uses BSD 'stat -f' with no GNU 'stat -c' form — it reads every mtime as garbage on Linux"
    continue
  fi
  # `stat -f … || stat -c …` on one line: the fallback is unreachable on Linux,
  # because GNU's -f exits 0.
  if has "$f" 'stat -f[^|]*\|\|[[:space:]]*stat -c'; then
    bad "$f falls back from 'stat -f' to 'stat -c' by exit code — GNU's -f exits 0, so the fallback never runs"
    continue
  fi
  ok
done

# ── shasum / sha256sum ────────────────────────────────────────────
for f in "${files[@]}"; do
  has "$f" 'shasum' || continue
  if has "$f" 'sha256sum'; then ok
  else bad "$f uses BSD 'shasum' with no GNU 'sha256sum' form"; fi
done

# ── date -v / date -d ─────────────────────────────────────────────
for f in "${files[@]}"; do
  has "$f" 'date -v' || continue
  if has "$f" 'date -d'; then ok
  else bad "$f uses BSD 'date -v' with no GNU 'date -d' form"; fi
done

# ── bash 4 builtins in a #!/bin/bash script ───────────────────────
# The second shape of the same class, and the one that actually shipped: a hook
# spelled `mapfile -t segments < <(…)` ran green for weeks and was dead on every
# invocation. macOS freezes /bin/bash at 3.2.57 (GPLv2), so a bash 4+ builtin
# there is not a syntax error — `bash -n` passes — it is a MISSING COMMAND at
# runtime. The hook then exited non-zero on every Bash tool call while every
# rule after that line never ran.
#
# It hid because the suites invoke `bash "$HOOK"`, and `bash` on a dev Mac is
# whatever the package manager put first on PATH (5.x). The script itself runs
# under its own shebang, which is /bin/bash. So scan the TEXT, not a run.
#
# Portable spellings: `while IFS= read -r x; do arr+=("$x"); done < <(…)` for
# mapfile/readarray; a `case` for ${v,,}/${v^^}; parallel arrays for `declare -A`.
#
# This file is the one exemption, and necessarily so: a blacklist scanner spells
# out every shape it forbids, so scanning itself can only ever report itself.
# The stat/shasum checks above self-pass because they are "if A then also B";
# this one is a plain blacklist and has no such escape.
for f in "${files[@]}"; do
  [ "$f" = "scripts/tests/portable-shell.test.sh" ] && continue
  head -n 1 "$f" | grep -q 'bin/bash' || continue
  found=""
  has "$f" '(^|[^[:alnum:]_])(mapfile|readarray)([[:space:]]|$)' && found="mapfile/readarray (bash 4.0)"
  has "$f" '(declare|local|typeset) -A' && found="declare -A associative array (bash 4.0)"
  has "$f" '\$\{[A-Za-z_][A-Za-z0-9_]*(\[[^]]*\])?(,,|\^\^|,|\^)\}' && found="\${v,,}/\${v^^} case conversion (bash 4.0)"
  has "$f" '\$\{[A-Za-z_][A-Za-z0-9_]*\[-[0-9]+\]\}' && found="negative array index (bash 4.3)"
  has "$f" '(declare|local) -n ' && found="nameref (bash 4.3)"
  has "$f" '\$\{[A-Za-z_][A-Za-z0-9_]*@[QEPAaKk]\}' && found="\${v@Q} transformation (bash 4.4)"
  if [ -n "$found" ]; then
    bad "$f uses $found — /bin/bash on macOS is 3.2, where that is 'command not found' at runtime, not a syntax error"
  else ok; fi
done

# ── this suite must not reintroduce the pipe it just removed ──────
# `code_of … | grep` under pipefail is the SIGPIPE race described at `has`.
if has "${BASH_SOURCE[0]}" 'code_of[^|]*\|[[:space:]]*grep'; then
  bad "portable-shell.test.sh pipes code_of into grep — use has() so pipefail cannot turn a match into a SIGPIPE failure"
else ok; fi

# ── hook suites must run the hook the way production runs it ──────
# The other half of the mapfile story. A scanner catches the bash-4 builtins it
# knows to name; it cannot catch the NEXT unnamed one. What let that bug live at
# all was the harness: `printf … | bash "$HOOK"` picks the interpreter off PATH
# (homebrew 5.x on a dev Mac), while Claude Code invokes the hook by path and
# gets its shebang — /bin/bash 3.2. Two different interpreters, one green suite.
#
# Executing the hook instead closes the class outright: every suite then runs
# under the same bash production does, and the next 4.x-ism fails a test on the
# machine that would have shipped it. Hooks are all mode 0755, so this costs
# nothing. Env prefixes still work — `FOO=1 "$HOOK"` is valid.
for f in "${files[@]}"; do
  case "$f" in scripts/tests/*.test.sh) ;; *) continue ;; esac
  if has "$f" 'bash[[:space:]]+"\$HOOK"'; then
    bad "$f runs the hook as \`bash \"\$HOOK\"\` — that takes bash off PATH, not the shebang production uses; execute \"\$HOOK\" instead"
  else ok; fi
done

# ── the shapes actually behave ────────────────────────────────────
# Not just "both spellings are present" — the real check. This runs whichever
# form the CURRENT machine has and asserts the result is a number, so the suite
# proves the invariant on Linux (in CI) and on macOS (locally) with one body.
probe="$(mktemp)"
trap 'rm -f "$probe"' EXIT
printf 'x' > "$probe"

mtime="$(stat -c %Y "$probe" 2>/dev/null)"
case "$mtime" in ''|*[!0-9]*) mtime="$(stat -f %m "$probe" 2>/dev/null)" ;; esac
case "$mtime" in
  ''|*[!0-9]*) bad "neither 'stat -c %Y' nor 'stat -f %m' yields a number on this machine — the helpers cannot work" ;;
  *) ok ;;
esac

# And the trap itself, asserted where it exists: on GNU, `stat -f` must be
# recognised as NOT an mtime. On BSD it is one, and the check is skipped.
if stat -c %Y "$probe" >/dev/null 2>&1; then
  wrong="$(stat -f %m "$probe" 2>/dev/null)"
  case "$wrong" in
    ''|*[!0-9]*) ok ;;  # correctly unusable — which is exactly why exit codes cannot be trusted
    *) bad "expected GNU 'stat -f %m' to yield non-numeric output; got '$wrong'" ;;
  esac
fi

printf 'portable-shell: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
