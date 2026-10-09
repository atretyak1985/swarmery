#!/usr/bin/env bash
# check-landing-gates.sh — the phase-landing quality gate, in one file.
#
# Runs, in order, and stops at the first red gate (printing which):
#   1. make build                         (docs snapshot → vite bundle → go:embed → binary)
#   2. make test — go vet ./...
#   3. make test — go test ./...          (run with -coverprofile, exactly as CI does)
#   4. Go coverage ≥ 70%                  (CI's exclusions: cmd/swarmery, web, internal/docsfs)
#   5. web: npm run build
#   6. web: npm test
#   7. web: npm run lint
#   8. web/scripts/check-no-provider-branching.sh   (SC-11 ratchet)
#   9. scripts/scan-flavor.sh prints "✓ clean"
#  10. no "gitlab not supported yet" left in internal/
#  11. VCS token prefixes (ghp_ / glpat-) appear in Go sources only where they
#      belong: the credstore, tests, and the pre-existing system-endpoint
#      redaction table internal/api/redact.go.
#
# Gates 2+3 are `make test` (go vet ./... then go test ./...) run as its two
# commands so the coverage profile comes from the same test run: CI
# (.github/workflows/swarmery-ci.yml) does exactly that and never calls make test.
#
# Flakes: a few Go tests and vitest files are known to time out under load
# (internal/toolproc, internal/usage, internal/dispatch, claudeprobe). A failing
# Go package, or a failing vitest file, is retried ONCE in isolation and the
# script says so ("flaky: <pkg> passed on isolated retry"). A second failure
# fails the gate. Nothing is retried silently.
#
# Toolchain: a Go toolchain without the covdata tool (GOTOOLCHAIN=auto
# downloads can lack it) makes go test exit 1 for every package with no tests
# under -coverprofile. That is tolerated, with a printed note, ONLY for packages
# that really have no tests; the local profile then lacks their 0% records, so
# the local number can sit slightly above CI's. Every non-zero exit of go test
# must be explained by a FAIL line or such a package, or the gate fails.
#
# Resolves every path from this script's location, so it runs from any cwd.
# Exit codes: 0 all green, 1 a gate failed. Logs of a failed run are kept and
# their directory printed.

set -uo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
sw_dir="$(cd "$script_dir/.." && pwd)"
repo_root="$(cd "$sw_dir/../.." && pwd)"
web_dir="$sw_dir/web"
coverage_floor="70.0"

work="$(mktemp -d "${TMPDIR:-/tmp}/landing-gates.XXXXXX")"
keep_logs=0
cleanup() {
  if [[ "$keep_logs" == 0 ]]; then
    rm -rf "$work"
  fi
}
trap cleanup EXIT

pass() {
  printf 'PASS  %s%s\n' "$1" "${2:+ — $2}"
}

# fail <gate> [detail] [log]: print the verdict, the tail of the log, and stop.
fail() {
  local gate="$1" detail="${2:-}" log="${3:-}"
  printf 'FAIL  %s%s\n' "$gate" "${detail:+ — $detail}"
  if [[ -n "$log" && -s "$log" ]]; then
    echo "----- last 40 lines of $log -----"
    tail -n 40 "$log"
    echo "-----"
  fi
  keep_logs=1
  echo "gate failed: $gate (logs kept in $work)"
  exit 1
}

note() {
  printf '      %s\n' "$1"
}

# --- 1. make build -----------------------------------------------------------
if make -C "$sw_dir" build >"$work/build.log" 2>&1; then
  pass "make build"
else
  fail "make build" "" "$work/build.log"
fi

# --- 2. make test: go vet ----------------------------------------------------
if (cd "$sw_dir" && go vet ./...) >"$work/vet.log" 2>&1; then
  pass "make test: go vet ./..."
else
  fail "make test: go vet ./..." "" "$work/vet.log"
fi

# --- 3. make test: go test (with the coverage profile CI uses) ---------------
profile="$work/coverage.out"

# splice_profile <pkg> <retry-profile>: replace <pkg>'s blocks in $profile by
# the ones from its isolated retry, so the coverage gate measures a passing run.
splice_profile() {
  local pkg="$1" retry="$2"
  awk -v p="$pkg/" '
    index($0, p) == 1 && index(substr($0, length(p) + 1), "/") == 0 { next }
    { print }
  ' "$profile" >"$profile.tmp"
  tail -n +2 "$retry" >>"$profile.tmp"
  mv "$profile.tmp" "$profile"
}

if (cd "$sw_dir" && go test -coverprofile="$profile" ./...) >"$work/test.log" 2>&1; then
  pass "make test: go test ./..."
else
  failed_pkgs="$(grep -E '^FAIL[[:space:]]+[^[:space:]]+' "$work/test.log" | awk '{print $2}' | sort -u)"
  # A toolchain without the covdata tool (Go ≥1.25 toolchains fetched by
  # GOTOOLCHAIN=auto ship without it) cannot write the 0% coverage record of a
  # package that has no tests, and go test exits 1 for it with
  # `go: no such tool "covdata"`. That is the machine, not the code — but only
  # for a package with no tests at all; anywhere else it fails the gate.
  covdata_pkgs="$(awk '/^# /{pkg=$2} /no such tool "covdata"/{print pkg}' "$work/test.log" | sort -u)"
  if [[ -z "$failed_pkgs" && -z "$covdata_pkgs" ]] || [[ ! -s "$profile" ]]; then
    fail "make test: go test ./..." "go test failed without a FAIL <pkg> line or a coverage profile" "$work/test.log"
  fi
  while IFS= read -r pkg; do
    [[ -z "$pkg" ]] && continue
    tests="$(cd "$sw_dir" && go list -f '{{len .TestGoFiles}}{{len .XTestGoFiles}}' "$pkg" 2>/dev/null)"
    if [[ "$tests" != "00" ]]; then
      fail "make test: go test ./..." "$pkg has tests but the toolchain has no covdata tool" "$work/test.log"
    fi
    note "env: this Go toolchain has no covdata tool, so test-less $pkg has no 0% record in the local profile (CI's has one)"
  done <<<"$covdata_pkgs"
  flaky=()
  i=0
  while IFS= read -r pkg; do
    [[ -z "$pkg" ]] && continue
    i=$((i + 1))
    retry_profile="$work/retry-$i.out"
    retry_log="$work/retry-$i.log"
    if (cd "$sw_dir" && go test -count=1 -coverprofile="$retry_profile" "$pkg") >"$retry_log" 2>&1; then
      flaky+=("$pkg")
      splice_profile "$pkg" "$retry_profile"
    else
      echo "      first run failures:"
      grep -E '^(--- FAIL|FAIL)' "$work/test.log" | sed 's/^/        /'
      fail "make test: go test ./..." "$pkg failed again on its isolated retry" "$retry_log"
    fi
  done <<<"$failed_pkgs"
  pass "make test: go test ./..." "${#flaky[@]} package(s) needed an isolated retry"
  for pkg in ${flaky[@]+"${flaky[@]}"}; do
    note "flaky: $pkg passed on isolated retry"
  done
  full_run_failures="$(grep -E '^[[:space:]]*--- FAIL' "$work/test.log" | awk '{print $3}' | sort -u | tr '\n' ' ' | sed 's/ $//')"
  if [[ -n "$full_run_failures" ]]; then
    note "full-run failures: $full_run_failures"
  fi
fi

# --- 4. coverage floor (CI's exact exclusion + aggregation) ------------------
grep -vE '/(cmd/swarmery|web|internal/docsfs)/' "$profile" >"$work/coverage.gated.out"
total="$(cd "$sw_dir" && go tool cover -func="$work/coverage.gated.out" | awk '/^total:/ {gsub(/%/,"",$3); print $3}')"
if [[ -z "$total" ]]; then
  fail "Go coverage ≥ ${coverage_floor}%" "go tool cover produced no total"
fi
if awk -v t="$total" -v f="$coverage_floor" 'BEGIN { exit (t+0 >= f+0) ? 0 : 1 }'; then
  pass "Go coverage ≥ ${coverage_floor}%" "gated coverage ${total}% (excl. cmd/swarmery, web, internal/docsfs)"
else
  echo "      lowest packages in the run:"
  grep -E 'coverage: [0-9.]+% of statements' "$work/test.log" |
    awk '{for (n = 1; n <= NF; n++) if ($n == "coverage:") print $(n + 1), $2}' |
    sort -n | head -n 10 | sed 's/^/        /'
  fail "Go coverage ≥ ${coverage_floor}%" "gated coverage ${total}% is below the floor"
fi

# --- 5. web build ------------------------------------------------------------
if npm --prefix "$web_dir" run build >"$work/web-build.log" 2>&1; then
  pass "web: npm run build"
else
  fail "web: npm run build" "" "$work/web-build.log"
fi

# --- 6. web tests ------------------------------------------------------------
if NO_COLOR=1 FORCE_COLOR=0 npm --prefix "$web_dir" test >"$work/web-test.log" 2>&1; then
  pass "web: npm test" "$(grep -E '^[[:space:]]*Tests[[:space:]]' "$work/web-test.log" | tail -n 1 | sed 's/^[[:space:]]*//')"
else
  failed_files="$(grep -oE '^[[:space:]]*FAIL[[:space:]]+[^[:space:]]+\.test\.tsx?' "$work/web-test.log" | awk '{print $2}' | sort -u)"
  if [[ -z "$failed_files" ]]; then
    fail "web: npm test" "vitest failed without a FAIL <file> line" "$work/web-test.log"
  fi
  j=0
  while IFS= read -r file; do
    [[ -z "$file" ]] && continue
    j=$((j + 1))
    if NO_COLOR=1 FORCE_COLOR=0 npm --prefix "$web_dir" test -- "$file" >"$work/web-retry-$j.log" 2>&1; then
      note "flaky: $file passed on isolated retry"
    else
      fail "web: npm test" "$file failed again on its isolated retry" "$work/web-retry-$j.log"
    fi
  done <<<"$failed_files"
  pass "web: npm test" "$j file(s) needed an isolated retry"
fi

# --- 7. web lint -------------------------------------------------------------
if npm --prefix "$web_dir" run lint >"$work/web-lint.log" 2>&1; then
  pass "web: npm run lint"
else
  fail "web: npm run lint" "" "$work/web-lint.log"
fi

# --- 8. SC-11 ratchet --------------------------------------------------------
if bash "$web_dir/scripts/check-no-provider-branching.sh" >"$work/ratchet.log" 2>&1; then
  pass "check-no-provider-branching" "$(tail -n 1 "$work/ratchet.log")"
else
  fail "check-no-provider-branching" "" "$work/ratchet.log"
fi

# --- 9. neutrality scan ------------------------------------------------------
bash "$repo_root/scripts/scan-flavor.sh" >"$work/flavor.log" 2>&1
if grep -q '✓ clean' "$work/flavor.log"; then
  pass "scan-flavor.sh" "$(grep '✓ clean' "$work/flavor.log")"
  if [[ -z "${FLAVOR_BRAND:-}" && ! -f "$repo_root/.flavor-tokens" ]]; then
    note "note: no FLAVOR_BRAND and no .flavor-tokens in $repo_root — the scan used its placeholder patterns"
  fi
else
  fail "scan-flavor.sh" "no '✓ clean' line" "$work/flavor.log"
fi

# --- 10. no GitLab placeholder left ------------------------------------------
if leftovers="$(grep -rn 'gitlab not supported yet' "$sw_dir/internal/")"; then
  echo "$leftovers" >"$work/gitlab-leftovers.log"
  fail "no 'gitlab not supported yet' in internal/" "the Phase 2/3 placeholder is still there" "$work/gitlab-leftovers.log"
else
  rc=$?
  if [[ "$rc" != 1 ]]; then
    fail "no 'gitlab not supported yet' in internal/" "grep failed (exit $rc)"
  fi
  pass "no 'gitlab not supported yet' in internal/"
fi

# --- 11. token prefixes only where they belong -------------------------------
# Allowed: the credstore (it defines the redaction shapes), *_test.go fixtures,
# and internal/api/redact.go — the /api/system/* redaction table, which predates
# the landing work and masks the same token shapes in config file views.
if token_files="$(cd "$sw_dir" && grep -rlE 'ghp_|glpat-' internal/ --include='*.go')"; then
  :
else
  rc=$?
  if [[ "$rc" != 1 ]]; then
    fail "token prefixes only in credstore/tests" "grep failed (exit $rc)"
  fi
  token_files=""
fi
stray="$(printf '%s\n' "$token_files" | grep -vE '^internal/repoprovider/credstore/|_test\.go$|^internal/api/redact\.go$' | grep -v '^$' || true)"
if [[ -n "$stray" ]]; then
  echo "$stray" >"$work/token-files.log"
  fail "token prefixes only in credstore/tests" "ghp_/glpat- found outside the allowed files" "$work/token-files.log"
fi
pass "token prefixes only in credstore/tests" "$(printf '%s\n' "$token_files" | grep -c .) file(s), all allowed"

echo "all landing gates green"
