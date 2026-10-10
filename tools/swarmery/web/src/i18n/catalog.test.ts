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
}

function unquote(s: string): string {
  return JSON.parse(s) as string;
}

/** Minimal .po reader: msgid/msgstr pairs with "…" continuation lines. */
function readPo(text: string): Entry[] {
  const out: Entry[] = [];
  let cur: Partial<Entry> | null = null;
  let key: 'msgid' | 'msgstr' | null = null;
  for (const raw of text.split('\n')) {
    const obsolete = raw.startsWith('#~ ');
    const line = obsolete ? raw.slice(3) : raw;
    if (line.startsWith('#')) continue;
    if (line.startsWith('msgid ')) {
      if (cur?.msgid) out.push(cur as Entry);
      cur = { msgid: unquote(line.slice(6)), msgstr: '', obsolete };
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
