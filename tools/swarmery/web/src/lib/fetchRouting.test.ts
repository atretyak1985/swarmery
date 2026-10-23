// @vitest-environment node
//
// Every request this app makes goes through lib/dashboardSession.ts's
// apiFetch: that is what puts the dashboard session on a write and notices a
// `dashboard_locked` refusal. This scan fails on a raw `fetch` anywhere else
// in src/ (tests excepted) — a call, a `window.`/`globalThis.`/`self.` member,
// or `fetch` passed or aliased as a value. Reads are held to the same rule:
// a method chosen at run time cannot be told apart from a read by a scan.

import { readdirSync, readFileSync } from 'node:fs';
import { dirname, join, relative, sep } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

const SRC = join(dirname(fileURLToPath(import.meta.url)), '..');
const HELPER = join('lib', 'dashboardSession.ts');
const SOURCE = /\.(ts|tsx|js|jsx|mjs)$/;
const TEST = /\.test\.[jt]sx?$/;

const RAW_FETCH: RegExp[] = [
  // fetch(…) — a call on the global, not apiFetch( or obj.fetch(.
  /(?<![\w$.])fetch\s*\(/,
  // window.fetch / globalThis.fetch / self.fetch / window['fetch'].
  /\b(?:window|globalThis|self)\s*(?:\??\.\s*fetch\b|\[\s*['"`]fetch['"`]\s*\])/,
  // fetch as a value: `const f = fetch;`, `call(fetch)`, `[fetch]`.
  /(?<![\w$.'"`])fetch\s*(?=[,;)\]}]|$)/m,
];

/** Comments out: prose about "a fetch" is not a call. */
function stripComments(source: string): string {
  return source.replace(/\/\*[\s\S]*?\*\//g, '').replace(/(^|[^:\\])\/\/.*$/gm, '$1');
}

function rawFetchLines(source: string): string[] {
  return stripComments(source)
    .split('\n')
    .filter((line) => RAW_FETCH.some((re) => re.test(line)))
    .map((line) => line.trim());
}

function sourceFiles(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) return sourceFiles(path);
    return SOURCE.test(entry.name) && !TEST.test(entry.name) ? [path] : [];
  });
}

describe('fetch routing', () => {
  it('the scanner catches each raw form and passes the helper', () => {
    for (const bad of [
      "await fetch('/api/x', { method: 'POST' })",
      'const res = await fetch (path, init)',
      'window.fetch(path)',
      "globalThis['fetch'](path)",
      'const send = fetch;',
      'retry(fetch)',
    ]) {
      expect(rawFetchLines(bad), bad).toHaveLength(1);
    }
    for (const ok of [
      "await apiFetch('/api/x', { method: 'POST' })",
      'fetchProjects()',
      'client.fetch(x)',
      "'http.fetch': msg`network`",
      '// a raw fetch( in a comment',
      '/* window.fetch(x) */',
    ]) {
      expect(rawFetchLines(ok), ok).toEqual([]);
    }
  });

  it('no source file outside lib/dashboardSession.ts calls fetch directly', () => {
    const offenders = sourceFiles(SRC)
      .filter((file) => relative(SRC, file) !== HELPER)
      .flatMap((file) =>
        rawFetchLines(readFileSync(file, 'utf8')).map((line) => `${relative(SRC, file).split(sep).join('/')}: ${line}`),
      );
    expect(offenders, 'route these through apiFetch (lib/dashboardSession.ts)').toEqual([]);
  });
});
