#!/usr/bin/env bash
# test.sh — behavioural self-test for the visual-explainer skill.
#   1. the shell passes the static checker, and the checker catches seeded defects;
#   2. new-page.mjs takes the project template first, the plugin's second, and never clobbers;
#   3. serve.mjs answers on 127.0.0.1 only, serves only the one page, and stops (on --stop and on idle);
#   4. to-artifact.mjs produces a body-level page with the title first;
#   5. probe.js runs inside a real page and reports seeded layout defects — when a headless
#      Chrome/Chromium is on the machine (skipped otherwise; CI runners have one).
# Usage: bash scripts/test.sh        Needs: node, curl.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
shell_tpl="$here/templates/explainer-shell.html"
tmp="$(mktemp -d)"
server_pid=""
cleanup() {
  if [ -n "$server_pid" ]; then kill "$server_pid" 2>/dev/null || true; fi
  rm -rf "$tmp"
}
trap cleanup EXIT
pass=0; fail=0; skip=0
ok()   { pass=$((pass + 1)); printf '  ok   %s\n' "$1"; }
bad()  { fail=$((fail + 1)); printf '  FAIL %s\n' "$1"; }
skp()  { skip=$((skip + 1)); printf '  skip %s\n' "$1"; }
json() { node -e "const o=JSON.parse(process.argv[1]); console.log(o[process.argv[2]] ?? '')" "$1" "$2"; }

command -v node >/dev/null || { echo "node is required"; exit 1; }
command -v curl >/dev/null || { echo "curl is required"; exit 1; }

# ── 1. static checker ───────────────────────────────────────────────────────
if node "$here/scripts/check.mjs" "$shell_tpl" >/dev/null; then ok "shell passes check.mjs"; else bad "shell fails check.mjs"; fi
if grep -qE 'innerHTML|insertAdjacentHTML|outerHTML|document\.write' "$shell_tpl"; then
  bad "shell parses strings as HTML somewhere"
else
  ok "shell never parses strings as HTML"
fi
sed -e 's/<main id="main">/<main id="main"><svg id="top" viewBox="0 0 200 100"><text>x<\/text><\/svg>/' \
    -e 's/data-t="sample"/data-t="nope"/' \
    -e 's/data-d="sample"/data-d="ghost"/' \
    "$shell_tpl" > "$tmp/broken.html"
out="$(node "$here/scripts/check.mjs" "$tmp/broken.html" || true)"
for code in dup-id glossary-key drawer-missing; do
  if grep -q "ERROR $code" <<<"$out"; then ok "checker flags $code"; else bad "checker misses $code"; fi
done
if node "$here/scripts/check.mjs" "$tmp/broken.html" >/dev/null 2>&1; then bad "checker exits 0 on a broken page"; else ok "checker exits non-zero on a broken page"; fi

# ── 2. new-page.mjs: project template first ─────────────────────────────────
proj="$tmp/project"; mkdir -p "$proj"
src="$(cd "$proj" && CLAUDE_PROJECT_DIR="$proj" node "$here/scripts/new-page.mjs" "$proj/a.html")"
if [ "$(json "$src" source)" = plugin ] && cmp -s "$proj/a.html" "$shell_tpl"; then ok "new-page uses the plugin shell when the project has none"; else bad "new-page plugin fallback"; fi
mkdir -p "$proj/.claude/templates"; echo '<!-- project shell -->' > "$proj/.claude/templates/explainer-shell.html"
src="$(CLAUDE_PROJECT_DIR="$proj" node "$here/scripts/new-page.mjs" "$proj/b.html")"
if [ "$(json "$src" source)" = project ] && grep -q 'project shell' "$proj/b.html"; then ok "new-page prefers .claude/templates/explainer-shell.html"; else bad "new-page project override"; fi
if CLAUDE_PROJECT_DIR="$proj" node "$here/scripts/new-page.mjs" "$proj/b.html" >/dev/null 2>&1; then bad "new-page overwrote an existing page"; else ok "new-page refuses to overwrite without --force"; fi

# ── 3. serve.mjs: loopback, one page, stops ─────────────────────────────────
site="$tmp/site"; mkdir -p "$site"; cp "$shell_tpl" "$site/page.html"; echo 'SECRET=1' > "$site/.env"; echo 'other' > "$site/other.html"
started="$(node "$here/scripts/serve.mjs" "$site/page.html" --idle 60)"
url="$(json "$started" url)"; server_pid="$(json "$started" pid)"
base="${url%/*}"
case "$url" in http://127.0.0.1:*/page.html) ok "serve.mjs reports a 127.0.0.1 URL" ;; *) bad "serve.mjs URL: $url" ;; esac
code() { curl -s -o /dev/null -w '%{http_code}' "$1"; }
if [ "$(code "$url")" = 200 ]; then ok "serve.mjs serves the page"; else bad "serve.mjs did not serve the page"; fi
if [ "$(code "$base/.env")" = 404 ] && [ "$(code "$base/other.html")" = 404 ] && [ "$(code "$base/../.env")" = 404 ]; then
  ok "serve.mjs answers 404 for every other path (.env, siblings, traversal)"
else
  bad "serve.mjs exposes files beside the page"
fi
if curl -sI "$url" | grep -qi '^cache-control: no-store'; then ok "serve.mjs sends Cache-Control: no-store"; else bad "serve.mjs caching header"; fi
lan_ip="$(node -e "const n=require('os').networkInterfaces();for(const k in n)for(const a of n[k])if(a.family==='IPv4'&&!a.internal){console.log(a.address);process.exit(0)}")"
if [ -n "$lan_ip" ]; then
  port="${base##*:}"
  if curl -s -o /dev/null --max-time 2 "http://$lan_ip:$port/page.html"; then bad "serve.mjs is reachable on $lan_ip"; else ok "serve.mjs is not reachable on a non-loopback interface"; fi
else
  skp "no non-loopback interface to probe"
fi
node "$here/scripts/serve.mjs" --stop "$server_pid" >/dev/null
sleep 1
if kill -0 "$server_pid" 2>/dev/null; then bad "serve.mjs --stop left the server running"; else ok "serve.mjs --stop stops the server"; server_pid=""; fi
started="$(node "$here/scripts/serve.mjs" "$site/page.html" --idle 1)"; server_pid="$(json "$started" pid)"
sleep 3
if kill -0 "$server_pid" 2>/dev/null; then bad "serve.mjs did not stop after the idle limit"; else ok "serve.mjs stops by itself when idle"; server_pid=""; fi

# ── 4. to-artifact.mjs ──────────────────────────────────────────────────────
node "$here/scripts/to-artifact.mjs" "$shell_tpl" "$tmp/artifact.html" >/dev/null
if head -c 200 "$tmp/artifact.html" | grep -q '^<title>'; then ok "to-artifact puts <title> first"; else bad "to-artifact title placement"; fi
if grep -qiE '<!doctype|<html|<head>|<body' "$tmp/artifact.html"; then bad "to-artifact left a document wrapper"; else ok "to-artifact removes the wrapper"; fi
if grep -q 'id="drawer"' "$tmp/artifact.html" && grep -q 'id="glossary"' "$tmp/artifact.html"; then ok "to-artifact keeps body content"; else bad "to-artifact lost body content"; fi

# ── 5. probe.js inside a real page (needs headless Chrome/Chromium) ─────────
chrome=""
for c in google-chrome google-chrome-stable chromium chromium-browser \
         "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
         "/Applications/Chromium.app/Contents/MacOS/Chromium"; do
  if command -v "$c" >/dev/null 2>&1 || [ -x "$c" ]; then chrome="$c"; break; fi
done
if [ -z "$chrome" ]; then
  skp "probe.js run — no headless Chrome/Chromium on this machine"
else
  # Seed three defects: a page-wide element, an SVG label outside its viewBox, a dead hotspot.
  node - "$shell_tpl" "$here/scripts/probe.js" "$tmp/probe.html" <<'NODE'
const fs = require('fs');
const [shellPath, probePath, out] = process.argv.slice(2);
let html = fs.readFileSync(shellPath, 'utf8');
const seeded = '<section class="sec" id="seed" data-name="Seed"><div style="width:3000px">wide</div>' +
  '<svg viewBox="0 0 100 40" role="img" aria-label="seeded diagram"><text x="90" y="20">label far right</text></svg>' +
  '<button class="more" data-d="ghost">dead</button></section>';
html = html.replace('</main>', seeded + '</main>');
const runner = '<script>window.addEventListener("load",function(){var r=(' + fs.readFileSync(probePath, 'utf8') +
  ')();var p=document.createElement("pre");p.id="probe-out";p.textContent=JSON.stringify(r);document.body.append(p);});</script>';
html = html.replace('</body>', runner + '</body>');
fs.writeFileSync(out, html);
NODE
  dom="$("$chrome" --headless=new --no-sandbox --disable-gpu --window-size=1280,900 --virtual-time-budget=4000 --dump-dom "file://$tmp/probe.html" 2>/dev/null || true)"
  report="$(sed -n 's:.*<pre id="probe-out">\(.*\)</pre>.*:\1:p' <<<"$dom" | sed 's/&quot;/"/g; s/&amp;/\&/g; s/&lt;/</g; s/&gt;/>/g')"
  if [ -z "$report" ]; then
    bad "probe.js produced no report in headless Chrome"
  else
    if node -e "const r=JSON.parse(process.argv[1]); process.exit(r.horizontalOverflow.length ? 0 : 1)" "$report"; then ok "probe.js reports the page-wide element"; else bad "probe.js missed horizontal overflow"; fi
    if node -e "const r=JSON.parse(process.argv[1]); process.exit(r.svgLabelIssues.some(s=>s.includes('leaves the viewBox')) ? 0 : 1)" "$report"; then ok "probe.js reports the label outside the viewBox"; else bad "probe.js missed the SVG label"; fi
    if node -e "const r=JSON.parse(process.argv[1]); process.exit(r.deadHotspots.includes('ghost') && r.ok === false ? 0 : 1)" "$report"; then ok "probe.js reports the dead hotspot and ok=false"; else bad "probe.js missed the dead hotspot"; fi
  fi
fi

printf '%d passed, %d failed, %d skipped\n' "$pass" "$fail" "$skip"
[ "$fail" -eq 0 ]
