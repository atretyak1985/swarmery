#!/usr/bin/env bash
# Static contract test for plugins/design-pack/ (Phase 5 -- dogfood & packaging).
#
# This test NEVER runs a model, never opens a browser and makes no network
# call. It asserts that the pack's shipped docs still say the things that make
# the pack safe: a read-only probe, a description that says when NOT to fire,
# the verbatim export route, the screenshot-only warning, the ban on declaring
# completion without a comparison image, the eight STOP triggers with their
# git prohibitions, the tokensFile-always-next-to-a-stop rule, the thin-proxy
# line budget, no machine-absolute paths, no undeclared config fields, and no
# committed verification artefacts.
#
# Style matches scripts/tests/jira-pack-dry-run.test.sh (set -euo pipefail,
# pass/fail counters, one line per check). Dependencies: grep, find, python3.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PACK="$ROOT/plugins/design-pack"
SKILLS="$PACK/skills"

pass=0
fail=0

ok()  { pass=$((pass + 1)); printf '  ok   - %s\n' "$1"; }
bad() { fail=$((fail + 1)); printf '  FAIL - %s\n' "$1"; }

# ── 1. requirements.json parses and declares the design key with a probe ────
if python3 - "$PACK/requirements.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1], encoding='utf-8'))
assert d.get('version') == 1, 'version is not 1'
entries = [e for e in d.get('projectConfig', []) if e.get('key') == 'design']
assert len(entries) == 1, 'expected exactly one projectConfig entry with key "design"'
p = entries[0].get('probe') or {}
assert isinstance(p.get('fields'), list) and p['fields'], 'probe.fields is missing or empty'
assert all(isinstance(f, str) and f for f in p['fields']), 'probe.fields holds a non-string'
assert isinstance(p.get('prompt'), str) and p['prompt'].strip(), 'probe.prompt is missing or empty'
assert isinstance(p.get('timeoutSeconds'), int) and p['timeoutSeconds'] > 0, 'probe.timeoutSeconds is not a positive integer'
PY
then
  ok "check1: requirements.json is version 1 with a well-formed design probe"
else
  bad "check1: requirements.json is malformed (see error above)"
fi

# ── 2. The probe prompt is explicitly read-only and starts nothing ──────────
check2_bad=0
if ! grep -q 'READ-ONLY' "$PACK/requirements.json"; then
  bad "check2: the probe prompt does not declare itself READ-ONLY"
  check2_bad=1
fi
if python3 - "$PACK/requirements.json" <<'PY'
import json, re, sys
d = json.load(open(sys.argv[1], encoding='utf-8'))
prompt = d['projectConfig'][0]['probe']['prompt'].lower()
# The prompt may forbid these actions; it may never instruct them. A verb is a
# finding only when no negation governs the sentence it sits in.
verbs = r'\b(npm install|yarn install|pnpm install|run the build|start the dev server|npm run build|npm run dev)\b'
neg = r"\b(do not|don't|never|without|no )\b"
for sentence in re.split(r'(?<=[.\n])', prompt):
    if re.search(verbs, sentence) and not re.search(neg, sentence):
        print(f'instructional install/build verb: {sentence.strip()[:120]}')
        sys.exit(1)
PY
then
  [ "$check2_bad" -eq 0 ] && ok "check2: the probe prompt is read-only and instructs no install or build"
else
  bad "check2: the probe prompt instructs an install/build action"
  check2_bad=1
fi

# ── 3. The main skill's description carries an explicit NOT for block ───────
if head -n 15 "$SKILLS/design-build/SKILL.md" | grep -q 'NOT for'; then
  ok "check3: design-build description says when NOT to fire"
else
  bad "check3: design-build description has no 'NOT for' block"
fi

# ── 4. The export route is reproduced verbatim, label for label ─────────────
check4_bad=0
for label in 'Share' 'Export' 'Implement this design in code' 'Project HTML'; do
  grep -qF "$label" "$SKILLS/design-acquire/SKILL.md" \
    || { bad "check4: design-acquire is missing the export-route label '$label'"; check4_bad=1; }
done
[ "$check4_bad" -eq 0 ] && ok "check4: design-acquire reproduces the four export-route labels"

# ── 5. Screenshot-only input is warned about, not quietly accepted ──────────
if grep -qiE 'pixel (accuracy|match)[^.]{0,80}(is not|are not|cannot be|can not be|never) (guaranteed|promised)' \
     "$SKILLS/design-acquire/SKILL.md"; then
  ok "check5: design-acquire warns that screenshots cannot promise pixel accuracy"
else
  bad "check5: design-acquire has no screenshot-only pixel-accuracy warning"
fi

# ── 6. Completion without the comparison image is forbidden in writing ──────
if grep -qiE 'forbidden|may not|never' "$SKILLS/design-verify/SKILL.md" \
   && grep -qF 'side-by-side.png' "$SKILLS/design-verify/SKILL.md"; then
  ok "check6: design-verify forbids declaring completion without side-by-side.png"
else
  bad "check6: design-verify does not forbid completion without the comparison image"
fi

# ── 7. All eight STOP triggers survive, and so do the git prohibitions ──────
AGENT="$PACK/agents/design-implementer.md"
check7_bad=0
declare -a triggers=(
  'tokensFile'          # 1 token change
  'fontLoader'          # 2 font
  'componentsRoot'      # 3 shared component
  'approved'            # 4 file outside the approved list
  'depend'              # 5 new dependency / build config
  'maxIterations'       # 6 budget
  'diffPercent'         # 7 no improvement
  'degraded'            # 8 degraded-mode confirmation
)
for t in "${triggers[@]}"; do
  grep -qF "$t" "$AGENT" || { bad "check7: the agent no longer mentions STOP-trigger anchor '$t'"; check7_bad=1; }
done
grep -qF 'STOPPED' "$AGENT" || { bad "check7: the agent has no STOPPED stop form"; check7_bad=1; }
for prohibition in 'commit' 'branch' 'push'; do
  grep -qi "$prohibition" "$AGENT" || { bad "check7: the agent no longer prohibits '$prohibition'"; check7_bad=1; }
done
[ "$check7_bad" -eq 0 ] && ok "check7: eight STOP-trigger anchors and the git prohibitions are intact"

# ── 8. tokensFile is never described as writable without a stop in scope ────
#     Negative check: a line that talks about WRITING tokensFile must sit
#     within four lines of stop language, so no future edit can quietly grant
#     the agent permission to "improve" the diff through the token layer.
#     Bare mentions (the input-contract enumeration, prose references) are not
#     write language and are not flagged.
if python3 - "$AGENT" <<'PY'
import re, sys
text = open(sys.argv[1], encoding='utf-8').read()
lines = text.splitlines()
write = r'(edit|write|writing|modify|modifying|change|changing|add|adding|update|updating|adjust|adjusting|nudge|set|touch)'
grant = re.compile(r'\b(may|can|is allowed to|are allowed to|permitted to|free to|should|it is fine to)\b[^.]{0,60}\b'
                   + write + r'\b', re.I)
negated = re.compile(r"\b(never|not|no|cannot|can't|may not|must not|refuse|forbidden|without)\b", re.I)
failures = []
for i, line in enumerate(lines):
    if 'tokensFile' not in line:
        continue
    if grant.search(line) and not negated.search(line):
        failures.append(f'{i + 1}: {line.strip()[:110]}')
# Positive half: the doc must still bind tokensFile to a stop, so deleting the
# trigger is a failure and not a silent pass.
bound = any('tokensFile' in ln and re.search(r'stop|trigger|zero writes|forbidden|never', ln, re.I)
            for ln in lines)
if not bound:
    failures.append('no line binds tokensFile to a stop/trigger at all')
if failures:
    print('tokensFile write permission is not fenced by a stop:')
    for f in failures:
        print('   ', f)
    sys.exit(1)
PY
then
  ok "check8: no tokensFile write is described without a stop in scope"
else
  bad "check8: the agent describes writing tokensFile with no stop in scope"
fi

# ── 9. The command stays a thin proxy (line budget) ─────────────────────────
# The budget covers the command body only: the `# How to use` guide that the
# docs contract (scripts/docgen/check-coverage.sh) REQUIRES on every command is
# reader documentation, not proxy logic, so counting it would make the two
# gates mutually exclusive. Everything before that mandated H1 must stay thin.
CMD="$PACK/commands/design-implement.md"
cmd_lines="$(awk '/^# How to use$/{exit} {n++} END{print n+0}' "$CMD")"
if [ "$cmd_lines" -le 110 ]; then
  ok "check9: commands/design-implement.md body is $cmd_lines lines (thin-proxy budget 110, usage guide excluded)"
else
  bad "check9: commands/design-implement.md body is $cmd_lines lines, over the 110-line thin-proxy budget (usage guide excluded)"
fi

# ── 10. No machine-absolute paths; no config field the schema does not declare
check10_bad=0
if grep -rnE '(^|[^A-Za-z0-9_])/(Users|Volumes|home)/' "$PACK" 2>/dev/null | grep -v '\$HOME' | grep -q .; then
  grep -rnE '(^|[^A-Za-z0-9_])/(Users|Volumes|home)/' "$PACK" 2>/dev/null | grep -v '\$HOME' | head -3
  bad "check10: an absolute machine path is committed in the pack"
  check10_bad=1
fi
if python3 - "$PACK" <<'PY'
import json, os, re, sys
pack = sys.argv[1]
schema = json.load(open(os.path.join(pack, 'requirements.json'), encoding='utf-8'))['projectConfig'][0]['schema']


def declared(node, prefix=''):
    out = set()
    for key, sub in (node.get('properties') or {}).items():
        path = f'{prefix}{key}'
        out.add(path)
        if sub.get('type') == 'object':
            out |= declared(sub, f'{path}.')
    return out


known = declared(schema)
# Artefact filenames the docs legitimately name (design.png, design.html, a
# design.zip export) are not config fields and must not be read as one.
artefacts = {'png', 'jpg', 'jpeg', 'html', 'htm', 'zip', 'json', 'css', 'md'}
undeclared = {}
for root, dirs, files in os.walk(pack):
    dirs[:] = [d for d in dirs if d not in {'.git', 'node_modules'}]
    for name in files:
        if not name.endswith('.md'):
            continue
        path = os.path.join(root, name)
        try:
            text = open(path, encoding='utf-8').read()
        except (UnicodeDecodeError, OSError):
            continue
        for ref in re.findall(r'\bdesign\.([A-Za-z][A-Za-z0-9_.]*)', text):
            ref = ref.rstrip('.').split('`')[0]
            if ref in known or ref.split('.')[-1].lower() in artefacts:
                continue
            # allow a prefix that is itself declared (design.verify in prose)
            if any(ref == k or k.startswith(ref + '.') for k in known):
                continue
            undeclared.setdefault(ref, os.path.relpath(path, pack))
if undeclared:
    print('config fields referenced but not declared in requirements.json:')
    for ref, where in sorted(undeclared.items()):
        print(f'    design.{ref}  ({where})')
    sys.exit(1)
PY
then
  [ "$check10_bad" -eq 0 ] && ok "check10: no absolute machine paths and no undeclared design.* fields"
else
  bad "check10: the pack references a design.* field requirements.json does not declare"
  check10_bad=1
fi

# ── 11. No bundled MCP server and no committed verification artefacts ───────
check11_bad=0
if find "$PACK" -iname '.mcp.json' 2>/dev/null | grep -q .; then
  bad "check11: a .mcp.json file exists under plugins/design-pack/"
  check11_bad=1
fi
if find "$PACK" \( -iname '*.png' -o -iname '*.jpg' -o -name '.design-verify' \) 2>/dev/null | grep -q .; then
  find "$PACK" \( -iname '*.png' -o -iname '*.jpg' -o -name '.design-verify' \) | head -3
  bad "check11: verification artefacts are committed inside the pack"
  check11_bad=1
fi
[ "$check11_bad" -eq 0 ] && ok "check11: no .mcp.json and no committed verification artefacts"

printf 'design-pack-contract: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
