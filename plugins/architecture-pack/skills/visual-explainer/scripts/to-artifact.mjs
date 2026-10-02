#!/usr/bin/env node
// to-artifact.mjs — turn a full HTML document into the body-level form that publishing
// tools which add their own <!doctype>/<html>/<head>/<body> skeleton expect.
//
// Usage: node to-artifact.mjs <page.html> <out.html>
//
// Keeps <title>, <meta name="description">, <link>, <style> and <script> from <head>, then the
// <body> content. Drops the doctype, <html>, <head>, <body>, and the charset/viewport metas the
// host skeleton provides.

import { readFileSync, writeFileSync } from 'node:fs';

const [src, out] = process.argv.slice(2);
if (!src || !out) {
  console.error('usage: node to-artifact.mjs <page.html> <out.html>');
  process.exit(2);
}
const html = readFileSync(src, 'utf8');
const headM = html.match(/<head\b[^>]*>([\s\S]*?)<\/head>/i);
const bodyM = html.match(/<body\b[^>]*>([\s\S]*)<\/body>/i);
if (!headM || !bodyM) {
  console.error(`${src}: no <head>…</head> and <body>…</body> found — is it already body-level?`);
  process.exit(1);
}
const head = headM[1]
  .replace(/<meta\s+charset[^>]*>\s*/gi, '')
  .replace(/<meta\s+name="viewport"[^>]*>\s*/gi, '')
  .trim();
const title = head.match(/<title>[\s\S]*?<\/title>/i);
if (!title) {
  console.error(`${src}: no <title> — add one before publishing`);
  process.exit(1);
}
// The title must sit in the first 8 KB, so it goes first.
const rest = head.replace(title[0], '').trim();
writeFileSync(out, `${title[0]}\n${rest}\n${bodyM[1].trim()}\n`);
console.log(`${out}: ${Buffer.byteLength(readFileSync(out))} bytes`);
