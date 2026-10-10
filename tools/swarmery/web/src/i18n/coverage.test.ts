// @vitest-environment node
//
// Catalog coverage (plan D2): `lingui extract` only reads the files a
// catalog's `include` names, so a macro in a file outside every catalog
// compiles to an id that no .po file ever carries — the string silently stays
// English. This test fails for any source file that imports a Lingui macro
// but falls inside no catalog of lingui.config.ts (after that catalog's
// `exclude`).
//
// Matching mirrors @lingui/cli's catalog.collect(): a glob entry matches as a
// glob, an entry naming a directory matches everything under it, any other
// entry matches that exact file.

import { readdirSync, readFileSync, statSync } from 'node:fs';
import { dirname, join, sep } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import config from '../../lingui.config';

const WEB_DIR = join(dirname(fileURLToPath(import.meta.url)), '..', '..');
const SRC_DIR = join(WEB_DIR, 'src');

const SKIP = /^(?:mock|terminal|test)\/|\.test\.[tj]sx?$|\.d\.ts$/;
const MACRO_IMPORT = /from\s+['"]@lingui\/(?:core|react)\/macro['"]/;
const GLOB_CHARS = /[*?{[]/;

/** web-relative POSIX path of a config entry (`<rootDir>/src/x` → `src/x`). */
function relEntry(entry: string): string {
  return entry.replace(/^<rootDir>\/?/, '').replace(/^\.\//, '');
}

function globToRegExp(glob: string): RegExp {
  let re = '';
  for (let i = 0; i < glob.length; i += 1) {
    const c = glob.charAt(i);
    if (c === '*' && glob.charAt(i + 1) === '*') {
      const slash = glob.charAt(i + 2) === '/';
      re += slash ? '(?:.*/)?' : '.*';
      i += slash ? 2 : 1;
    } else if (c === '*') re += '[^/]*';
    else if (c === '?') re += '[^/]';
    else if (c === '{') re += '(?:';
    else if (c === '}') re += ')';
    else if (c === ',') re += '|';
    else re += c.replace(/[.+^$()|[\]\\]/g, '\\$&');
  }
  return new RegExp(`^${re}$`);
}

/** A predicate over web-relative POSIX paths for one include/exclude entry. */
function matcher(entry: string): (path: string) => boolean {
  const rel = relEntry(entry);
  if (GLOB_CHARS.test(rel)) {
    const re = globToRegExp(rel);
    return (path) => re.test(path);
  }
  let isDir = false;
  try {
    isDir = statSync(join(WEB_DIR, rel)).isDirectory();
  } catch {
    isDir = false;
  }
  return isDir ? (path) => path.startsWith(`${rel}/`) : (path) => path === rel;
}

interface CatalogEntry {
  include: string[];
  exclude?: string[];
}

const catalogs = ((config.catalogs ?? []) as CatalogEntry[]).map((c) => ({
  include: c.include.map(matcher),
  exclude: (c.exclude ?? []).map(matcher),
}));

function inSomeCatalog(path: string): boolean {
  return catalogs.some((c) => c.include.some((m) => m(path)) && !c.exclude.some((m) => m(path)));
}

function macroFiles(): string[] {
  return readdirSync(SRC_DIR, { recursive: true })
    .map((p) => String(p).split(sep).join('/'))
    .filter((p) => /\.tsx?$/.test(p) && !SKIP.test(p))
    .filter((p) => MACRO_IMPORT.test(readFileSync(join(SRC_DIR, p), 'utf8')))
    .map((p) => `src/${p}`)
    .sort();
}

describe('i18n catalog coverage', () => {
  it('matches include entries the way lingui extract does', () => {
    expect(matcher('<rootDir>/src/lib')('src/lib/format.ts')).toBe(true);
    expect(matcher('<rootDir>/src/api')('src/api.ts')).toBe(false);
    expect(matcher('<rootDir>/src/api.ts')('src/api.ts')).toBe(true);
    expect(matcher('**/*.test.*')('src/lib/format.test.ts')).toBe(true);
    expect(matcher('src/pages/**/*.tsx')('src/pages/Today.tsx')).toBe(true);
  });

  it('puts every file that imports a Lingui macro into some catalog', () => {
    const files = macroFiles();
    expect(files.length).toBeGreaterThan(0);
    const orphans = files.filter((f) => !inSomeCatalog(f));
    expect(orphans, 'files with Lingui macros outside every catalog include of lingui.config.ts').toEqual([]);
  });
});
