#!/bin/bash
# Behavioral tests for the lockfile stamp and the borrower refusal in
# tools/swarmery/Makefile's build-web target.
#
# Framework-free. Every case runs the REAL build-web recipe with BUILD_WEB_DIR
# pointed at a throwaway web dir, BUILD_WEB_NPM at a fake npm that only logs,
# and BUILD_WEB_LEND_ROOTS at a throwaway worktree root, so nothing in the
# checkout is installed, built or deleted. It asserts that:
#   (a) a stamp matching package-lock.json skips npm ci and still builds;
#   (b) a stamp miss while a worktree symlink borrows this node_modules refuses,
#       names the borrower, and leaves node_modules untouched;
#   (c) a stamp miss with no borrower (only a decoy link elsewhere) runs npm ci,
#       writes the stamp, and builds;
#   (d) no node_modules at all runs npm ci and writes the stamp;
#   (e) a build run from INSIDE a borrower (its node_modules is the link)
#       refuses too, instead of wiping the lender through the link.
# Observed 2026-10-19: `make install` ran npm ci in the primary checkout while a
# phase run borrowed its web/node_modules, and a build died on
# "vite: command not found".
# Run locally with `bash scripts/tests/make-build-web-stamp.test.sh`; CI
# discovers scripts/tests/*.test.sh.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
MK_DIR="$ROOT/tools/swarmery"

pass=0
fail=0
fail_case() { fail=$((fail + 1)); printf '  ✗ %s\n' "$1"; }
ok_case() { pass=$((pass + 1)); }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
# Physical path: macOS mktemp lands under /var, a symlink to /private/var, and
# the recipe compares physical paths.
tmp="$(cd -P "$tmp" && pwd -P)"

sha256() {
	if command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | awk '{print $1}'
	else
		sha256sum "$1" | awk '{print $1}'
	fi
}

# The fake npm: log the subcommand; `ci` behaves like the real one on disk —
# delete node_modules, then create it fresh.
cat >"$tmp/npm" <<'SH'
#!/bin/sh
printf '%s\n' "$*" >>"$NPM_LOG"
if [ "$1" = ci ]; then
	rm -rf node_modules
	mkdir node_modules
	echo installed >node_modules/marker
fi
SH
chmod +x "$tmp/npm"

# new_web <name> — a web dir with a lockfile and a dist to prune.
new_web() {
	local d="$tmp/$1/web"
	mkdir -p "$d/dist/assets"
	printf '{"lockfileVersion":3,"name":"%s"}\n' "$1" >"$d/package-lock.json"
	echo old >"$d/dist/index.html"
	printf '%s' "$d"
}

# run_build <webdir> <lendroots> <log> <out> — the real recipe, the fakes wired in.
run_build() {
	NPM_LOG="$3" make -s -C "$MK_DIR" build-web \
		BUILD_WEB_DIR="$1" BUILD_WEB_NPM="$tmp/npm" \
		BUILD_WEB_LEND_ROOTS="$2" >"$4" 2>&1
}

# --- (a) stamp hit: skip npm ci, still build ---------------------------------
web="$(new_web a)"
mkdir -p "$web/node_modules"
sha256 "$web/package-lock.json" >"$web/node_modules/.swarmery-lock-sha"
log="$tmp/a.log"
: >"$log"
if run_build "$web" "$tmp/a-roots" "$log" "$tmp/a.out" &&
	! grep -qx ci "$log" && grep -qx 'run build' "$log" && [ ! -e "$web/dist/index.html" ]; then
	ok_case
else
	fail_case "(a) a matching stamp skips npm ci and builds (npm: $(tr '\n' ';' <"$log"); out: $(cat "$tmp/a.out"))"
fi

# --- (b) stamp miss + borrower: refuse, name it, touch nothing ---------------
web="$(new_web b)"
mkdir -p "$web/node_modules"
echo stale >"$web/node_modules/.swarmery-lock-sha"
echo mine >"$web/node_modules/marker"
borrower="$tmp/b-roots/proj/T-1/tools/swarmery/web/node_modules"
mkdir -p "$(dirname "$borrower")"
ln -s "$web/node_modules" "$borrower"
log="$tmp/b.log"
: >"$log"
if run_build "$web" "$tmp/b-roots" "$log" "$tmp/b.out"; then
	fail_case "(b) a borrowed node_modules must make build-web fail"
elif [ -s "$log" ]; then
	fail_case "(b) npm ran despite a borrower (npm: $(tr '\n' ';' <"$log"))"
elif ! grep -qF "$borrower" "$tmp/b.out"; then
	fail_case "(b) the refusal does not name the borrower (out: $(cat "$tmp/b.out"))"
elif [ "$(cat "$web/node_modules/marker")" != mine ]; then
	fail_case "(b) node_modules was modified despite the refusal"
else
	ok_case
fi

# --- (c) stamp miss, only a decoy link: npm ci, stamp, build -----------------
web="$(new_web c)"
mkdir -p "$web/node_modules" "$tmp/c-elsewhere/node_modules"
echo stale >"$web/node_modules/.swarmery-lock-sha"
decoy="$tmp/c-roots/proj/T-2/web/node_modules"
mkdir -p "$(dirname "$decoy")"
ln -s "$tmp/c-elsewhere/node_modules" "$decoy"
log="$tmp/c.log"
: >"$log"
want="$(sha256 "$web/package-lock.json")"
if run_build "$web" "$tmp/c-roots" "$log" "$tmp/c.out" &&
	[ "$(tr '\n' ';' <"$log")" = "ci;run build;" ] &&
	[ "$(cat "$web/node_modules/.swarmery-lock-sha" 2>/dev/null)" = "$want" ] &&
	[ -e "$web/node_modules/marker" ]; then
	ok_case
else
	fail_case "(c) a stamp miss without a borrower runs npm ci and writes the stamp (npm: $(tr '\n' ';' <"$log"); out: $(cat "$tmp/c.out"))"
fi

# --- (d) no node_modules at all: npm ci, stamp -------------------------------
web="$(new_web d)"
log="$tmp/d.log"
: >"$log"
want="$(sha256 "$web/package-lock.json")"
if run_build "$web" "$tmp/d-roots-missing" "$log" "$tmp/d.out" &&
	[ "$(tr '\n' ';' <"$log")" = "ci;run build;" ] &&
	[ "$(cat "$web/node_modules/.swarmery-lock-sha" 2>/dev/null)" = "$want" ]; then
	ok_case
else
	fail_case "(d) a fresh web dir runs npm ci and writes the stamp (npm: $(tr '\n' ';' <"$log"); out: $(cat "$tmp/d.out"))"
fi

# --- (e) build from inside the borrower: refuse too --------------------------
lender="$(new_web e-lender)"
mkdir -p "$lender/node_modules"
echo lent >"$lender/node_modules/marker"
bweb="$tmp/e-roots/proj/T-3/tools/swarmery/web"
mkdir -p "$bweb"
cp "$lender/package-lock.json" "$bweb/package-lock.json"
echo '{"changed":true}' >>"$bweb/package-lock.json" # a different lockfile: a stamp miss
ln -s "$lender/node_modules" "$bweb/node_modules"
log="$tmp/e.log"
: >"$log"
if run_build "$bweb" "$tmp/e-roots" "$log" "$tmp/e.out"; then
	fail_case "(e) a build inside a borrower must refuse instead of reinstalling through the link"
elif [ -s "$log" ] || [ "$(cat "$lender/node_modules/marker")" != lent ]; then
	fail_case "(e) the lender's node_modules was touched (npm: $(tr '\n' ';' <"$log"))"
else
	ok_case
fi

echo "make-build-web-stamp: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
