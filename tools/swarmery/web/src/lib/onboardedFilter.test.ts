// Unit tests for isOnboarded()'s umbrella-nesting predicate.
//
// The web app ships no committed test runner (CI is `npm run build` only), so
// this suite is dev-only: run it with
//   npx vitest run src/lib/onboardedFilter.test.ts
// (vitest is fetched on demand; it is intentionally NOT a committed dependency.)
// web/tsconfig.json EXCLUDES *.test.ts, and vitest transpiles without type
// checking, so nothing type-checks this file — treat its types as
// documentation.
//
// isOnboarded() has already shipped two wrong versions within this same
// feature branch (plugin !== null, then plain managed === true) before this
// nesting check landed — see the header comment in onboardedFilter.ts. These
// cases pin the current behavior so a third regression fails loudly instead
// of waiting for someone to notice their sidebar looks wrong.

import { describe, expect, it } from 'vitest';
import { isOnboarded } from './onboardedFilter';

interface Row {
  id: number;
  path: string;
  isSystem: boolean;
  archived: boolean;
  plugin: { managed: boolean } | null;
}

function row(over: Partial<Row> = {}): Row {
  return {
    id: 0,
    path: '/home/x/proj',
    isSystem: false,
    archived: false,
    plugin: { managed: true },
    ...over,
  };
}

describe('isOnboarded', () => {
  it('is false when not managed at all', () => {
    const p = row({ id: 1, plugin: null });
    expect(isOnboarded(p, [p])).toBe(false);
  });

  it('is false when managed is explicitly false', () => {
    const p = row({ id: 1, plugin: { managed: false } });
    expect(isOnboarded(p, [p])).toBe(false);
  });

  it('is true for a managed project with no other rows', () => {
    const p = row({ id: 1, path: '/home/x/skygor' });
    expect(isOnboarded(p, [p])).toBe(true);
  });

  it('demotes a managed project nested under another managed project (the umbrella case)', () => {
    const umbrella = row({ id: 1, path: '/home/x/skygor' });
    const subRepo = row({ id: 2, path: '/home/x/skygor/platform/sk-next' });
    expect(isOnboarded(subRepo, [umbrella, subRepo])).toBe(false);
    expect(isOnboarded(umbrella, [umbrella, subRepo])).toBe(true);
  });

  it('does not treat a sibling with a matching path PREFIX as an ancestor', () => {
    // /home/foo is a string-prefix of /home/foobar but not a real parent dir —
    // the '+ "/"' in the startsWith check must reject this.
    const foo = row({ id: 1, path: '/home/foo' });
    const foobar = row({ id: 2, path: '/home/foobar' });
    expect(isOnboarded(foobar, [foo, foobar])).toBe(true);
  });

  it('normalizes trailing slashes on both sides before comparing', () => {
    const umbrella = row({ id: 1, path: '/home/x/skygor/' });
    const subRepo = row({ id: 2, path: '/home/x/skygor/platform/sk-next/' });
    expect(isOnboarded(subRepo, [umbrella, subRepo])).toBe(false);
  });

  it('does not demote a project against itself', () => {
    const p = row({ id: 1, path: '/home/x/skygor' });
    expect(isOnboarded(p, [p])).toBe(true);
  });

  it('ignores the System project as a candidate ancestor', () => {
    const system = row({ id: 1, path: '/home/x', isSystem: true });
    const child = row({ id: 2, path: '/home/x/skygor' });
    expect(isOnboarded(child, [system, child])).toBe(true);
  });

  it('ignores an archived project as a candidate ancestor', () => {
    const archivedParent = row({ id: 1, path: '/home/x/skygor', archived: true });
    const child = row({ id: 2, path: '/home/x/skygor/platform/sk-next' });
    expect(isOnboarded(child, [archivedParent, child])).toBe(true);
  });

  it('does not collapse everything when a candidate ancestor has an empty path', () => {
    const root = row({ id: 1, path: '/', plugin: { managed: true } });
    const child = row({ id: 2, path: '/home/x/skygor' });
    expect(isOnboarded(child, [root, child])).toBe(true);
  });
});
