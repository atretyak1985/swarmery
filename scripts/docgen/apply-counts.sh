#!/usr/bin/env bash
# apply-counts.sh — splice scripts/docgen/counts.sh's numbers into the docs.
#
# Every consumer and its marked regions are listed below; nothing outside them is touched:
#
#   README.md         <!-- BEGIN generated:packs -->                the pack table
#   README.uk.md      <!-- BEGIN generated:packs -->                the same table, Ukrainian labels
#   site/index.html   <!-- BEGIN generated:stats-hero -->           the hero counters
#   site/index.html   <!-- BEGIN generated:stats-control-plane -->  the daemon counters
#
# The markers are the contract. Prose stays hand-written and reviewable; only
# the rows and tiles between a BEGIN/END pair are regenerated, and a missing or
# reversed marker is a hard error rather than a silent no-op — a splice that
# quietly matched nothing is exactly how a "generated" number goes stale while
# still looking generated.
#
# The `core` row keeps its hand-written sentence with the three counts
# substituted, because that row says something no manifest field says. Every
# other row is the pack's own `description` from marketplace.json with the
# trailing "Opt-in; requires …" clause dropped: the README states that once,
# under the table, and repeating it eleven times is noise. Two tiles inside the
# control-plane region — the port and the bind address — are not repo counts and
# are emitted verbatim; they live inside the region only because they are part
# of the same visual row.
#
# A counter whose value counts.sh could not derive (null) is DROPPED rather than
# guessed. An unverifiable stat is worth less than no stat.
#
# --check is the CI shape: it regenerates each file into a throwaway copy under
# $TMPDIR, `diff -u`s it against the tree, prints every difference and exits
# non-zero. It never writes to the working tree. That gate is the whole point —
# without it this script is a one-off edit that rots, which is the state it was
# written to end.
#
# Usage:
#   apply-counts.sh            rewrite the marked regions in place
#   apply-counts.sh --check    report drift and exit 1; touches nothing
#
# Exit: 0 = written (or already current), 1 = drift found, bad usage, missing
# marker, or a failing counts.sh.
#
# Env:
#   DOCGEN_ROOT  corpus root (default: this repo, resolved from lib.sh)
set -euo pipefail

DOCGEN_SELF_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# shellcheck source=scripts/docgen/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

APPLY_MODE="write"
case "${1:-}" in
  "") ;;
  --check) APPLY_MODE="check" ;;
  -h | --help)
    echo "usage: apply-counts.sh [--check]"
    exit 0
    ;;
  *)
    echo "usage: apply-counts.sh [--check]" >&2
    exit 1
    ;;
esac
if [ "$#" -gt 1 ]; then
  echo "usage: apply-counts.sh [--check]" >&2
  exit 1
fi
export APPLY_MODE

# counts.sh exits non-zero when the manifest and the tree disagree; `set -e`
# turns that into this script's exit, which is the intended behaviour — never
# splice numbers derived from a corpus that is already known to be inconsistent.
COUNTS_JSON="$(bash "${DOCGEN_SELF_DIR}/counts.sh")"
export COUNTS_JSON

read -r -d '' APPLY_JS <<'APPLY' || true

const os = require('os');
const { execFileSync } = require('child_process');

const root = process.env.DOCGEN_ROOT || process.cwd();
const mode = process.env.APPLY_MODE === 'check' ? 'check' : 'write';
const counts = JSON.parse(process.env.COUNTS_JSON || '{}');

const die = (why) => {
  console.error('apply-counts: ' + why);
  process.exit(1);
};

// ── the splice ──────────────────────────────────────────────────────────────
// Replaces everything strictly between a BEGIN/END pair, re-indenting the
// generated lines to the BEGIN marker's own column so neither the markdown nor
// the HTML has its indentation hard-coded here.
function splice(text, name, lines, rel) {
  const begin = '<!-- BEGIN ' + name + ' -->';
  const end = '<!-- END ' + name + ' -->';
  const all = text.split('\n');
  const b = all.findIndex((l) => l.trim() === begin);
  const e = all.findIndex((l) => l.trim() === end);
  if (b < 0) die(rel + ' has no `' + begin + '` marker');
  if (e < 0) die(rel + ' has no `' + end + '` marker');
  if (e < b) die(rel + ': `' + end + '` appears before `' + begin + '`');
  const indent = (all[b].match(/^[ \t]*/) || [''])[0];
  const body = lines.map((l) => (l === '' ? '' : indent + l));
  return all.slice(0, b + 1).concat(body, all.slice(e)).join('\n');
}

// ── README: the pack table ──────────────────────────────────────────────────
const mdCell = (s) => s.replace(/\|/g, '\\|');

// Every pack but core carries a trailing "Opt-in; requires …" sentence in the
// manifest. "requires core" is dropped: the README states it once, under the
// table. Anything else the clause names is a prerequisite ON THE MACHINE — the
// serena binary, the graphify CLI, an Atlassian MCP provider — and that is the
// one fact a reader scanning this table acts on, so it is kept as its own
// sentence. Dropping the whole clause is how the table quietly stopped saying
// which packs need something installed first.
const oneLiner = (d) => {
  const m = String(d).trim().match(/^([\s\S]*?)\s*Opt-in;\s*([\s\S]*)$/);
  if (!m) return String(d).trim();
  const head = m[1].trim();
  const rest = m[2].trim().replace(/^requires\s+core\s+and\s+/i, 'requires ');
  if (/^requires\s+core\.?$/i.test(m[2].trim())) return head;
  return (head + ' ' + rest.charAt(0).toUpperCase() + rest.slice(1)).trim();
};

// Ukrainian noun form for a count: 1, 21 → one; 2–4, 22–24 → few; the rest
// (5–20, 11–14, 25…) → many. A generated number must never read as a typo.
const ukPlural = (n, one, few, many) => {
  const m10 = n % 10;
  const m100 = n % 100;
  if (m10 === 1 && m100 !== 11) return one;
  if (m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14)) return few;
  return many;
};

// Per-language labels for the generated regions. Only the header row and the
// hand-written `core` sentence are localised: every other row is the pack's own
// manifest description, which stays English in every language because the
// manifests are not localised. `coreRow` takes counts.core and interpolates its
// three numbers. Keyed by language so other targets can add their own keys
// beside these without touching them.
const LABELS = {
  en: {
    header: "| Plugin | What's inside |",
    coreRow: (c) =>
      '| **`core`** | The vendor-neutral framework every consumer enables: ' +
      c.agents +
      ' judgment-style agents (tech-lead, planner, architect, implementation-agent, ' +
      'code-reviewer, … — see `plugins/core/AGENTS.md`), ' +
      c.skills +
      ' progressively-disclosed skills, ' +
      c.commands +
      ' commands, lifecycle/safety hooks, the statusline, and the project-aware ' +
      '`agent-work` workspace CLI. |',
  },
  uk: {
    header: '| Плагін | Що всередині |',
    coreRow: (c) =>
      '| **`core`** | Фреймворк, незалежний від постачальника, який вмикає кожен споживач: ' +
      c.agents +
      ' ' +
      ukPlural(c.agents, 'агент', 'агенти', 'агентів') +
      ' з власним судженням (tech-lead, planner, architect, implementation-agent, ' +
      'code-reviewer, … — див. `plugins/core/AGENTS.md`), ' +
      c.skills +
      ' ' +
      ukPlural(c.skills, 'скіл', 'скіли', 'скілів') +
      ' із поступовим розкриттям, ' +
      c.commands +
      ' ' +
      ukPlural(c.commands, 'команда', 'команди', 'команд') +
      ', хуки життєвого циклу й безпеки, statusline і CLI робочого простору ' +
      '`agent-work`, що знає про проєкт. |',
  },
};

function packsTable(L) {
  const rows = [L.header, '|---|---|'];
  for (const p of counts.plugins || []) {
    if (p.name === 'core') {
      rows.push(L.coreRow(counts.core));
    } else {
      rows.push('| `' + p.name + '` | ' + mdCell(oneLiner(p.description)) + ' |');
    }
  }
  if (rows.length < 3) die('counts.sh returned no plugins — refusing to empty the pack table');
  return rows;
}

// ── landing page: the two stat rows ─────────────────────────────────────────
function tile(n, label, opts) {
  const o = opts || {};
  return (
    '<div class="stat' +
    (o.hot ? ' hot' : '') +
    '"><div class="n' +
    (o.small ? ' sm' : '') +
    '">' +
    n +
    '</div><div class="l">' +
    label +
    '</div></div>'
  );
}

function heroStats() {
  return [
    tile(counts.packs, 'plugin packs'),
    tile(counts.agents, 'agents'),
    tile(counts.skills, 'skills'),
    tile(':7777', 'local control plane', { hot: true }),
  ];
}

function controlPlaneStats() {
  const out = [];
  if (counts.go_packages !== null && counts.go_packages !== undefined) {
    out.push(tile(counts.go_packages, 'Go packages'));
  }
  if (counts.api_routes !== null && counts.api_routes !== undefined) {
    out.push(tile(counts.api_routes, 'REST routes'));
  }
  out.push(tile(':7777', 'dashboard port', { hot: true }));
  out.push(tile('127.0.0.1', 'the only interface it binds', { small: true }));
  return out;
}

const targets = [
  { rel: 'README.md', regions: [['generated:packs', packsTable(LABELS.en)]] },
  { rel: 'README.uk.md', regions: [['generated:packs', packsTable(LABELS.uk)]] },
  {
    rel: 'site/index.html',
    regions: [
      ['generated:stats-hero', heroStats()],
      ['generated:stats-control-plane', controlPlaneStats()],
    ],
  },
];

let drift = 0;
const tmp = mode === 'check' ? fs.mkdtempSync(path.join(os.tmpdir(), 'docgen-counts-')) : null;

try {
  for (const t of targets) {
    const abs = path.join(root, t.rel);
    if (!fs.existsSync(abs)) die('missing target: ' + t.rel);
    const before = fs.readFileSync(abs, 'utf8');
    let after = before;
    for (const region of t.regions) after = splice(after, region[0], region[1], t.rel);

    if (mode === 'check') {
      if (after === before) continue;
      drift += 1;
      const cand = path.join(tmp, path.basename(t.rel));
      fs.writeFileSync(cand, after);
      // `diff` exits 1 precisely because the files differ — that is the signal
      // being asked for, so the throw is the success path here.
      let out = '';
      try {
        execFileSync('diff', ['-u', abs, cand], { encoding: 'utf8' });
      } catch (err) {
        out = (err && err.stdout) || '';
      }
      console.log('DRIFT: ' + t.rel);
      process.stdout.write(out.endsWith('\n') || out === '' ? out : out + '\n');
    } else if (after !== before) {
      fs.writeFileSync(abs, after);
      console.log('wrote ' + t.rel);
    } else {
      console.log('unchanged ' + t.rel);
    }
  }
} finally {
  if (tmp) fs.rmSync(tmp, { recursive: true, force: true });
}

if (mode === 'check' && drift > 0) {
  console.error(
    'apply-counts: ' + drift + ' file(s) carry numbers the tree no longer supports.'
  );
  console.error('Run `bash scripts/docgen/apply-counts.sh` and commit the result. Published');
  console.error('counts are generated from the corpus — never hand-typed, never estimated.');
  process.exit(1);
}
APPLY

node -e "${DOCGEN_NODE_LIB}${APPLY_JS}"
