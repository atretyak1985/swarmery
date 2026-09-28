#!/usr/bin/env node
// Compile every `regex` / `not-regex` assertion in evals/agents/*.yaml with the
// JS RegExp engine promptfoo uses, so a pattern JS rejects (e.g. PCRE's `(?i)`)
// fails CI without a paid model run.
//
// Usage: node evals/scripts/check-regex.mjs [agents-dir]
// Exit:  0 all compile · 1 at least one failure · 2 no YAML parser resolvable
import { readdirSync, readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const evalsDir = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const agentsDir = resolve(process.argv[2] ?? join(evalsDir, 'agents'));

// js-yaml ships with promptfoo (evals/node_modules after `npm ci`); `yaml` is
// the fallback. Resolution also honours NODE_PATH.
function loadYamlParser() {
  const require = createRequire(join(evalsDir, 'package.json'));
  try {
    return require('js-yaml').load;
  } catch {}
  try {
    return require('yaml').parse;
  } catch {}
  return null;
}

const parse = loadYamlParser();
if (!parse) {
  console.error('check-regex: no YAML parser (js-yaml or yaml) resolvable — run `npm ci` in evals/');
  process.exit(2);
}

const failures = [];
const files = readdirSync(agentsDir).filter((f) => f.endsWith('.yaml')).sort();
for (const file of files) {
  const cases = parse(readFileSync(join(agentsDir, file), 'utf8')) ?? [];
  if (!Array.isArray(cases)) continue;
  cases.forEach((c, ci) => {
    const caseName = c?.description ?? `#${ci}`;
    (c?.assert ?? []).forEach((a, ai) => {
      if (a?.type !== 'regex' && a?.type !== 'not-regex') return;
      try {
        new RegExp(a.value);
      } catch (err) {
        failures.push(`${file}:${caseName}:${ai} (${a.type}) ${err.message}`);
      }
    });
  });
}

for (const f of failures) console.error(f);
if (failures.length > 0) {
  console.error(`check-regex: ${failures.length} assertion(s) failed to compile`);
  process.exit(1);
}
console.log(`check-regex: ok — ${files.length} file(s) in ${agentsDir}`);
