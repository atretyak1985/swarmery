// @vitest-environment node
//
// Catalog completeness (plan SC-6): every uk .po entry carries a translation,
// and each translation keeps the placeholders, `#` and ICU plural forms of its
// source message. `lingui compile --strict` (npm run i18n:check) also fails on
// a missing translation; this test is the in-suite mirror of it and adds the
// placeholder check, which compile does not do.

import { readFileSync, readdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

const UK_DIR = join(dirname(fileURLToPath(import.meta.url)), '..', 'locales', 'uk');

interface Entry {
  msgid: string;
  msgstr: string;
  obsolete: boolean;
  /** msgctxt, when the macro call carried `context` — part of the runtime id. */
  context: string;
}

function unquote(s: string): string {
  return JSON.parse(s) as string;
}

/** Minimal .po reader: msgid/msgstr pairs with "…" continuation lines. */
function readPo(text: string): Entry[] {
  const out: Entry[] = [];
  let cur: Partial<Entry> | null = null;
  let key: 'msgid' | 'msgstr' | 'context' | null = null;
  let pendingContext = '';
  for (const raw of text.split('\n')) {
    const obsolete = raw.startsWith('#~ ');
    const line = obsolete ? raw.slice(3) : raw;
    if (line.startsWith('#')) continue;
    if (line.startsWith('msgctxt ')) {
      pendingContext = unquote(line.slice(8));
      cur = { context: pendingContext, msgstr: '', obsolete };
      key = 'context';
    } else if (line.startsWith('msgid ')) {
      if (cur?.msgid) out.push(cur as Entry);
      cur = { msgid: unquote(line.slice(6)), msgstr: '', obsolete, context: cur?.context ?? '' };
      key = 'msgid';
    } else if (line.startsWith('msgstr ') && cur) {
      cur.msgstr = unquote(line.slice(7));
      key = 'msgstr';
    } else if (line.startsWith('"') && cur && key) {
      cur[key] = (cur[key] ?? '') + unquote(line);
    } else if (line.trim() === '') {
      if (cur?.msgid) out.push(cur as Entry);
      cur = null;
      key = null;
    }
  }
  if (cur?.msgid) out.push(cur as Entry);
  return out;
}

const PLACEHOLDER = /\{[A-Za-z0-9_]+\}/g;
const ICU_FORMS = /\b(one|few|many|other)\s*\{/g;

function placeholders(s: string): string[] {
  // ICU plural: the selector `{n, plural, …}` counts as the `{n}` placeholder
  const plain = s.replace(/\{(\w+),\s*(?:plural|select|selectordinal),[\s\S]*\}/g, (_m, v) => `{${v}}`);
  return [...plain.matchAll(PLACEHOLDER)].map((m) => m[0]).sort();
}

describe('uk catalogs', () => {
  const files = readdirSync(UK_DIR).filter((f) => f.endsWith('.po'));
  it('translates a msgid shared by several catalogs the same way everywhere', () => {
    // src/i18n/index.ts merges the four compiled catalogs with Object.assign and
    // Lingui hashes a message id from msgid (+ context), so the same msgid in
    // two catalogs IS one runtime message: the catalog loaded last wins. A
    // different wording per catalog is therefore an accident, never a choice —
    // a real context difference needs `context` in the macro call.
    const seen = new Map<string, Map<string, string>>();
    for (const file of files) {
      for (const e of readPo(readFileSync(join(UK_DIR, file), 'utf8'))) {
        if (e.obsolete || e.msgstr.trim() === '') continue;
        const id = e.context ? `${e.context}\u0004${e.msgid}` : e.msgid;
        const m = seen.get(id) ?? new Map<string, string>();
        m.set(file, e.msgstr);
        seen.set(id, m);
      }
    }
    const conflicts = [...seen.entries()]
      .filter(([, m]) => new Set(m.values()).size > 1)
      .map(([id, m]) => `${id}: ${[...m.entries()].map(([f, v]) => `${f}=${v}`).join(' | ')}`);
    expect(conflicts, conflicts.slice(0, 8).join('\n')).toEqual([]);
  });
  it('has the four catalogs', () => {
    expect(files.sort()).toEqual(['insights-system.po', 'plans.po', 'shared.po', 'today-sessions.po']);
  });
  for (const file of files) {
    const entries = readPo(readFileSync(join(UK_DIR, file), 'utf8')).filter((e) => !e.obsolete);
    it(`${file}: every message is translated`, () => {
      const missing = entries.filter((e) => e.msgstr.trim() === '').map((e) => e.msgid);
      expect(missing, `${missing.length} untranslated: ${missing.slice(0, 5).join(' | ')}`).toEqual([]);
    });
    it(`${file}: translations keep placeholders and plural forms`, () => {
      const bad: string[] = [];
      for (const e of entries) {
        if (e.msgstr.trim() === '') continue;
        const a = placeholders(e.msgid);
        const b = placeholders(e.msgstr);
        if (a.join() !== b.join()) bad.push(`${e.msgid} → ${e.msgstr}`);
        if (/\bplural\b/.test(e.msgid)) {
          const forms = new Set([...e.msgstr.matchAll(ICU_FORMS)].map((m) => m[1]));
          for (const f of ['one', 'few', 'many', 'other']) if (!forms.has(f)) bad.push(`${e.msgid}: missing ${f}`);
        }
        if (e.msgid.includes('#') && /\bplural\b/.test(e.msgid) && !e.msgstr.includes('#')) bad.push(`${e.msgid}: lost #`);
      }
      expect(bad, bad.slice(0, 5).join('\n')).toEqual([]);
    });
  }
});
