// Terminal dock layout model — DockState v2 (terminal-dock-webstorm, phase 3).
//
// The dock's panes form a tree: a LayoutSplit divides its rectangle between
// children along one axis by `sizes`, and every leaf is a LayoutGroup (one tab
// strip). This module is the whole model — persisted-shape migration (v1 → v2),
// normalization, the tree operations the UI dispatches, and the geometry the
// renderer needs. Pure data and pure functions: no React, no DOM, and every
// operation returns a new normalized state without mutating its input.

import type { TermTab } from './TerminalDock';

/** `row` = children side by side (Split right), `column` = stacked (Split down). */
export type SplitDir = 'row' | 'column';
/** A drop edge of a group's rectangle. */
export type Edge = 'left' | 'right' | 'top' | 'bottom';
/**
 * A dock tab as the v2 state stores it. `sessionId` is carried here until
 * `TermTab` declares it itself; once it does, this type is identical to it.
 */
export type DockTab = TermTab & { sessionId?: string };
/** A leaf of the layout tree: one tab strip and its active tab. */
export interface LayoutGroup {
  kind: 'group';
  id: string;
  tabIds: string[];
  activeId: string | null;
}
/** An inner node: `children` share the rectangle along `dir` by `sizes`. */
export interface LayoutSplit {
  kind: 'split';
  id: string;
  dir: SplitDir;
  sizes: number[];
  children: LayoutNode[];
}
/** Any node of the layout tree. */
export type LayoutNode = LayoutGroup | LayoutSplit;
/** The persisted dock state, version 2. */
export interface DockStateV2 {
  version: 2;
  open: boolean;
  tabs: DockTab[];
  layout: LayoutNode;
  focusedGroupId: string;
}
/** An axis-aligned rectangle in px. */
export interface Rect {
  x: number;
  y: number;
  w: number;
  h: number;
}
/** Node id generator, injectable so tests stay deterministic. */
export type IdGen = () => string;

const ROOT_ID = 'g-root';
/** Smallest share of a split a child may take. */
const MIN_SIZE = 0.1;
/** The outer share of a rectangle that counts as an edge drop zone. */
const EDGE_BAND = 0.25;
/** Float tolerance for "already fitted" sizes; keeps normalization idempotent. */
const EPS = 1e-9;

let idCounter = 0;
const defaultIdGen: IdGen = () => `g-${Date.now().toString(36)}${(idCounter++).toString(36)}`;

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

function emptyGroup(id: string): LayoutGroup {
  return { kind: 'group', id, tabIds: [], activeId: null };
}

/**
 * Fit `raw` to `n` shares: each ≥ MIN_SIZE, summing to 1, proportional to the
 * input otherwise. A wrong length, or no positive share, gives equal shares.
 * Already-fitted input comes back unchanged, so a second pass is a no-op.
 */
function fitSizes(raw: readonly number[], n: number): number[] {
  if (n <= 0) return [];
  const equal = Array.from({ length: n }, () => 1 / n);
  if (raw.length !== n || n * MIN_SIZE >= 1 - EPS) return equal;
  const vals = raw.map((v) => (Number.isFinite(v) && v > 0 ? v : 0));
  const total = vals.reduce((a, b) => a + b, 0);
  if (total <= 0) return equal;
  if (Math.abs(total - 1) <= EPS && vals.every((v) => v >= MIN_SIZE - EPS)) return vals;
  // Water-fill: pin every share that falls under the floor to MIN_SIZE, scale
  // the rest into the remaining budget, repeat until nothing new is pinned.
  // Terminates in ≤ n rounds, and n·MIN_SIZE < 1 keeps one share unpinned.
  const base = vals.map((v) => v / total);
  const pinned = base.map(() => false);
  for (;;) {
    const budget = 1 - MIN_SIZE * pinned.filter(Boolean).length;
    const freeSum = base.reduce((a, v, i) => (pinned[i] ? a : a + v), 0);
    const out = base.map((v, i) => (pinned[i] ? MIN_SIZE : (v * budget) / freeSum));
    let changed = false;
    out.forEach((v, i) => {
      if (!pinned[i] && v < MIN_SIZE) {
        pinned[i] = true;
        changed = true;
      }
    });
    if (!changed) return out;
  }
}

function clampIndex(i: number, len: number): number {
  if (!Number.isFinite(i)) return len;
  return Math.max(0, Math.min(len, Math.trunc(i)));
}

function insertAt<T>(list: readonly T[], at: number, item: T): T[] {
  return [...list.slice(0, at), item, ...list.slice(at)];
}

/** A new tree with `fn` applied to every group (depth-first, children in order). */
function mapGroups(node: LayoutNode, fn: (g: LayoutGroup) => LayoutGroup): LayoutNode {
  if (node.kind === 'group') return fn(node);
  return { ...node, sizes: [...node.sizes], children: node.children.map((c) => mapGroups(c, fn)) };
}

function findGroup(node: LayoutNode, groupId: string): LayoutGroup | undefined {
  return allGroups(node).find((g) => g.id === groupId);
}

function findSplit(node: LayoutNode, splitId: string): LayoutSplit | undefined {
  if (node.kind === 'group') return undefined;
  if (node.id === splitId) return node;
  for (const c of node.children) {
    const hit = findSplit(c, splitId);
    if (hit !== undefined) return hit;
  }
  return undefined;
}

/** `g` without `tabId`; if it was active, its right neighbour (else left) takes over. */
function removeFromGroup(g: LayoutGroup, tabId: string): LayoutGroup {
  const i = g.tabIds.indexOf(tabId);
  if (i < 0) return g;
  const tabIds = g.tabIds.filter((id) => id !== tabId);
  const activeId = g.activeId === tabId ? (tabIds[i] ?? tabIds[i - 1] ?? null) : g.activeId;
  return { ...g, tabIds, activeId };
}

/**
 * Drop empty groups, collapse single-child splits, flatten a child split of the
 * parent's own `dir` into the parent (its sizes scaled by its share), and fit
 * every split's sizes. Bottom-up, so a single pass reaches the fixed point.
 * Returns null when no group with tabs is left.
 */
function prune(node: LayoutNode): LayoutNode | null {
  if (node.kind === 'group') return node.tabIds.length > 0 ? node : null;
  const base = fitSizes(node.sizes, node.children.length);
  const kept: { node: LayoutNode; size: number }[] = [];
  node.children.forEach((child, i) => {
    const p = prune(child);
    if (p === null) return;
    const share = base[i] ?? 0;
    if (p.kind === 'split' && p.dir === node.dir) {
      p.children.forEach((c, j) => {
        kept.push({ node: c, size: share * (p.sizes[j] ?? 0) });
      });
    } else {
      kept.push({ node: p, size: share });
    }
  });
  const first = kept[0];
  if (first === undefined) return null;
  if (kept.length === 1) return first.node;
  return {
    kind: 'split',
    id: node.id,
    dir: node.dir,
    sizes: fitSizes(
      kept.map((k) => k.size),
      kept.length,
    ),
    children: kept.map((k) => k.node),
  };
}

/** A persisted tab, or null when it has no string `id`/`cwd`. Known fields only. */
function parseTab(raw: unknown): DockTab | null {
  if (!isRecord(raw)) return null;
  const { id, cwd, label, run, sessionId, ptyId } = raw;
  if (typeof id !== 'string' || id === '' || typeof cwd !== 'string') return null;
  const tab: DockTab = { id, label: typeof label === 'string' ? label : id, cwd };
  if (typeof run === 'string') tab.run = run;
  if (typeof sessionId === 'string') tab.sessionId = sessionId;
  // Phase 5: the daemon-side session id to reattach to on a reload instead of
  // starting a fresh PTY — see TermTab.ptyId and termSocket.ts's termWsUrlFor.
  if (typeof ptyId === 'string') tab.ptyId = ptyId;
  return tab;
}

/** A structurally valid node from persisted JSON, or null. A missing or duplicate id gets `gen()`. */
function parseNode(raw: unknown, gen: IdGen, seen: Set<string>): LayoutNode | null {
  if (!isRecord(raw)) return null;
  const takeId = (): string => {
    const id = typeof raw.id === 'string' && raw.id !== '' && !seen.has(raw.id) ? raw.id : gen();
    seen.add(id);
    return id;
  };
  if (raw.kind === 'group') {
    const tabIds = Array.isArray(raw.tabIds) ? raw.tabIds.filter((t): t is string => typeof t === 'string') : [];
    const activeId = typeof raw.activeId === 'string' ? raw.activeId : null;
    return { kind: 'group', id: takeId(), tabIds, activeId };
  }
  const dir = raw.dir;
  const children = raw.children;
  if (raw.kind !== 'split' || (dir !== 'row' && dir !== 'column') || !Array.isArray(children)) return null;
  const id = takeId();
  const rawSizes: unknown[] = Array.isArray(raw.sizes) && raw.sizes.length === children.length ? raw.sizes : [];
  const kept: { node: LayoutNode; size: number }[] = [];
  children.forEach((c, i) => {
    const node = parseNode(c, gen, seen);
    const size = rawSizes[i];
    if (node !== null) kept.push({ node, size: typeof size === 'number' ? size : Number.NaN });
  });
  return { kind: 'split', id, dir, sizes: kept.map((k) => k.size), children: kept.map((k) => k.node) };
}

/** The tree with `fresh` beside group `targetId` along `dir`; a same-dir parent gains a sibling. */
function insertBeside(
  node: LayoutNode,
  targetId: string,
  fresh: LayoutGroup,
  dir: SplitDir,
  before: boolean,
  gen: IdGen,
): LayoutNode {
  if (node.kind === 'group') {
    if (node.id !== targetId) return node;
    return { kind: 'split', id: gen(), dir, sizes: [0.5, 0.5], children: before ? [fresh, node] : [node, fresh] };
  }
  const idx = node.children.findIndex((c) => c.kind === 'group' && c.id === targetId);
  if (idx >= 0 && node.dir === dir) {
    const sizes = fitSizes(node.sizes, node.children.length);
    const half = (sizes[idx] ?? 0) / 2;
    const at = before ? idx : idx + 1;
    return {
      ...node,
      sizes: insertAt(
        sizes.map((v, i) => (i === idx ? half : v)),
        at,
        half,
      ),
      children: insertAt(node.children, at, fresh),
    };
  }
  return {
    ...node,
    sizes: [...node.sizes],
    children: node.children.map((c) => insertBeside(c, targetId, fresh, dir, before, gen)),
  };
}

/** The tree with split `splitId`'s sizes replaced by the fitted `sizes`. */
function withSizes(node: LayoutNode, splitId: string, sizes: readonly number[]): LayoutNode {
  if (node.kind === 'group') return node;
  if (node.id === splitId) return { ...node, sizes: fitSizes(sizes, node.children.length), children: [...node.children] };
  return { ...node, sizes: [...node.sizes], children: node.children.map((c) => withSizes(c, splitId, sizes)) };
}

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------

/** A closed dock with no tabs: a single empty root group, focused. */
export function emptyDockV2(): DockStateV2 {
  return { version: 2, open: false, tabs: [], layout: emptyGroup(ROOT_ID), focusedGroupId: ROOT_ID };
}

/** Any persisted dock value (v1, v2 or garbage) as a normalized v2 state. */
export function migrateDock(raw: unknown, gen: IdGen = defaultIdGen): DockStateV2 {
  if (!isRecord(raw) || !Array.isArray(raw.tabs)) return emptyDockV2();
  const tabs = raw.tabs.map(parseTab).filter((t): t is DockTab => t !== null);
  const open = raw.open === true;
  if (raw.version === 2) {
    const layout = parseNode(raw.layout, gen, new Set()) ?? emptyGroup(ROOT_ID);
    const focusedGroupId = typeof raw.focusedGroupId === 'string' ? raw.focusedGroupId : '';
    return normalizeDock({ version: 2, open, tabs, layout, focusedGroupId });
  }
  if (raw.version !== undefined) return emptyDockV2();
  const activeId = typeof raw.activeId === 'string' ? raw.activeId : null;
  const layout: LayoutGroup = { kind: 'group', id: ROOT_ID, tabIds: tabs.map((t) => t.id), activeId };
  return normalizeDock({ version: 2, open, tabs, layout, focusedGroupId: ROOT_ID });
}

/** `s` with every layout invariant restored (phase doc §3); idempotent. */
export function normalizeDock(s: DockStateV2): DockStateV2 {
  // Tabs: first occurrence of an id wins.
  const tabIdSet = new Set<string>();
  const tabs: DockTab[] = [];
  for (const t of s.tabs) {
    if (tabIdSet.has(t.id)) continue;
    tabIdSet.add(t.id);
    tabs.push(t);
  }
  // Groups: a tab id stays only in its first group (depth-first); ids without a tab go.
  const claimed = new Set<string>();
  const claim = (id: string): boolean => {
    if (!tabIdSet.has(id) || claimed.has(id)) return false;
    claimed.add(id);
    return true;
  };
  let layout = mapGroups(s.layout, (g) => ({ ...g, tabIds: g.tabIds.filter(claim) }));
  if (allGroups(layout).length === 0) layout = emptyGroup(ROOT_ID);
  // Orphan tabs join the focused group, else the first one.
  const groups = allGroups(layout);
  const homeId = (groups.find((g) => g.id === s.focusedGroupId) ?? groups[0])?.id ?? ROOT_ID;
  const orphans = tabs.filter((t) => !claimed.has(t.id)).map((t) => t.id);
  if (orphans.length > 0) {
    layout = mapGroups(layout, (g) => (g.id === homeId ? { ...g, tabIds: [...g.tabIds, ...orphans] } : g));
  }
  // Tree shape, then each group's active tab, then focus.
  const pruned = prune(layout) ?? emptyGroup(homeId);
  const fixed = mapGroups(pruned, (g) => ({
    ...g,
    activeId: g.activeId !== null && g.tabIds.includes(g.activeId) ? g.activeId : (g.tabIds[0] ?? null),
  }));
  const finalGroups = allGroups(fixed);
  const focusedGroupId = finalGroups.some((g) => g.id === s.focusedGroupId)
    ? s.focusedGroupId
    : (finalGroups[0]?.id ?? ROOT_ID);
  return { version: 2, open: s.open, tabs, layout: fixed, focusedGroupId };
}

// ---------------------------------------------------------------------------
// Operations
// ---------------------------------------------------------------------------

/** Append `tab` to a group (default: the focused one), activate and focus it, open the dock. */
export function addTab(s: DockStateV2, tab: DockTab, groupId: string = s.focusedGroupId): DockStateV2 {
  if (s.tabs.some((t) => t.id === tab.id)) return { ...activateTab(s, tab.id), open: true };
  const groups = allGroups(s.layout);
  const target = groups.find((g) => g.id === groupId) ?? groups.find((g) => g.id === s.focusedGroupId) ?? groups[0];
  const tabs = [...s.tabs, tab];
  if (target === undefined) return normalizeDock({ ...s, open: true, tabs });
  const layout = mapGroups(s.layout, (g) =>
    g.id === target.id ? { ...g, tabIds: [...g.tabIds, tab.id], activeId: tab.id } : g,
  );
  return normalizeDock({ ...s, open: true, tabs, layout, focusedGroupId: target.id });
}

/** Remove a tab; its right neighbour (else left) becomes active. No tabs left → a closed empty dock. */
export function closeTab(s: DockStateV2, tabId: string): DockStateV2 {
  const tabs = s.tabs.filter((t) => t.id !== tabId);
  if (tabs.length === 0) return { ...emptyDockV2(), open: false };
  const layout = mapGroups(s.layout, (g) => removeFromGroup(g, tabId));
  return normalizeDock({ ...s, tabs, layout });
}

/** Make a tab active in its group and focus that group. */
export function activateTab(s: DockStateV2, tabId: string): DockStateV2 {
  const home = groupOf(s, tabId);
  if (home === null) return normalizeDock(s);
  const layout = mapGroups(s.layout, (g) => (g.id === home.id ? { ...g, activeId: tabId } : g));
  return normalizeDock({ ...s, layout, focusedGroupId: home.id });
}

/** Move a tab to `toIndex` (its final position, clamped) within its own group. */
export function reorderTab(s: DockStateV2, tabId: string, toIndex: number): DockStateV2 {
  const home = groupOf(s, tabId);
  if (home === null) return normalizeDock(s);
  const rest = home.tabIds.filter((id) => id !== tabId);
  const tabIds = insertAt(rest, clampIndex(toIndex, rest.length), tabId);
  const layout = mapGroups(s.layout, (g) => (g.id === home.id ? { ...g, tabIds } : g));
  return normalizeDock({ ...s, layout });
}

/** Move a tab into another group at `index` (default: the end); an emptied source group vanishes. */
export function moveTab(s: DockStateV2, tabId: string, targetGroupId: string, index?: number): DockStateV2 {
  const source = groupOf(s, tabId);
  const target = findGroup(s.layout, targetGroupId);
  if (source === null || target === undefined) return normalizeDock(s);
  if (source.id === target.id) return reorderTab(s, tabId, index ?? target.tabIds.length - 1);
  const layout = mapGroups(s.layout, (g) => {
    if (g.id === source.id) return removeFromGroup(g, tabId);
    if (g.id !== target.id) return g;
    const at = clampIndex(index ?? g.tabIds.length, g.tabIds.length);
    return { ...g, tabIds: insertAt(g.tabIds, at, tabId), activeId: tabId };
  });
  return normalizeDock({ ...s, layout, focusedGroupId: target.id });
}

/**
 * Open a new group holding only `tabId` on `edge` of the target group: left/right
 * split as a row, top/bottom as a column. A parent already split that way gains a
 * sibling instead of a nested split; the target's share is halved. Splitting a
 * group by its own sole tab is a no-op.
 */
export function splitWithTab(
  s: DockStateV2,
  tabId: string,
  targetGroupId: string,
  edge: Edge,
  gen: IdGen = defaultIdGen,
): DockStateV2 {
  const source = groupOf(s, tabId);
  const target = findGroup(s.layout, targetGroupId);
  if (source === null || target === undefined) return normalizeDock(s);
  if (source.id === target.id && target.tabIds.length === 1) return normalizeDock(s);
  const dir: SplitDir = edge === 'left' || edge === 'right' ? 'row' : 'column';
  const before = edge === 'left' || edge === 'top';
  const fresh: LayoutGroup = { kind: 'group', id: gen(), tabIds: [tabId], activeId: tabId };
  const detached = mapGroups(s.layout, (g) => (g.id === source.id ? removeFromGroup(g, tabId) : g));
  const layout = insertBeside(detached, target.id, fresh, dir, before, gen);
  return normalizeDock({ ...s, layout, focusedGroupId: fresh.id });
}

/** Set a split's sizes: each clamped to ≥ 0.1, the set renormalized to sum 1. A length mismatch is ignored. */
export function resizeSplit(s: DockStateV2, splitId: string, sizes: readonly number[]): DockStateV2 {
  const split = findSplit(s.layout, splitId);
  if (split === undefined || split.children.length !== sizes.length) return normalizeDock(s);
  return normalizeDock({ ...s, layout: withSizes(s.layout, splitId, sizes) });
}

/** Focus a group (ignored when it does not exist). */
export function focusGroup(s: DockStateV2, groupId: string): DockStateV2 {
  if (findGroup(s.layout, groupId) === undefined) return normalizeDock(s);
  return normalizeDock({ ...s, focusedGroupId: groupId });
}

/** The group holding `tabId`, or null. */
export function groupOf(s: DockStateV2, tabId: string): LayoutGroup | null {
  return allGroups(s.layout).find((g) => g.tabIds.includes(tabId)) ?? null;
}

/** Every group under `node`, in depth-first (visual reading) order. */
export function allGroups(node: LayoutNode): LayoutGroup[] {
  return node.kind === 'group' ? [node] : node.children.flatMap(allGroups);
}

// ---------------------------------------------------------------------------
// Geometry
// ---------------------------------------------------------------------------

/**
 * Lay the tree out in `rect`: one rectangle per group, and one resize handle per
 * pair of neighbours (handle `index` i fills the `gutter` between children i and i+1).
 */
export function layoutRects(
  node: LayoutNode,
  rect: Rect,
  gutter = 4,
): { groups: Record<string, Rect>; handles: { splitId: string; index: number; dir: SplitDir; rect: Rect }[] } {
  const groups: Record<string, Rect> = {};
  const handles: { splitId: string; index: number; dir: SplitDir; rect: Rect }[] = [];
  const walk = (n: LayoutNode, r: Rect): void => {
    if (n.kind === 'group') {
      groups[n.id] = r;
      return;
    }
    const count = n.children.length;
    const row = n.dir === 'row';
    const start = row ? r.x : r.y;
    const extent = row ? r.w : r.h;
    const avail = Math.max(0, extent - gutter * (count - 1));
    const sizes = fitSizes(n.sizes, count);
    let offset = start;
    n.children.forEach((child, i) => {
      const last = i === count - 1;
      // The last child takes the remainder, so float drift never leaves a seam.
      const len = Math.max(0, last ? start + extent - offset : avail * (sizes[i] ?? 0));
      walk(child, row ? { x: offset, y: r.y, w: len, h: r.h } : { x: r.x, y: offset, w: r.w, h: len });
      offset += len;
      if (last) return;
      const hr: Rect = row ? { x: offset, y: r.y, w: gutter, h: r.h } : { x: r.x, y: offset, w: r.w, h: gutter };
      handles.push({ splitId: n.id, index: i, dir: n.dir, rect: hr });
      offset += gutter;
    });
  };
  walk(node, rect);
  return { groups, handles };
}

/** Where a drag over `r` at `p` drops: the nearest edge within its outer 25 %, else `center`. */
export function dropTarget(r: Rect, p: { x: number; y: number }): Edge | 'center' {
  if (r.w <= 0 || r.h <= 0) return 'center';
  const fx = Math.min(1, Math.max(0, (p.x - r.x) / r.w));
  const fy = Math.min(1, Math.max(0, (p.y - r.y) / r.h));
  const candidates: [Edge, number][] = [
    ['left', fx],
    ['right', 1 - fx],
    ['top', fy],
    ['bottom', 1 - fy],
  ];
  let best: Edge | 'center' = 'center';
  let bestDist = EDGE_BAND;
  for (const [edge, dist] of candidates) {
    if (dist < bestDist) {
      best = edge;
      bestDist = dist;
    }
  }
  return best;
}
