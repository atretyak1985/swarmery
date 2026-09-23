#!/usr/bin/env bash
# validate-agent-refs.sh — component reference-integrity gate.
#
# Closes the dead-reference bug class found by the 2026-09 audit: an agent that
# names a skill its plugin does not ship, points at a doc that does not exist,
# pins a model instead of an alias, or carries frontmatter Claude Code ignores
# must fail CI, not surface months later as silent behavior drift.
#
# Checks, over plugins/*/agents/*.md (plus skills/commands for path checks):
#   1. frontmatter key whitelist (catches permissionMode & friends — ignored
#      on plugin subagents, so their presence is always a bug);
#   2. `model:` must be an alias: opus | sonnet | haiku | inherit;
#   2b. `effort:` must be one of low | medium | high | xhigh | max — a typo or a
#      retired level is silently ignored at dispatch, so it must fail here;
#   3. every `skills:` entry resolves to <plugin>/skills/<name>/SKILL.md — or,
#      for domain packs, to a core skill (packs layer on top of core: every
#      consumer that enables a pack also enables core). Anything else needs a
#      CROSS_PLUGIN_ALLOW entry below — and every allow entry must still match
#      a live ref (a stale exemption fails too). core itself may never reach
#      into another plugin (neutrality: core has no dependencies);
#   4. every ${CLAUDE_PLUGIN_ROOT}/<path> reference resolves inside its plugin;
#   5. known-retired model IDs are forbidden everywhere under plugins/**, and
#      full current-generation IDs are forbidden in agents/*.md (bodies drift
#      when models rotate — aliases don't). Educational content in skills may
#      cite concrete API model IDs.
#
# Output mirrors the CI frontmatter step: `PROBLEM: <path> — <why>` lines and a
# `checked=N problems=K` summary. Exit 1 on any problem.
#
# Env: AGENT_REFS_ROOT — corpus root (default: repo root above this script).
set -euo pipefail

ROOT="${AGENT_REFS_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"

# Intentional cross-plugin skill references, "plugin:skill" per line.
# Empty today by design (core must stay self-contained); add entries only with
# a recorded reason. An entry no longer matched by any live ref FAILS the run.
CROSS_PLUGIN_ALLOW=""

AGENT_REFS_ROOT="$ROOT" CROSS_PLUGIN_ALLOW="$CROSS_PLUGIN_ALLOW" python3 - <<'PY'
import glob, os, re, sys

root = os.environ['AGENT_REFS_ROOT']
allow = {e.strip() for e in os.environ.get('CROSS_PLUGIN_ALLOW', '').split() if e.strip()}

# Plugin-shipped agents accept a NARROWER field set than project/user agents:
# name, description, model, effort, maxTurns, tools, disallowedTools, skills,
# memory, background, isolation. `hooks`, `mcpServers` and `permissionMode` are
# unsupported for plugin agents *for security reasons* — the plugins reference
# says so explicitly. That is why the general subagent docs listing
# permissionMode does not contradict this rule: that list covers project and
# user agents, not plugin ones. (Verified 2026-09-02 against
# code.claude.com/docs/en/plugins-reference — re-check before relaxing.)
# `docs` is ours, not Claude Code's: the provenance block scripts/docgen/ and
# internal/sysscan read. `color` is listed for subagents generally; harmless
# either way since an unread key changes no behaviour.
AGENT_KEYS = {'name', 'description', 'model', 'tools', 'disallowedTools',
              'skills', 'memory', 'isolation', 'background', 'color',
              'maxTurns', 'docs', 'effort'}
MODEL_ALIASES = {'opus', 'sonnet', 'haiku', 'inherit'}
EFFORT_LEVELS = {'low', 'medium', 'high', 'xhigh', 'max'}
RETIRED_MODEL = re.compile(r'sonnet-4-6|opus-4-8|claude-(?:sonnet|opus)-4(?![0-9])[0-9a-z.\-]*')
PINNED_MODEL = re.compile(r'claude-(?:sonnet|opus|haiku)-[0-9][0-9a-z.\-]*')

problems = 0
checked = 0
used_allow = set()

def report(rel, why):
    global problems
    problems += 1
    print(f'PROBLEM: {rel} — {why}')

def frontmatter(text):
    m = re.match(r'^---\n(.*?)\n---\n', text, re.S)
    return m.group(1) if m else None

for path in sorted(glob.glob(os.path.join(root, 'plugins/*/agents/*.md'))):
    rel = os.path.relpath(path, root)
    plugin = rel.split(os.sep)[1]
    checked += 1
    text = open(path, encoding='utf-8').read()
    fm = frontmatter(text)
    if fm is None:
        report(rel, 'missing frontmatter block')
        continue
    keys = []
    for line in fm.split('\n'):
        if line.startswith('#') or line.startswith(' ') or line.startswith('\t') or not line.strip():
            continue
        km = re.match(r'^([A-Za-z_][A-Za-z0-9_-]*):', line)
        if km:
            keys.append(km.group(1))
    for k in keys:
        if k not in AGENT_KEYS:
            report(rel, f'frontmatter key "{k}" is not consumed by Claude Code for plugin subagents — remove it (metadata belongs in plugins/{plugin}/AGENTS.md)')
    mm = re.search(r'^model:\s*(\S+)\s*$', fm, re.M)
    if mm and mm.group(1) not in MODEL_ALIASES:
        report(rel, f'model "{mm.group(1)}" is not an alias — use one of {sorted(MODEL_ALIASES)} so model upgrades never strand the fleet')
    em = re.search(r'^effort:\s*(\S+)\s*$', fm, re.M)
    if em and em.group(1) not in EFFORT_LEVELS:
        report(rel, f'effort "{em.group(1)}" is not a known level — use one of {sorted(EFFORT_LEVELS)}; an unrecognised value is ignored at dispatch instead of failing loudly')
    # tools:/disallowedTools: take exact tool names, MCP server patterns
    # (mcp__server, mcp__server__*) or Agent(a, b) — NOT the scoped permission
    # syntax from settings.json. `Bash(git diff:*)` in tools: is silently not a
    # Bash grant, which is the permissionMode failure mode wearing a new hat.
    # Scoping belongs in the consumer's permissions.allow.
    for field in ('tools', 'disallowedTools'):
        tm = re.search(r'^%s:[ \t]*(.*)$' % field, fm, re.M)
        if not tm:
            continue
        # split on commas OUTSIDE parentheses — Agent(worker, researcher) is one entry
        raw, depth, buf = [], 0, ''
        for ch in tm.group(1):
            if ch == '(':
                depth += 1
            elif ch == ')':
                depth = max(0, depth - 1)
            if ch == ',' and depth == 0:
                raw.append(buf); buf = ''
            else:
                buf += ch
        raw.append(buf)
        for entry in (e.strip() for e in raw):
            if not entry or entry.startswith('mcp__'):
                continue
            if re.fullmatch(r'Agent\([^)]*\)', entry):
                continue
            if not re.fullmatch(r'[A-Za-z][A-Za-z0-9_]*', entry):
                report(rel, f'{field} entry "{entry}" is not a bare tool name — '
                            'tools:/disallowedTools: accept exact names, mcp__ patterns or Agent(...); '
                            'scoped syntax like Bash(git diff:*) only works in permissions.allow and is ignored here')
    sm = re.search(r'^skills:[ \t]*(.*)$', fm, re.M)
    if sm:
        entries = []
        if sm.group(1).strip() and sm.group(1).strip() != '[]':
            inline = sm.group(1).strip()
            if inline.startswith('['):
                entries = [e.strip() for e in inline.strip('[]').split(',') if e.strip()]
        after = fm[sm.end():]
        for line in after.split('\n'):
            im = re.match(r'^\s+-\s+(\S+)\s*$', line)
            if im:
                entries.append(im.group(1))
            elif line and not line.startswith((' ', '\t')):
                break
        for skill in entries:
            skill_md = os.path.join(root, 'plugins', plugin, 'skills', skill, 'SKILL.md')
            core_md = os.path.join(root, 'plugins', 'core', 'skills', skill, 'SKILL.md')
            key = f'{plugin}:{skill}'
            if os.path.isfile(skill_md):
                continue
            if plugin != 'core' and os.path.isfile(core_md):
                continue  # packs layer on core
            if key in allow:
                used_allow.add(key)
                continue
            report(rel, f'skills entry "{skill}" resolves in neither plugins/{plugin}/skills/ nor core, and has no CROSS_PLUGIN_ALLOW entry')

# 4. ${CLAUDE_PLUGIN_ROOT} path references in all component markdown.
for pattern in ('plugins/*/agents/*.md', 'plugins/*/skills/*/SKILL.md', 'plugins/*/commands/*.md'):
    for path in sorted(glob.glob(os.path.join(root, pattern))):
        rel = os.path.relpath(path, root)
        plugin = rel.split(os.sep)[1]
        text = open(path, encoding='utf-8').read()
        for ref in set(re.findall(r'\$\{CLAUDE_PLUGIN_ROOT\}/([A-Za-z0-9_./-]+)', text)):
            target = os.path.join(root, 'plugins', plugin, ref.rstrip('/'))
            if not os.path.exists(target):
                report(rel, f'references ${{CLAUDE_PLUGIN_ROOT}}/{ref} but plugins/{plugin}/{ref} does not exist')

# 5a. retired model IDs anywhere under plugins/**.
for path in sorted(glob.glob(os.path.join(root, 'plugins/**/*'), recursive=True)):
    if not os.path.isfile(path) or not path.endswith(('.md', '.json', '.sh', '.yaml', '.yml')):
        continue
    rel = os.path.relpath(path, root)
    for i, line in enumerate(open(path, encoding='utf-8', errors='replace'), 1):
        m = RETIRED_MODEL.search(line)
        if m:
            report(rel, f'line {i}: retired model id "{m.group(0)}" — use aliases (opus/sonnet/haiku/inherit)')

# 5b. full pinned IDs in agent files (frontmatter or body).
for path in sorted(glob.glob(os.path.join(root, 'plugins/*/agents/*.md'))):
    rel = os.path.relpath(path, root)
    for i, line in enumerate(open(path, encoding='utf-8', errors='replace'), 1):
        m = PINNED_MODEL.search(line)
        if m:
            report(rel, f'line {i}: pinned model id "{m.group(0)}" in an agent file — use aliases so model rotations never strand the fleet')

for key in sorted(allow - used_allow):
    report('scripts/validate-agent-refs.sh', f'stale CROSS_PLUGIN_ALLOW entry "{key}" matches no live reference — remove it')

print(f'checked={checked} problems={problems}')
sys.exit(1 if problems else 0)
PY
