// Unit tests for the terminal dock's v2 layout model (terminal-dock-webstorm
// phase 3): migration, normalization invariants, tree operations, geometry.
// Pure logic, no DOM. Node ids come from an injected deterministic IdGen.
//
// On its own: `npm --prefix tools/swarmery/web test -- src/terminal/layout.test.ts`.
// web/tsconfig.json EXCLUDES *.test.ts and vitest does not type-check, so the
// types here are documentation.

import { describe, expect, it } from 'vitest';
import {
  type DockStateV2,
  type DockTab,
  type IdGen,
  type LayoutGroup,
  type LayoutNode,
  type LayoutSplit,
  type Rect,
  type SplitDir,
  activateTab,
  addTab,
  allGroups,
  closeTab,
  dropTarget,
  emptyDockV2,
  focusGroup,
  groupOf,
  layoutRects,
  migrateDock,
  moveTab,
  normalizeDock,
  reorderTab,
  resizeSplit,
  splitWithTab,
} from './layout';

// --- builders --------------------------------------------------------------

function seqGen(prefix = 'n'): IdGen {
  let n = 0;
  return () => `${prefix}${++n}`;
}

function tab(id: string, extra: Partial<DockTab> = {}): DockTab {
  return { id, label: id.toUpperCase(), cwd: `/p/${id}`, ...extra };
}

function group(id: string, tabIds: string[], activeId: string | null = tabIds[0] ?? null): LayoutGroup {
  return { kind: 'group', id, tabIds, activeId };
}

function split(id: string, dir: SplitDir, children: LayoutNode[], sizes?: number[]): LayoutSplit {
  return { kind: 'split', id, dir, sizes: sizes ?? children.map(() => 1 / children.length), children };
}

/** A state whose tabs are every id referenced by the layout (plus `extraTabs`). */
function state(layout: LayoutNode, focusedGroupId?: string, extraTabs: string[] = []): DockStateV2 {
  const ids = [...allGroups(layout).flatMap((g) => g.tabIds), ...extraTabs];
  const unique = [...new Set(ids)];
  return {
    version: 2,
    open: true,
    tabs: unique.map((id) => tab(id)),
    layout,
    focusedGroupId: focusedGroupId ?? allGroups(layout)[0]?.id ?? 'g-root',
  };
}

function deepFreeze<T>(v: T): T {
  if (typeof v === 'object' && v !== null) {
    for (const child of Object.values(v)) deepFreeze(child);
    Object.freeze(v);
  }
  return v;
}

function sum(xs: number[]): number {
  return xs.reduce((a, b) => a + b, 0);
}

/** Every split in the tree satisfies the sizes invariant. */
function expectSizesValid(node: LayoutNode): void {
  if (node.kind === 'group') return;
  expect(node.sizes).toHaveLength(node.children.length);
  expect(sum(node.sizes)).toBeCloseTo(1, 9);
  for (const s of node.sizes) expect(s).toBeGreaterThanOrEqual(0.1 - 1e-9);
  for (const c of node.children) expectSizesValid(c);
}

// --- migrateDock -----------------------------------------------------------

describe('migrateDock', () => {
  it('v1 with 3 tabs → one root group, same order and activeId, run/sessionId kept (SC-9)', () => {
    const v1 = {
      open: true,
      tabs: [
        { id: 'a', label: 'Shell', cwd: '/repo' },
        { id: 'b', label: 'Claude Code', cwd: '/repo/wt', run: 'claude' },
        { id: 'c', label: 'Session', cwd: '/repo', sessionId: 'sess-1', run: 'claude --resume sess-1' },
      ],
      activeId: 'b',
    };
    const v2 = migrateDock(v1, seqGen());
    expect(v2).toEqual({
      version: 2,
      open: true,
      tabs: v1.tabs,
      layout: { kind: 'group', id: 'g-root', tabIds: ['a', 'b', 'c'], activeId: 'b' },
      focusedGroupId: 'g-root',
    });
  });

  it('v1 keeps a closed dock closed', () => {
    const v2 = migrateDock({ open: false, tabs: [{ id: 'a', label: 'A', cwd: '/' }], activeId: 'a' });
    expect(v2.open).toBe(false);
    expect(v2.tabs).toHaveLength(1);
  });

  it('v1 with a stale activeId falls back to the first tab', () => {
    const v2 = migrateDock({ open: true, tabs: [tab('a'), tab('b')], activeId: 'gone' });
    expect(v2.layout).toEqual(group('g-root', ['a', 'b'], 'a'));
  });

  it('drops tabs without a string id/cwd and strips unknown fields', () => {
    const v2 = migrateDock({
      open: true,
      tabs: [
        { id: 1, label: 'num id', cwd: '/' },
        { id: 'nocwd', label: 'x' },
        null,
        'str',
        { id: 'ok', label: 'Ok', cwd: '/ok', junk: 1, run: 42, sessionId: null },
      ],
      activeId: 'ok',
    });
    expect(v2.tabs).toEqual([{ id: 'ok', label: 'Ok', cwd: '/ok' }]);
    expect(v2.layout).toEqual(group('g-root', ['ok'], 'ok'));
  });

  it.each([
    ['null', null],
    ['undefined', undefined],
    ['a string', 'garbage'],
    ['a number', 42],
    ['an array', [1, 2]],
    ['an empty object', {}],
    ['tabs not an array', { tabs: 'x' }],
    ['an unknown version', { version: 3, open: true, tabs: [tab('a')] }],
  ])('%s → emptyDockV2', (_name, raw) => {
    expect(migrateDock(raw, seqGen())).toEqual(emptyDockV2());
  });

  it('v2 round-trips through JSON unchanged', () => {
    const gen = seqGen();
    let s = emptyDockV2();
    s = addTab(s, tab('a'));
    s = addTab(s, tab('b', { run: 'claude', sessionId: 'sess-9' }));
    s = addTab(s, tab('c'));
    s = splitWithTab(s, 'b', 'g-root', 'right', gen);
    s = splitWithTab(s, 'c', 'g-root', 'bottom', gen);
    const back = migrateDock(JSON.parse(JSON.stringify(s)), seqGen('x'));
    expect(back).toEqual(s);
  });

  it('v2 with a broken layout puts every tab in one root group', () => {
    const v2 = migrateDock({ version: 2, open: true, tabs: [tab('a'), tab('b')], layout: 'nope', focusedGroupId: 7 });
    expect(v2.layout).toEqual(group('g-root', ['a', 'b'], 'a'));
    expect(v2.focusedGroupId).toBe('g-root');
  });

  it('v2 gives a missing or duplicate node id a generated one', () => {
    const v2 = migrateDock(
      {
        version: 2,
        open: true,
        tabs: [tab('a'), tab('b')],
        layout: {
          kind: 'split',
          id: 's',
          dir: 'row',
          sizes: [0.5, 0.5],
          children: [
            { kind: 'group', id: 'g', tabIds: ['a'], activeId: 'a' },
            { kind: 'group', id: 'g', tabIds: ['b'], activeId: 'b' },
          ],
        },
        focusedGroupId: 'g',
      },
      seqGen(),
    );
    expect(allGroups(v2.layout).map((g) => g.id)).toEqual(['g', 'n1']);
  });

  it('v2 drops invalid children and keeps the survivors sizes aligned', () => {
    const v2 = migrateDock(
      {
        version: 2,
        open: true,
        tabs: [tab('a'), tab('b')],
        layout: {
          kind: 'split',
          id: 's',
          dir: 'column',
          sizes: [0.6, 0.1, 0.3],
          children: [group('g1', ['a']), { kind: 'weird' }, group('g2', ['b'])],
        },
        focusedGroupId: 'g1',
      },
      seqGen(),
    );
    const layout = v2.layout as LayoutSplit;
    expect(layout.children.map((c) => c.id)).toEqual(['g1', 'g2']);
    expect(layout.sizes[0]).toBeCloseTo(2 / 3, 9);
    expect(layout.sizes[1]).toBeCloseTo(1 / 3, 9);
  });
});

// --- normalizeDock ---------------------------------------------------------

describe('normalizeDock', () => {
  it('keeps a tab id duplicated across groups only in the first group', () => {
    const s = state(split('r', 'row', [group('g1', ['a', 'b']), group('g2', ['b', 'c'], 'b')]));
    const n = normalizeDock(s);
    expect(allGroups(n.layout)).toEqual([group('g1', ['a', 'b'], 'a'), group('g2', ['c'], 'c')]);
  });

  it('dedupes the tab list itself (first wins)', () => {
    const s = { ...state(group('g1', ['a'])), tabs: [tab('a'), tab('a', { label: 'second' })] };
    expect(normalizeDock(s).tabs).toEqual([tab('a')]);
  });

  it('appends an orphan tab to the focused group', () => {
    const s = state(split('r', 'row', [group('g1', ['a']), group('g2', ['b'])]), 'g2', ['c']);
    const n = normalizeDock(s);
    expect(groupOf(n, 'c')?.id).toBe('g2');
    expect(allGroups(n.layout)[1]?.tabIds).toEqual(['b', 'c']);
  });

  it('appends an orphan to the first group when the focused id is unknown', () => {
    const s = state(split('r', 'row', [group('g1', ['a']), group('g2', ['b'])]), 'ghost', ['c']);
    const n = normalizeDock(s);
    expect(groupOf(n, 'c')?.id).toBe('g1');
    expect(n.focusedGroupId).toBe('g1');
  });

  it('drops a tab id that has no tab', () => {
    const s = { ...state(group('g1', ['a'])), layout: group('g1', ['a', 'ghost'], 'ghost') };
    const n = normalizeDock(s);
    expect(n.layout).toEqual(group('g1', ['a'], 'a'));
  });

  it('removes an empty group and collapses the single-child split left behind', () => {
    const s = state(split('r', 'row', [group('g1', ['a']), group('g2', [])]));
    expect(normalizeDock(s).layout).toEqual(group('g1', ['a'], 'a'));
  });

  it('removes an empty group from a 3-way split and renormalizes the rest proportionally', () => {
    const s = state(split('r', 'row', [group('g1', ['a']), group('g2', []), group('g3', ['b'])], [0.5, 0.25, 0.25]));
    const layout = normalizeDock(s).layout as LayoutSplit;
    expect(layout.children.map((c) => c.id)).toEqual(['g1', 'g3']);
    expect(layout.sizes[0]).toBeCloseTo(2 / 3, 9);
    expect(layout.sizes[1]).toBeCloseTo(1 / 3, 9);
  });

  it('keeps an empty group when it is the only node', () => {
    expect(normalizeDock(emptyDockV2())).toEqual(emptyDockV2());
  });

  it('collapses an all-empty tree to a single empty group, keeping the focused id', () => {
    const s = state(split('r', 'row', [group('g1', []), group('g2', [])]), 'g2');
    const n = normalizeDock(s);
    expect(n.layout).toEqual(group('g2', [], null));
    expect(n.focusedGroupId).toBe('g2');
  });

  it('replaces a single-child split with its child', () => {
    const s = state(split('c', 'column', [group('g1', ['a'])]));
    expect(normalizeDock(s).layout).toEqual(group('g1', ['a'], 'a'));
  });

  it('flattens a nested same-dir split into its parent with scaled sizes', () => {
    const s = state(
      split('r', 'row', [group('g1', ['a']), split('r2', 'row', [group('g2', ['b']), group('g3', ['c'])], [0.5, 0.5])], [
        0.4, 0.6,
      ]),
    );
    const layout = normalizeDock(s).layout as LayoutSplit;
    expect(layout.id).toBe('r');
    expect(layout.children.map((c) => c.id)).toEqual(['g1', 'g2', 'g3']);
    expect(layout.sizes[0]).toBeCloseTo(0.4, 9);
    expect(layout.sizes[1]).toBeCloseTo(0.3, 9);
    expect(layout.sizes[2]).toBeCloseTo(0.3, 9);
  });

  it('keeps a nested split of the other dir', () => {
    const inner = split('c', 'column', [group('g2', ['b']), group('g3', ['c'])]);
    const s = state(split('r', 'row', [group('g1', ['a']), inner]));
    expect(normalizeDock(s).layout).toEqual(s.layout);
  });

  it('collapses then flattens: row[g1, column[row[g2, g3]]] → row[g1, g2, g3]', () => {
    const s = state(
      split('r', 'row', [
        group('g1', ['a']),
        split('c', 'column', [split('r2', 'row', [group('g2', ['b']), group('g3', ['c'])])]),
      ]),
    );
    const layout = normalizeDock(s).layout as LayoutSplit;
    expect(layout.kind).toBe('split');
    expect(layout.dir).toBe('row');
    expect(layout.children.map((c) => c.id)).toEqual(['g1', 'g2', 'g3']);
    expectSizesValid(layout);
  });

  it('renormalizes sizes: scale, floor 0.1, wrong length', () => {
    const scaled = normalizeDock(state(split('r', 'row', [group('g1', ['a']), group('g2', ['b'])], [2, 2])));
    expect((scaled.layout as LayoutSplit).sizes).toEqual([0.5, 0.5]);

    const floored = normalizeDock(
      state(split('r', 'row', [group('g1', ['a']), group('g2', ['b']), group('g3', ['c'])], [0.98, 0.01, 0.01])),
    );
    const fs = (floored.layout as LayoutSplit).sizes;
    expect(fs[0]).toBeCloseTo(0.8, 9);
    expect(fs[1]).toBeCloseTo(0.1, 9);
    expect(fs[2]).toBeCloseTo(0.1, 9);
    expectSizesValid(floored.layout);

    const wrongLen = normalizeDock(state(split('r', 'row', [group('g1', ['a']), group('g2', ['b'])], [1])));
    expect((wrongLen.layout as LayoutSplit).sizes).toEqual([0.5, 0.5]);

    const junk = normalizeDock(
      state(split('r', 'row', [group('g1', ['a']), group('g2', ['b'])], [Number.NaN, -3])),
    );
    expect((junk.layout as LayoutSplit).sizes).toEqual([0.5, 0.5]);
  });

  it('fixes an activeId outside the group to the first tab', () => {
    const s = state(group('g1', ['a', 'b'], 'zzz'));
    expect((normalizeDock(s).layout as LayoutGroup).activeId).toBe('a');
  });

  it('points focusedGroupId at an existing group', () => {
    const s = state(split('r', 'row', [group('g1', ['a']), group('g2', ['b'])]), 'gone');
    expect(normalizeDock(s).focusedGroupId).toBe('g1');
  });

  it('is idempotent on messy input', () => {
    const messy: DockStateV2[] = [
      state(split('r', 'row', [group('g1', ['a', 'b']), group('g2', ['b', 'ghost'], 'ghost')], [0.9, 0.0001]), 'x', [
        'c',
      ]),
      state(
        split('r', 'row', [
          group('g1', ['a']),
          split('r2', 'row', [group('g2', ['b']), split('c', 'column', [group('g3', ['c'])])], [0.07, 0.93]),
          group('g4', []),
        ], [0.33, 0.33, 0.34]),
      ),
      state(
        split('c', 'column', [
          split('r', 'row', [group('g1', ['a']), group('g2', ['b']), group('g3', ['c'])], [0.01, 0.01, 0.98]),
          group('g4', ['d']),
        ], [1, 3]),
      ),
      emptyDockV2(),
    ];
    for (const s of messy) {
      const once = normalizeDock(s);
      expect(normalizeDock(once)).toEqual(once);
      expectSizesValid(once.layout);
    }
  });

  it('does not mutate its input', () => {
    const s = deepFreeze(
      state(split('r', 'row', [group('g1', ['a', 'b']), split('r2', 'row', [group('g2', ['b']), group('g3', [])])]), 'x', [
        'c',
      ]),
    );
    const copy = structuredClone(s);
    normalizeDock(s);
    expect(s).toEqual(copy);
  });
});

// --- operations ------------------------------------------------------------

describe('addTab', () => {
  it('adds the first tab to the root group, activates it and opens the dock', () => {
    const s = addTab(emptyDockV2(), tab('a'));
    expect(s).toEqual({
      version: 2,
      open: true,
      tabs: [tab('a')],
      layout: group('g-root', ['a'], 'a'),
      focusedGroupId: 'g-root',
    });
  });

  it('adds to a named group and focuses it', () => {
    const s = addTab(state(split('r', 'row', [group('g1', ['a']), group('g2', ['b'])]), 'g1'), tab('c'), 'g2');
    expect(groupOf(s, 'c')).toEqual(group('g2', ['b', 'c'], 'c'));
    expect(s.focusedGroupId).toBe('g2');
  });

  it('re-adding an existing tab id only activates it', () => {
    const base = { ...state(group('g1', ['a', 'b'], 'a')), open: false };
    const s = addTab(base, tab('b'));
    expect(s.tabs).toHaveLength(2);
    expect((s.layout as LayoutGroup).activeId).toBe('b');
    expect(s.open).toBe(true);
  });
});

describe('splitWithTab', () => {
  const base = (): DockStateV2 => state(group('g1', ['a', 'b'], 'b'));

  it.each([
    ['right', 'row', ['g1', 'n1']],
    ['left', 'row', ['n1', 'g1']],
    ['bottom', 'column', ['g1', 'n1']],
    ['top', 'column', ['n1', 'g1']],
  ] as const)('%s → a %s split in the right order', (edge, dir, order) => {
    const s = splitWithTab(base(), 'b', 'g1', edge, seqGen());
    const layout = s.layout as LayoutSplit;
    expect(layout).toMatchObject({ kind: 'split', id: 'n2', dir, sizes: [0.5, 0.5] });
    expect(layout.children.map((c) => c.id)).toEqual(order);
    expect(groupOf(s, 'a')?.id).toBe('g1');
    expect(groupOf(s, 'b')).toEqual(group('n1', ['b'], 'b'));
    expect(groupOf(s, 'a')?.activeId).toBe('a');
    expect(s.focusedGroupId).toBe('n1');
  });

  it('into a parent of the same dir inserts a sibling, not a nested split', () => {
    const s0 = state(split('r', 'row', [group('g1', ['a', 'b']), group('g2', ['c'])]));
    const right = splitWithTab(s0, 'b', 'g1', 'right', seqGen());
    const lr = right.layout as LayoutSplit;
    expect(lr.id).toBe('r');
    expect(lr.children.map((c) => c.id)).toEqual(['g1', 'n1', 'g2']);
    expect(lr.children.every((c) => c.kind === 'group')).toBe(true);
    expect(lr.sizes).toEqual([0.25, 0.25, 0.5]);

    const left = splitWithTab(s0, 'b', 'g2', 'left', seqGen());
    const ll = left.layout as LayoutSplit;
    expect(ll.children.map((c) => c.id)).toEqual(['g1', 'n1', 'g2']);
    expect(ll.sizes).toEqual([0.5, 0.25, 0.25]);
  });

  it('splitting a group by its sole tab is a no-op', () => {
    const solo = state(group('g1', ['a']));
    expect(splitWithTab(solo, 'a', 'g1', 'right', seqGen())).toEqual(normalizeDock(solo));
    const pair = state(split('r', 'row', [group('g1', ['a']), group('g2', ['b'])]));
    expect(splitWithTab(pair, 'a', 'g1', 'bottom', seqGen())).toEqual(normalizeDock(pair));
  });

  it('nests a row inside a column', () => {
    const s0 = state(split('c', 'column', [group('g1', ['a', 'b']), group('g2', ['c'])]));
    const s = splitWithTab(s0, 'b', 'g1', 'right', seqGen());
    const col = s.layout as LayoutSplit;
    expect(col).toMatchObject({ id: 'c', dir: 'column', sizes: [0.5, 0.5] });
    const row = col.children[0] as LayoutSplit;
    expect(row).toMatchObject({ kind: 'split', id: 'n2', dir: 'row', sizes: [0.5, 0.5] });
    expect(row.children.map((c) => c.id)).toEqual(['g1', 'n1']);
    expect(col.children[1]?.id).toBe('g2');
  });

  it('dragging another group’s sole tab to an edge removes the emptied source', () => {
    const s0 = state(split('r', 'row', [group('g1', ['a']), group('g2', ['b', 'c'])]));
    const s = splitWithTab(s0, 'a', 'g2', 'bottom', seqGen());
    const col = s.layout as LayoutSplit;
    expect(col).toMatchObject({ kind: 'split', id: 'n2', dir: 'column' });
    expect(col.children.map((c) => c.id)).toEqual(['g2', 'n1']);
    expect(allGroups(s.layout).some((g) => g.id === 'g1')).toBe(false);
  });

  it('does not mutate its input', () => {
    const s0 = deepFreeze(state(split('r', 'row', [group('g1', ['a', 'b']), group('g2', ['c'])])));
    expect(() => splitWithTab(s0, 'b', 'g1', 'right', seqGen())).not.toThrow();
    expect(() => splitWithTab(s0, 'b', 'g1', 'top', seqGen())).not.toThrow();
  });
});

describe('moveTab', () => {
  it('moving the only tab out of a group removes that group', () => {
    const s0 = state(split('r', 'row', [group('g1', ['a']), group('g2', ['b'])]), 'g1');
    const s = moveTab(s0, 'a', 'g2');
    expect(s.layout).toEqual(group('g2', ['b', 'a'], 'a'));
    expect(s.focusedGroupId).toBe('g2');
  });

  it('inserts at an index and hands the source’s active role to a neighbour', () => {
    const s0 = state(split('r', 'row', [group('g1', ['a', 'b', 'c'], 'b'), group('g2', ['d'])]));
    const s = moveTab(s0, 'b', 'g2', 0);
    expect(allGroups(s.layout)).toEqual([group('g1', ['a', 'c'], 'c'), group('g2', ['b', 'd'], 'b')]);
  });

  it('within the same group behaves like reorderTab', () => {
    const s0 = state(group('g1', ['a', 'b', 'c']));
    expect(moveTab(s0, 'a', 'g1', 2)).toEqual(reorderTab(s0, 'a', 2));
  });

  it('an unknown target or tab leaves the state as is', () => {
    const s0 = normalizeDock(state(group('g1', ['a', 'b'])));
    expect(moveTab(s0, 'a', 'nope')).toEqual(s0);
    expect(moveTab(s0, 'zzz', 'g1')).toEqual(s0);
  });
});

describe('reorderTab', () => {
  const s0 = state(group('g1', ['a', 'b', 'c'], 'b'));
  const order = (s: DockStateV2): string[] => (s.layout as LayoutGroup).tabIds;

  it('moves a tab to its final index within the group', () => {
    expect(order(reorderTab(s0, 'a', 2))).toEqual(['b', 'c', 'a']);
    expect(order(reorderTab(s0, 'c', 0))).toEqual(['c', 'a', 'b']);
    expect(order(reorderTab(s0, 'a', 1))).toEqual(['b', 'a', 'c']);
  });

  it('clamps an out-of-range index', () => {
    expect(order(reorderTab(s0, 'a', 99))).toEqual(['b', 'c', 'a']);
    expect(order(reorderTab(s0, 'c', -5))).toEqual(['c', 'a', 'b']);
  });

  it('keeps the active tab', () => {
    expect((reorderTab(s0, 'a', 2).layout as LayoutGroup).activeId).toBe('b');
  });
});

describe('closeTab', () => {
  it('activates the right neighbour, else the left one', () => {
    const s0 = state(group('g1', ['a', 'b', 'c'], 'b'));
    const s1 = closeTab(s0, 'b');
    expect(s1.layout).toEqual(group('g1', ['a', 'c'], 'c'));
    const s2 = closeTab(s1, 'c');
    expect(s2.layout).toEqual(group('g1', ['a'], 'a'));
    expect(s2.tabs.map((t) => t.id)).toEqual(['a']);
  });

  it('closing an inactive tab keeps the active one', () => {
    const s = closeTab(state(group('g1', ['a', 'b', 'c'], 'b')), 'c');
    expect((s.layout as LayoutGroup).activeId).toBe('b');
  });

  it('closing a group’s last tab removes the group and moves focus to a survivor', () => {
    const s0 = state(split('r', 'row', [group('g1', ['a']), group('g2', ['b'])]), 'g2');
    const s = closeTab(s0, 'b');
    expect(s.layout).toEqual(group('g1', ['a'], 'a'));
    expect(s.focusedGroupId).toBe('g1');
  });

  it('closing the last tab gives a closed empty dock', () => {
    const s = closeTab(state(group('g1', ['a'])), 'a');
    expect(s).toEqual({ ...emptyDockV2(), open: false });
  });
});

describe('activateTab / focusGroup / groupOf', () => {
  const s0 = state(split('r', 'row', [group('g1', ['a', 'b']), group('g2', ['c'])]), 'g2');

  it('activateTab sets the tab active and focuses its group', () => {
    const s = activateTab(s0, 'b');
    expect(groupOf(s, 'b')?.activeId).toBe('b');
    expect(s.focusedGroupId).toBe('g1');
  });

  it('focusGroup focuses an existing group and ignores an unknown one', () => {
    expect(focusGroup(s0, 'g1').focusedGroupId).toBe('g1');
    expect(focusGroup(s0, 'nope').focusedGroupId).toBe('g2');
  });

  it('groupOf finds the holder or null', () => {
    expect(groupOf(s0, 'c')?.id).toBe('g2');
    expect(groupOf(s0, 'zzz')).toBeNull();
  });
});

describe('resizeSplit', () => {
  const s0 = state(split('r', 'row', [group('g1', ['a']), group('g2', ['b']), group('g3', ['c'])]));
  const sizesOf = (s: DockStateV2): number[] => (s.layout as LayoutSplit).sizes;

  it('clamps each size to ≥ 0.1 and sums to 1', () => {
    const s = resizeSplit(s0, 'r', [0.01, 0.01, 0.98]);
    const sz = sizesOf(s);
    expect(sz[0]).toBeCloseTo(0.1, 9);
    expect(sz[1]).toBeCloseTo(0.1, 9);
    expect(sz[2]).toBeCloseTo(0.8, 9);
    expectSizesValid(s.layout);
  });

  it('normalizes a sum other than 1', () => {
    const sz = sizesOf(resizeSplit(s0, 'r', [2, 1, 1]));
    expect(sz[0]).toBeCloseTo(0.5, 9);
    expect(sz[1]).toBeCloseTo(0.25, 9);
    expect(sz[2]).toBeCloseTo(0.25, 9);
  });

  it('ignores a length mismatch or an unknown split', () => {
    const n = normalizeDock(s0);
    expect(resizeSplit(s0, 'r', [0.5, 0.5])).toEqual(n);
    expect(resizeSplit(s0, 'nope', [0.2, 0.3, 0.5])).toEqual(n);
  });

  it('resizes a nested split', () => {
    const nested = state(split('c', 'column', [group('g1', ['a']), split('r', 'row', [group('g2', ['b']), group('g3', ['c'])])]));
    const s = resizeSplit(nested, 'r', [0.7, 0.3]);
    const inner = (s.layout as LayoutSplit).children[1] as LayoutSplit;
    expect(inner.sizes[0]).toBeCloseTo(0.7, 9);
    expect(inner.sizes[1]).toBeCloseTo(0.3, 9);
  });
});

// --- geometry --------------------------------------------------------------

function overlaps(a: Rect, b: Rect): boolean {
  const eps = 1e-6;
  return a.x < b.x + b.w - eps && b.x < a.x + a.w - eps && a.y < b.y + b.h - eps && b.y < a.y + a.h - eps;
}

function inside(inner: Rect, outer: Rect): boolean {
  const eps = 1e-6;
  return (
    inner.x >= outer.x - eps &&
    inner.y >= outer.y - eps &&
    inner.x + inner.w <= outer.x + outer.w + eps &&
    inner.y + inner.h <= outer.y + outer.h + eps
  );
}

describe('layoutRects', () => {
  it('a 0.5/0.5 row of 1000×400 with gutter 4 → two 498-wide groups and one handle', () => {
    const node = split('r', 'row', [group('g1', ['a']), group('g2', ['b'])], [0.5, 0.5]);
    const { groups, handles } = layoutRects(node, { x: 0, y: 0, w: 1000, h: 400 }, 4);
    expect(groups).toEqual({
      g1: { x: 0, y: 0, w: 498, h: 400 },
      g2: { x: 502, y: 0, w: 498, h: 400 },
    });
    expect(handles).toEqual([{ splitId: 'r', index: 0, dir: 'row', rect: { x: 498, y: 0, w: 4, h: 400 } }]);
  });

  it('a lone group takes the whole rect with no handles', () => {
    const rect = { x: 10, y: 20, w: 300, h: 200 };
    expect(layoutRects(group('g', ['a']), rect)).toEqual({ groups: { g: rect }, handles: [] });
  });

  it('defaults the gutter to 4 px', () => {
    const node = split('c', 'column', [group('g1', ['a']), group('g2', ['b'])]);
    const { groups, handles } = layoutRects(node, { x: 0, y: 0, w: 200, h: 104 });
    expect(groups.g1).toEqual({ x: 0, y: 0, w: 200, h: 50 });
    expect(groups.g2).toEqual({ x: 0, y: 54, w: 200, h: 50 });
    expect(handles[0]).toMatchObject({ index: 0, dir: 'column', rect: { x: 0, y: 50, w: 200, h: 4 } });
  });

  it('nested splits tile the rect without overlap', () => {
    const node = split(
      'c',
      'column',
      [
        split('r', 'row', [group('g1', ['a']), group('g2', ['b']), group('g3', ['c'])], [0.2, 0.5, 0.3]),
        split('r2', 'row', [group('g4', ['d']), split('c2', 'column', [group('g5', ['e']), group('g6', ['f'])])]),
      ],
      [0.6, 0.4],
    );
    const outer = { x: 0, y: 0, w: 800, h: 600 };
    const { groups, handles } = layoutRects(node, outer, 4);
    const tiles = [...Object.values(groups), ...handles.map((h) => h.rect)];
    expect(Object.keys(groups).sort()).toEqual(['g1', 'g2', 'g3', 'g4', 'g5', 'g6']);
    expect(handles).toHaveLength(1 + 2 + 1 + 1);
    for (const t of tiles) expect(inside(t, outer)).toBe(true);
    for (let i = 0; i < tiles.length; i++) {
      for (let j = i + 1; j < tiles.length; j++) {
        expect(overlaps(tiles[i] as Rect, tiles[j] as Rect)).toBe(false);
      }
    }
    const area = sum(tiles.map((t) => t.w * t.h));
    expect(area).toBeCloseTo(outer.w * outer.h, 6);
    // Handle i sits between children i and i+1 of its split.
    const h0 = handles.find((h) => h.splitId === 'r' && h.index === 0);
    expect(h0?.rect.x).toBeCloseTo((groups.g1?.x ?? 0) + (groups.g1?.w ?? 0), 9);
    expect((h0?.rect.x ?? 0) + (h0?.rect.w ?? 0)).toBeCloseTo(groups.g2?.x ?? -1, 9);
  });
});

describe('dropTarget', () => {
  const r: Rect = { x: 100, y: 100, w: 400, h: 200 };

  it.each([
    ['left', { x: 110, y: 200 }],
    ['right', { x: 490, y: 200 }],
    ['top', { x: 300, y: 110 }],
    ['bottom', { x: 300, y: 290 }],
  ] as const)('a point in the %s 25%% band → that edge', (edge, p) => {
    expect(dropTarget(r, p)).toBe(edge);
  });

  it('just inside a band counts; just past it is the centre', () => {
    expect(dropTarget(r, { x: 199, y: 200 })).toBe('left');
    expect(dropTarget(r, { x: 201, y: 200 })).toBe('center');
    expect(dropTarget(r, { x: 300, y: 151 })).toBe('center');
    expect(dropTarget(r, { x: 300, y: 149 })).toBe('top');
  });

  it('the centre → center', () => {
    expect(dropTarget(r, { x: 300, y: 200 })).toBe('center');
  });

  it('a corner picks the nearer edge', () => {
    expect(dropTarget(r, { x: 120, y: 104 })).toBe('top'); // fx 0.05 vs fy 0.02
    expect(dropTarget(r, { x: 104, y: 120 })).toBe('left'); // fx 0.01 vs fy 0.10
    expect(dropTarget(r, { x: 495, y: 290 })).toBe('right'); // 1-fx 0.0125 vs 1-fy 0.05
    expect(dropTarget(r, { x: 480, y: 298 })).toBe('bottom'); // 1-fx 0.05 vs 1-fy 0.01
  });

  it('a degenerate rect → center', () => {
    expect(dropTarget({ x: 0, y: 0, w: 0, h: 100 }, { x: 0, y: 50 })).toBe('center');
  });
});
