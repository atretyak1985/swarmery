#!/usr/bin/env bash
# check-plugin-versions.sh — CI gate: marketplace ↔ plugin.json version consistency.
#
# A plugin's version lives in its own .claude-plugin/plugin.json and nowhere
# else: consumers adopt a change through `/plugin update`, which compares that
# number. So a pushed change without a valid bump, or a second copy of the
# number that disagrees, ships nothing or ships the wrong thing.
#
# Checks, against .claude-plugin/marketplace.json:
#   1. every marketplace entry's `source` holds a .claude-plugin/plugin.json
#      that parses, whose `name` equals the entry's name, and whose `version`
#      is a valid SemVer 2.0.0 string;
#   2. an entry that carries its own `version` (none does today) must equal its
#      plugin.json's — the only way a second copy is allowed to exist;
#   3. marketplace `metadata.version` equals core's plugin.json version (the
#      marketplace tracks the core version);
#   4. every plugins/*/.claude-plugin/plugin.json is listed in the marketplace,
#      so no plugin is versioned and silently never distributed.
#
# Bash + `node -e`, like check-plugin-requirements.sh: node is the one runtime
# the CI validate job has. PLUGIN_VERSIONS_ROOT points it at another tree
# (scripts/tests/check-plugin-versions.test.sh uses fixture trees).
#
# Exit: 0 = consistent, 1 = a problem (each printed with ✗).
set -euo pipefail

ROOT="${PLUGIN_VERSIONS_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"

read -r -d '' NODE_SRC <<'NODE' || true
const fs = require('fs');
const path = require('path');

const root = process.env.PV_ROOT;
const rel = (p) => path.relative(root, p) || '.';
const marketPath = path.join(root, '.claude-plugin', 'marketplace.json');

// SemVer 2.0.0, the regex semver.org publishes (numeric parts without leading
// zeros, optional pre-release and build metadata).
const SEMVER = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$/;

let problems = 0;
const note = (msg) => { problems += 1; console.error('  ✗ ' + msg); };

const readJSON = (file) => {
  try {
    return { doc: JSON.parse(fs.readFileSync(file, 'utf8')) };
  } catch (err) {
    return { err: err.code === 'ENOENT' ? 'missing' : 'invalid JSON: ' + err.message };
  }
};

const market = readJSON(marketPath);
if (market.err) {
  console.error('check-plugin-versions: ' + rel(marketPath) + ' ' + market.err);
  process.exit(1);
}
const entries = Array.isArray(market.doc.plugins) ? market.doc.plugins : [];
if (entries.length === 0) {
  console.error('check-plugin-versions: ' + rel(marketPath) + ' lists no plugins');
  process.exit(1);
}

const listed = new Set();
const versions = new Map();

for (const [i, entry] of entries.entries()) {
  const at = 'plugins[' + i + ']';
  const name = entry && typeof entry.name === 'string' ? entry.name : '';
  if (!name) { note(at + ' has no name'); continue; }
  if (typeof entry.source !== 'string' || entry.source.trim() === '') {
    note(name + ': marketplace entry has no string source');
    continue;
  }
  const dir = path.resolve(root, entry.source);
  listed.add(dir);
  const manifest = path.join(dir, '.claude-plugin', 'plugin.json');
  const pj = readJSON(manifest);
  if (pj.err) { note(name + ': ' + rel(manifest) + ' ' + pj.err); continue; }

  if (pj.doc.name !== name) {
    note(name + ': ' + rel(manifest) + ' name is ' + JSON.stringify(pj.doc.name));
  }
  const v = pj.doc.version;
  if (typeof v !== 'string' || !SEMVER.test(v)) {
    note(name + ': ' + rel(manifest) + ' version ' + JSON.stringify(v) + ' is not valid semver');
    continue;
  }
  versions.set(name, v);

  if (entry.version !== undefined && entry.version !== v) {
    note(name + ': marketplace version ' + JSON.stringify(entry.version) +
         ' differs from plugin.json ' + v + ' (drop the marketplace copy or make them equal)');
    continue;
  }
  console.log('  ✓ ' + name + ' ' + v + (entry.version !== undefined ? ' (marketplace copy equal)' : ''));
}

const meta = market.doc.metadata && market.doc.metadata.version;
if (!versions.has('core')) {
  note('no valid core entry, so metadata.version cannot be checked');
} else if (meta !== versions.get('core')) {
  note('marketplace metadata.version ' + JSON.stringify(meta) +
       ' differs from core plugin.json ' + versions.get('core'));
} else {
  console.log('  ✓ metadata.version ' + meta + ' = core');
}

const pluginsDir = path.join(root, 'plugins');
const dirs = fs.existsSync(pluginsDir) ? fs.readdirSync(pluginsDir).sort() : [];
for (const d of dirs) {
  const dir = path.join(pluginsDir, d);
  if (!fs.existsSync(path.join(dir, '.claude-plugin', 'plugin.json'))) continue;
  if (!listed.has(dir)) note(rel(dir) + ' has a plugin.json but no marketplace entry');
}

if (problems > 0) {
  console.error('');
  console.error('check-plugin-versions: ' + problems + ' problem(s). A plugin version lives in its plugin.json;');
  console.error('bump it there (semver), keep metadata.version on core, list every plugin in the marketplace.');
  process.exit(1);
}
console.log('✓ plugin versions consistent (' + versions.size + ' plugins)');
NODE

PV_ROOT="$ROOT" node -e "$NODE_SRC"
