#!/usr/bin/env node
// check.mjs — static checks for a single-file HTML explainer page.
//
// Usage: node check.mjs <page.html> [--json]
//
// Exit 0 when no ERROR was found (warnings allowed), 1 on any ERROR, 2 on bad usage.
// Generic checks apply to any page; shell checks run when the page uses the
// explainer shell's attributes (data-d drawer hotspots, data-t glossary terms).
// No dependencies: regex scanning plus node:vm for script syntax.

import { readFileSync } from 'node:fs';
import vm from 'node:vm';

const args = process.argv.slice(2);
const file = args.find((a) => !a.startsWith('--'));
const asJson = args.includes('--json');
if (!file) {
  console.error('usage: node check.mjs <page.html> [--json]');
  process.exit(2);
}

let raw;
try {
  raw = readFileSync(file, 'utf8');
} catch (e) {
  console.error(`cannot read ${file}: ${e.message}`);
  process.exit(2);
}
// Blank out HTML comments (keeping newlines, so line numbers stay true): the shell
// documents its own markup inside comments, and that must not count as markup.
const html = raw.replace(/<!--[\s\S]*?-->/g, (m) => m.replace(/[^\n]/g, ' '));

const findings = [];
const add = (level, code, msg) => findings.push({ level, code, msg });
const lineOf = (idx) => html.slice(0, idx).split('\n').length;

// Strip <template> and <script> bodies for structural checks that must not
// count drawer content twice or match strings inside JS.
const scriptRe = /<script\b([^>]*)>([\s\S]*?)<\/script[^>]*>/gi;
const templateRe = /<template\b[^>]*>[\s\S]*?<\/template>/gi;
const withoutScripts = html.replace(scriptRe, (m) => ' '.repeat(m.length));

// ---------- size & title ----------
const bytes = Buffer.byteLength(raw, 'utf8');
if (bytes > 16 * 1024 * 1024) add('ERROR', 'size', `page is ${(bytes / 1048576).toFixed(1)} MB; the limit is 16 MB`);
const head8k = raw.slice(0, 8192);
const title = (head8k.match(/<title>([\s\S]*?)<\/title>/i) || [])[1];
if (!title) add('ERROR', 'title', 'no <title> in the first 8 KB');
else if (/[—:|]\s/.test(title) && title.length > 40) add('WARN', 'title', `title reads like a caption: "${title.trim()}" — use a short name`);

// ---------- ids ----------
const ids = new Map();
for (const m of withoutScripts.matchAll(/\sid="([^"]+)"/g)) {
  const list = ids.get(m[1]) || [];
  list.push(lineOf(m.index));
  ids.set(m[1], list);
}
for (const [id, lines] of ids) {
  if (lines.length > 1) add('ERROR', 'dup-id', `id="${id}" is used ${lines.length} times (lines ${lines.join(', ')})`);
}

// url(#x) and href="#x" must resolve
for (const m of html.matchAll(/url\(#([\w.-]+)\)/g)) {
  if (!ids.has(m[1])) add('ERROR', 'dangling-ref', `url(#${m[1]}) at line ${lineOf(m.index)} has no element with that id`);
}
for (const m of withoutScripts.matchAll(/\shref="#([\w.-]+)"/g)) {
  if (!ids.has(m[1])) add('WARN', 'dangling-anchor', `href="#${m[1]}" at line ${lineOf(m.index)} points nowhere`);
}

// ---------- scripts ----------
let scriptCount = 0;
for (const m of html.matchAll(scriptRe)) {
  const attrs = m[1] || '';
  const body = m[2] || '';
  const srcM = attrs.match(/\ssrc="([^"]+)"/);
  if (srcM) {
    const ok = /^https:\/\/(cdnjs\.cloudflare\.com|cdn\.jsdelivr\.net\/npm\/|unpkg\.com|cdn\.tailwindcss\.com|code\.jquery\.com)/.test(srcM[1]);
    if (!ok && !/^(\.\/|[\w-]+\.js$)/.test(srcM[1])) add('WARN', 'cdn', `script src ${srcM[1]} is outside the artifact CDN allowlist`);
    if (/^https:/.test(srcM[1]) && !/\d+\.\d+/.test(srcM[1])) add('WARN', 'cdn-pin', `script src ${srcM[1]} has no pinned version`);
    continue;
  }
  const type = (attrs.match(/\stype="([^"]+)"/) || [])[1] || '';
  if (/json/.test(type)) {
    try { JSON.parse(body); } catch (e) { add('ERROR', 'json-island', `JSON data island at line ${lineOf(m.index)} does not parse: ${e.message}`); }
    if (/<\/script/i.test(body)) add('ERROR', 'json-island', `JSON data island at line ${lineOf(m.index)} contains "</script" — escape it as "<\\/script"`);
    continue;
  }
  if (type && !/javascript|module/.test(type)) continue;
  scriptCount++;
  try {
    new vm.Script(type === 'module' ? `(async()=>{${body.replace(/^\s*import\s.*$/gm, '')}})` : body, { filename: `inline-script-${scriptCount}` });
  } catch (e) {
    add('ERROR', 'js-syntax', `inline script #${scriptCount} (line ${lineOf(m.index)}): ${e.message}`);
  }
}

// ---------- external stylesheets ----------
for (const m of withoutScripts.matchAll(/<link\b[^>]*rel="stylesheet"[^>]*href="([^"]+)"/gi)) {
  if (!/^https:\/\/fonts\.googleapis\.com\//.test(m[1])) add('WARN', 'css-host', `stylesheet ${m[1]} is not from Google Fonts — inline it`);
  else add('WARN', 'external-font', 'web fonts load from Google: every viewer\'s browser contacts it and the file is not offline-ready — keep it only for a page published online with the user\'s consent');
}

// ---------- theme ----------
const css = (html.match(/<style\b[^>]*>([\s\S]*?)<\/style>/gi) || []).join('\n');
const hasDarkMedia = /prefers-color-scheme:\s*dark/.test(css);
const hasDarkAttr = /\[data-theme="dark"\]/.test(css);
const hasLightFirstDark = /prefers-color-scheme:\s*light/.test(css);
if (!hasDarkMedia && !hasLightFirstDark) add('WARN', 'theme', 'no prefers-color-scheme block — the page ignores the viewer theme');
if (hasDarkMedia && !hasDarkAttr) add('WARN', 'theme', 'dark tokens exist only under the media query; add :root[data-theme="dark"] so an explicit toggle wins');
if (!/body\s*\{[^}]*background/.test(css)) add('WARN', 'body-bg', 'body has no explicit background — a transparent body shows the host theme');
// literal colors inside component rules (outside :root / theme blocks) — heuristic
const cssNoTokens = css.replace(/:root[^{]*\{[^}]*\}/g, '').replace(/@media[^{]*\{\s*:root[^{]*\{[^}]*\}\s*\}/g, '');
const literalColors = (cssNoTokens.match(/(?<![-\w])(color|background|fill|stroke|border(-color)?)\s*:\s*#[0-9a-f]{3,8}\b/gi) || []).length;
if (literalColors > 6) add('WARN', 'literal-colors', `${literalColors} component rules use literal hex colors instead of tokens — they will not follow the theme`);

// ---------- hidden attribute vs display ----------
if (/\shidden(\s|>|=)/.test(withoutScripts) || /\.hidden\s*=/.test(html)) {
  if (!/\[hidden\]\s*\{[^}]*display:\s*none\s*!important/.test(css)) add('WARN', 'hidden', 'page toggles the hidden attribute but has no [hidden]{display:none!important} — any display:flex/grid rule overrides it');
}

// ---------- monospace ligatures ----------
if (/JetBrains Mono|Fira Code|Cascadia|Monaspace/i.test(html) && !/font-variant-ligatures:\s*none|"calt"\s*0/.test(css)) {
  add('WARN', 'ligatures', 'a ligature monospace font is used without font-variant-ligatures:none — "-<", "->", "=>" in code turn into arrows');
}

// ---------- svg accessibility ----------
let svgCount = 0;
for (const m of withoutScripts.replace(templateRe, '').matchAll(/<svg\b([^>]*)>/gi)) {
  const a = m[1];
  if (/aria-hidden="true"/.test(a) || /width="0"/.test(a)) continue;
  svgCount++;
  if (!/role="img"/.test(a) || !/aria-label="[^"]{8,}"/.test(a)) {
    if (/viewBox="0 0 (\d+)/.test(a) && +RegExp.$1 > 100) add('WARN', 'svg-a11y', `diagram <svg> at line ${lineOf(m.index)} lacks role="img" with an aria-label stating its claim`);
  }
}

// ---------- shell contracts ----------
const usesShell = /\sdata-d="/.test(html) || /\sdata-t="/.test(html);
let stats = {};
if (usesShell) {
  const templates = new Set([...html.matchAll(/<template\s+id="d-([^"]+)"/g)].map((m) => m[1]));
  let glossary = null;
  const gm = html.match(/<script[^>]*id="glossary"[^>]*>([\s\S]*?)<\/script>/);
  if (gm) { try { glossary = JSON.parse(gm[1]); } catch { /* reported above */ } }
  let registry = null;
  const rm = html.match(/<script[^>]*id="details"[^>]*>([\s\S]*?)<\/script>/);
  if (rm) { try { registry = JSON.parse(rm[1]); } catch { /* reported above */ } }
  const dKeys = new Set([...html.matchAll(/\sdata-d="([^"]+)"/g)].map((m) => m[1]));
  const tKeys = new Set([...html.matchAll(/\sdata-t="([^"]+)"/g)].map((m) => m[1]));
  for (const k of dKeys) {
    if (k.includes("'") || k.includes('+')) continue; // built in JS
    if (!templates.has(k) && !(registry && Object.prototype.hasOwnProperty.call(registry, k))) add('ERROR', 'drawer-missing', `data-d="${k}" has no <template id="d-${k}"> and no entry in the #details registry`);
  }
  for (const k of templates) if (!dKeys.has(k)) add('WARN', 'drawer-unused', `<template id="d-${k}"> is never opened by any data-d`);
  if (tKeys.size && !glossary) add('ERROR', 'glossary-missing', 'data-t terms are used but there is no <script type="application/json" id="glossary">');
  if (glossary) for (const k of tKeys) if (!Object.prototype.hasOwnProperty.call(glossary, k)) add('ERROR', 'glossary-key', `data-t="${k}" has no glossary entry`);
  stats = { drawerTemplates: templates.size, drawerHotspots: dKeys.size, glossaryTerms: tKeys.size };
}

// ---------- structure stats ----------
const bodyOnly = withoutScripts.replace(templateRe, '');
stats = {
  bytes,
  sections: (bodyOnly.match(/<section\b/gi) || []).length,
  figures: (bodyOnly.match(/<figure\b/gi) || []).length,
  diagrams: svgCount,
  tables: (bodyOnly.match(/<table\b/gi) || []).length,
  inlineScripts: scriptCount,
  ...stats,
};
if (stats.sections > 2 && stats.figures + stats.diagrams === 0) add('WARN', 'no-visuals', 'several sections and not one figure or diagram — the page is prose with styling');

const errors = findings.filter((f) => f.level === 'ERROR').length;
if (asJson) {
  console.log(JSON.stringify({ file, errors, warnings: findings.length - errors, stats, findings }, null, 2));
} else {
  console.log(`${file}`);
  console.log(`  ${Object.entries(stats).map(([k, v]) => `${k}=${v}`).join('  ')}`);
  for (const f of findings) console.log(`  ${f.level.padEnd(5)} ${f.code.padEnd(16)} ${f.msg}`);
  console.log(errors ? `  ✗ ${errors} error(s)` : `  ✓ no errors (${findings.length} warning(s))`);
}
process.exit(errors ? 1 : 0);
