#!/usr/bin/env node
// new-page.mjs — start a page from the explainer shell, honouring a project override.
//
//   node new-page.mjs <out.html> [--force]
//
// Resolution (the repo-wide template rule — project first, plugin second):
//   1. ${CLAUDE_PROJECT_DIR:-$PWD}/.claude/templates/explainer-shell.html
//   2. <this skill>/templates/explainer-shell.html
// Prints one JSON line: {"template":"<path used>","source":"project|plugin","out":"<path>"}.
// Refuses to overwrite an existing <out.html> unless --force.

import { copyFileSync, existsSync, mkdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const argv = process.argv.slice(2);
const out = argv.find((a) => !a.startsWith('--'));
if (!out || !out.endsWith('.html')) {
  console.error('usage: node new-page.mjs <out.html> [--force]');
  process.exit(2);
}
const projectDir = process.env.CLAUDE_PROJECT_DIR || process.cwd();
const projectShell = path.join(projectDir, '.claude', 'templates', 'explainer-shell.html');
const pluginShell = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', 'templates', 'explainer-shell.html');
const [template, source] = existsSync(projectShell) ? [projectShell, 'project'] : [pluginShell, 'plugin'];

if (existsSync(out) && !argv.includes('--force')) {
  console.error(`new-page.mjs: ${out} exists — pass --force to overwrite`);
  process.exit(1);
}
mkdirSync(path.dirname(path.resolve(out)), { recursive: true });
copyFileSync(template, out);
console.log(JSON.stringify({ template: path.resolve(template), source, out: path.resolve(out) }));
