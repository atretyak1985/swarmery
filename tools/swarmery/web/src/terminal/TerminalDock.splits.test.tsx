// Split rendering, drag-and-drop and the tab menu (terminal-dock-webstorm
// phase 4). The FakeXTerm counts mounts AND unmounts: a real XTerm closes its
// PTY on unmount, so no split, move or merge may unmount one (SC-14).
//
// jsdom has no layout, no DataTransfer and no DragEvent: drags carry a stub
// dataTransfer, coordinates are set on the event by hand, and every box a
// handler measures has its getBoundingClientRect stubbed.

import { useState } from 'react';
import { act, cleanup, createEvent, fireEvent, render, screen, within } from '../test/render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { loadDock } from '../workspace/ProjectWorkspaceLayout';
import { type DockStateV2, type LayoutNode, allGroups, migrateDock } from './layout';

let mounts = 0;
let unmounts = 0;

vi.mock('./XTerm', async () => {
  const { forwardRef, useEffect, useImperativeHandle } = await import('react');
  return {
    XTerm: forwardRef<unknown, { cwd?: string }>(function FakeXTerm({ cwd }, ref) {
      useImperativeHandle(ref, () => ({ clear: () => undefined, fit: () => undefined, send: () => undefined }));
      useEffect(() => {
        mounts += 1;
        return () => {
          unmounts += 1;
        };
      }, []);
      return <div data-testid="xterm" data-cwd={cwd} />;
    }),
  };
});

import { TerminalDock, activeTabId } from './TerminalDock';
import { TAB_MIME } from './TabStrip';

let latest: DockStateV2 | null = null;
function StatefulDock({ initial }: { initial: DockStateV2 }): JSX.Element {
  const [state, setState] = useState(initial);
  latest = state;
  return <TerminalDock projectSlug="proj" projectPath="/tmp/project" state={state} onChange={setState} />;
}

function current(): DockStateV2 {
  if (latest === null) throw new Error('dock not rendered');
  return latest;
}

const tab = (n: number) => ({ id: `t${n}`, label: `shell ${n}`, cwd: `/tmp/project/${n}` });

/** Three tabs in one group (a v1 value, the way a pre-split dock was saved). */
function threeTabs(): DockStateV2 {
  return migrateDock({ open: true, tabs: [tab(1), tab(2), tab(3)], activeId: 't1' });
}

/** row[ gA(t1, t3) | gB(t2) ] */
function twoGroups(focusedGroupId = 'gA'): DockStateV2 {
  return migrateDock({
    version: 2,
    open: true,
    tabs: [tab(1), tab(2), tab(3)],
    layout: {
      kind: 'split',
      id: 's-root',
      dir: 'row',
      sizes: [0.5, 0.5],
      children: [
        { kind: 'group', id: 'gA', tabIds: ['t1', 't3'], activeId: 't1' },
        { kind: 'group', id: 'gB', tabIds: ['t2'], activeId: 't2' },
      ],
    },
    focusedGroupId,
  });
}

/** row[ gA(t1) | column[ gB(t2) / gC(t3) ] ] */
function nested(): DockStateV2 {
  return migrateDock({
    version: 2,
    open: true,
    tabs: [tab(1), tab(2), tab(3)],
    layout: {
      kind: 'split',
      id: 's-root',
      dir: 'row',
      sizes: [0.5, 0.5],
      children: [
        { kind: 'group', id: 'gA', tabIds: ['t1'], activeId: 't1' },
        {
          kind: 'split',
          id: 's-col',
          dir: 'column',
          sizes: [0.5, 0.5],
          children: [
            { kind: 'group', id: 'gB', tabIds: ['t2'], activeId: 't2' },
            { kind: 'group', id: 'gC', tabIds: ['t3'], activeId: 't3' },
          ],
        },
      ],
    },
    focusedGroupId: 'gC',
  });
}

/** The tree as nested tab-id lists: `{ row: [['t1'], { column: [['t2'], ['t3']] }] }`. */
function shape(node: LayoutNode): unknown {
  return node.kind === 'group' ? node.tabIds : { [node.dir]: node.children.map(shape) };
}

function rect(x: number, y: number, w: number, h: number): DOMRect {
  return { x, y, left: x, top: y, width: w, height: h, right: x + w, bottom: y + h, toJSON: () => ({}) } as DOMRect;
}
function stubRect(el: Element, x: number, y: number, w: number, h: number): void {
  el.getBoundingClientRect = () => rect(x, y, w, h);
}

interface StubDataTransfer {
  types: string[];
  effectAllowed: string;
  dropEffect: string;
  setData: (type: string, value: string) => void;
  getData: (type: string) => string;
}
function dataTransfer(): StubDataTransfer {
  const data = new Map<string, string>();
  const dt: StubDataTransfer = {
    types: [],
    effectAllowed: 'all',
    dropEffect: 'none',
    setData: (type, value) => {
      data.set(type, value);
      if (!dt.types.includes(type)) dt.types.push(type);
    },
    getData: (type) => data.get(type) ?? '',
  };
  return dt;
}

/** Fire a drag event carrying `dt`, at viewport point (x, y). */
function fireDrag(
  kind: 'dragStart' | 'dragOver' | 'drop' | 'dragEnd',
  el: Element,
  dt: StubDataTransfer,
  x = 0,
  y = 0,
): void {
  const ev = createEvent[kind](el, { dataTransfer: dt });
  Object.defineProperty(ev, 'clientX', { value: x });
  Object.defineProperty(ev, 'clientY', { value: y });
  fireEvent(el, ev);
}

function tabEl(id: string): HTMLElement {
  const el = document.querySelector<HTMLElement>(`[data-tab-id="${id}"]`);
  if (el === null) throw new Error(`no tab ${id}`);
  return el;
}
function dropZones(): HTMLElement[] {
  return screen.queryAllByTestId('drop-zone');
}
function dropZone(groupId: string): HTMLElement {
  const el = dropZones().find((z) => z.dataset.groupId === groupId);
  if (el === undefined) throw new Error(`no drop zone for ${groupId}`);
  return el;
}
function stripTabIds(strip: HTMLElement): string[] {
  return Array.from(strip.querySelectorAll<HTMLElement>('[data-tab-id]')).map((el) => el.dataset.tabId ?? '');
}

async function renderDock(initial: DockStateV2): Promise<void> {
  render(<StatefulDock initial={initial} />);
  await vi.waitFor(() => expect(screen.getAllByTestId('xterm')).toHaveLength(initial.tabs.length));
  expect(mounts).toBe(initial.tabs.length);
}

/** Shift+F10 on a tab's button, then ↓ `steps` times; returns the focused menu item. */
function menuItemByKeyboard(tabName: string, steps: number): HTMLElement {
  const button = screen.getByRole('button', { name: tabName });
  button.focus();
  fireEvent.keyDown(button, { key: 'F10', shiftKey: true });
  const menu = screen.getByRole('menu', { name: 'Tab actions' });
  expect(document.activeElement).toBe(menu);
  for (let i = 0; i < steps; i++) fireEvent.keyDown(document.activeElement ?? menu, { key: 'ArrowDown' });
  return document.activeElement as HTMLElement;
}

beforeEach(() => {
  mounts = 0;
  unmounts = 0;
  latest = null;
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe('tab menu (SC-13, SC-14)', () => {
  it('6a: Shift+F10 → ↓ → Enter on "Split right" opens a second group, remounting nothing', async () => {
    await renderDock(threeTabs());
    expect(screen.getAllByTestId('tab-strip')).toHaveLength(1);

    const item = menuItemByKeyboard('shell 2', 1);
    expect(item.getAttribute('role')).toBe('menuitem');
    expect(item.textContent).toBe('Split right');
    fireEvent.keyDown(item, { key: 'Enter' });

    expect(screen.queryByRole('menu')).toBeNull();
    expect(shape(current().layout)).toEqual({ row: [['t1', 't3'], ['t2']] });
    const strips = screen.getAllByTestId('tab-strip');
    expect(strips).toHaveLength(2);
    expect(stripTabIds(strips[0] as HTMLElement)).toEqual(['t1', 't3']);
    expect(stripTabIds(strips[1] as HTMLElement)).toEqual(['t2']);
    // Focus follows the tab into its new strip.
    expect(document.activeElement).toBe(within(strips[1] as HTMLElement).getByRole('button', { name: 'shell 2' }));
    expect(mounts).toBe(3);
    expect(unmounts).toBe(0);
  });

  it('lists every action, disables what cannot apply, and Esc hands focus back to the tab', async () => {
    await renderDock(twoGroups());
    const t2 = screen.getByRole('button', { name: 'shell 2' });
    t2.focus();
    fireEvent.keyDown(t2, { key: 'ContextMenu' });
    const menu = screen.getByRole('menu', { name: 'Tab actions' });
    const items = within(menu).getAllByRole('menuitem');
    expect(items.map((el) => el.textContent)).toEqual([
      'Split right',
      'Split down',
      'Move left',
      'Move right',
      'Move to group 1',
      'Close',
    ]);
    // t2 is alone in its group: nothing to split off and nowhere to move within it.
    expect(items.map((el) => (el as HTMLButtonElement).disabled)).toEqual([true, true, true, true, false, false]);
    // ↓ skips the disabled entries.
    fireEvent.keyDown(menu, { key: 'ArrowDown' });
    expect(document.activeElement?.textContent).toBe('Move to group 1');
    fireEvent.keyDown(document.activeElement as Element, { key: 'ArrowDown' });
    expect(document.activeElement?.textContent).toBe('Close');
    fireEvent.keyDown(document.activeElement as Element, { key: 'ArrowDown' });
    expect(document.activeElement?.textContent).toBe('Move to group 1');

    fireEvent.keyDown(document.activeElement as Element, { key: 'Escape' });
    expect(screen.queryByRole('menu')).toBeNull();
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'shell 2' }));
    expect(shape(current().layout)).toEqual({ row: [['t1', 't3'], ['t2']] });
  });

  it('moves a tab right within its group and to another group from the menu', async () => {
    await renderDock(twoGroups());
    fireEvent.keyDown(menuItemByKeyboard('shell 1', 3), { key: 'Enter' });
    expect(shape(current().layout)).toEqual({ row: [['t3', 't1'], ['t2']] });

    const toGroup2 = menuItemByKeyboard('shell 1', 4);
    expect(toGroup2.textContent).toBe('Move to group 2');
    fireEvent.keyDown(toGroup2, { key: 'Enter' });
    expect(shape(current().layout)).toEqual({ row: [['t3'], ['t2', 't1']] });
    expect(current().focusedGroupId).toBe('gB');
    expect(unmounts).toBe(0);
  });

  it('opens from the "⋯" button and closes the tab from its Close entry', async () => {
    await renderDock(threeTabs());
    fireEvent.click(screen.getByRole('button', { name: 'Actions for shell 3' }));
    const menu = screen.getByRole('menu', { name: 'Tab actions' });
    fireEvent.click(within(menu).getByRole('menuitem', { name: 'Close' }));
    expect(current().tabs.map((t) => t.id)).toEqual(['t1', 't2']);
    expect(screen.queryByRole('menu')).toBeNull();
  });

  it('drops an open menu once its tab is closed by other means', async () => {
    await renderDock(threeTabs());
    fireEvent.click(screen.getByRole('button', { name: 'Actions for shell 3' }));
    expect(screen.getByRole('menu', { name: 'Tab actions' })).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'close shell 3' }));
    expect(screen.queryByRole('menu')).toBeNull();
  });
});

describe('drop on a group (SC-11, SC-14)', () => {
  it('6b: a drop on the bottom edge of another group nests a column split; no terminal remounts', async () => {
    await renderDock(twoGroups());
    // No overlay outside a drag: xterm keeps the pointer.
    expect(dropZones()).toHaveLength(0);

    const dt = dataTransfer();
    fireDrag('dragStart', tabEl('t3'), dt);
    expect(dt.getData(TAB_MIME)).toBe('t3');
    expect(dropZones()).toHaveLength(2);

    const zone = dropZone('gB');
    stubRect(zone, 500, 28, 500, 372);
    fireDrag('dragOver', zone, dt, 750, 390);
    expect(within(zone).getByTestId('drop-highlight').dataset.zone).toBe('bottom');
    fireDrag('drop', zone, dt, 750, 390);

    expect(shape(current().layout)).toEqual({ row: [['t1'], { column: [['t2'], ['t3']] }] });
    expect(dropZones()).toHaveLength(0);
    expect(screen.getAllByTestId('tab-strip')).toHaveLength(3);
    expect(mounts).toBe(3);
    expect(unmounts).toBe(0);
  });

  it('6c: a drop in the centre of group 1 moves the tab there and the emptied group disappears', async () => {
    await renderDock(nested());
    expect(screen.getAllByTestId('tab-strip')).toHaveLength(3);

    const dt = dataTransfer();
    fireDrag('dragStart', tabEl('t3'), dt);
    const zone = dropZone('gA');
    stubRect(zone, 0, 28, 498, 372);
    fireDrag('dragOver', zone, dt, 249, 214);
    expect(within(zone).getByTestId('drop-highlight').dataset.zone).toBe('center');
    fireDrag('drop', zone, dt, 249, 214);

    expect(shape(current().layout)).toEqual({ row: [['t1', 't3'], ['t2']] });
    expect(allGroups(current().layout).map((g) => g.id)).toEqual(['gA', 'gB']);
    expect(screen.getAllByTestId('tab-strip')).toHaveLength(2);
    expect(mounts).toBe(3);
    expect(unmounts).toBe(0);
  });

  it('splits by keyboard, nests by a drop, merges back by a drop — the same three terminals throughout', async () => {
    await renderDock(threeTabs());
    fireEvent.keyDown(menuItemByKeyboard('shell 2', 1), { key: 'Enter' });

    let dt = dataTransfer();
    fireDrag('dragStart', tabEl('t3'), dt);
    const second = dropZones()[1] as HTMLElement;
    stubRect(second, 500, 28, 500, 372);
    fireDrag('drop', second, dt, 750, 395);
    expect(shape(current().layout)).toEqual({ row: [['t1'], { column: [['t2'], ['t3']] }] });

    dt = dataTransfer();
    fireDrag('dragStart', tabEl('t3'), dt);
    const first = dropZones()[0] as HTMLElement;
    stubRect(first, 0, 28, 498, 372);
    fireDrag('drop', first, dt, 249, 214);
    expect(shape(current().layout)).toEqual({ row: [['t1', 't3'], ['t2']] });

    expect(mounts).toBe(3);
    expect(unmounts).toBe(0);
  });

  it('a drag that ends without a drop (Esc) changes nothing and removes the overlays', async () => {
    await renderDock(twoGroups());
    const before = current();
    const dt = dataTransfer();
    fireDrag('dragStart', tabEl('t1'), dt);
    expect(dropZones()).toHaveLength(2);
    fireDrag('dragEnd', tabEl('t1'), dt);
    expect(dropZones()).toHaveLength(0);
    expect(current()).toBe(before);
  });
});

describe('drop on a strip (SC-10)', () => {
  it('6d: a drop inside the strip reorders the tabs at the marker', async () => {
    await renderDock(threeTabs());
    stubRect(tabEl('t1'), 0, 0, 60, 24);
    stubRect(tabEl('t2'), 64, 0, 60, 24);
    stubRect(tabEl('t3'), 128, 0, 60, 24);
    const strip = screen.getByTestId('tab-strip');

    const dt = dataTransfer();
    fireDrag('dragStart', tabEl('t3'), dt);
    fireDrag('dragOver', strip, dt, 10, 12);
    // The insertion bar stands right before tab 1.
    expect(within(strip).getByTestId('insert-marker').nextElementSibling).toBe(tabEl('t1'));
    fireDrag('drop', strip, dt, 10, 12);

    expect(shape(current().layout)).toEqual(['t3', 't1', 't2']);
    expect(stripTabIds(strip)).toEqual(['t3', 't1', 't2']);
    expect(screen.queryByTestId('insert-marker')).toBeNull();
    expect(unmounts).toBe(0);

    // Past the last midpoint: to the end.
    const again = dataTransfer();
    stubRect(tabEl('t3'), 0, 0, 60, 24);
    stubRect(tabEl('t1'), 64, 0, 60, 24);
    stubRect(tabEl('t2'), 128, 0, 60, 24);
    fireDrag('dragStart', tabEl('t1'), again);
    fireDrag('drop', strip, again, 180, 12);
    expect(shape(current().layout)).toEqual(['t3', 't2', 't1']);
  });

  it('leaves no insertion marker behind for the next drag', async () => {
    await renderDock(threeTabs());
    const strip = screen.getByTestId('tab-strip');
    const dt = dataTransfer();
    fireDrag('dragStart', tabEl('t3'), dt);
    fireDrag('dragOver', strip, dt, 10, 12);
    expect(screen.getByTestId('insert-marker')).toBeTruthy();
    fireDrag('dragEnd', tabEl('t3'), dt);
    expect(screen.queryByTestId('insert-marker')).toBeNull();

    fireDrag('dragStart', tabEl('t2'), dataTransfer());
    expect(screen.queryByTestId('insert-marker')).toBeNull();
    expect(shape(current().layout)).toEqual(['t1', 't2', 't3']);
  });

  it('a drop on another group\'s strip moves the tab in at that index', async () => {
    await renderDock(twoGroups());
    const target = screen.getAllByTestId('tab-strip')[1] as HTMLElement;
    stubRect(tabEl('t2'), 504, 2, 60, 24);

    const dt = dataTransfer();
    fireDrag('dragStart', tabEl('t3'), dt);
    fireDrag('drop', target, dt, 510, 12);
    expect(shape(current().layout)).toEqual({ row: [['t1'], ['t3', 't2']] });
    expect(current().focusedGroupId).toBe('gB');
    expect(unmounts).toBe(0);
  });
});

describe('split handle (SC-12)', () => {
  function sizesOf(s: DockStateV2): number[] {
    if (s.layout.kind !== 'split') throw new Error('not split');
    return s.layout.sizes;
  }

  it('6e: arrows and a pointer drag resize the split, and the sizes persist', async () => {
    // The body is 1000 × 400 px: two row children share 1000 − 4 (gutter) = 996 px.
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
      return this.dataset.testid === 'dock-body' ? rect(0, 0, 1000, 400) : rect(0, 0, 0, 0);
    });
    await renderDock(twoGroups());
    const handle = screen.getByRole('separator', { name: 'Resize split' });
    expect(handle.getAttribute('aria-orientation')).toBe('vertical');
    expect(handle.className).toContain('cursor-col-resize');
    expect(handle.style.left).toBe('498px');

    handle.focus();
    fireEvent.keyDown(handle, { key: 'ArrowRight' });
    const [a, b] = sizesOf(current());
    expect(a).toBeCloseTo(0.55, 6);
    expect(b).toBeCloseTo(0.45, 6);
    expect(handle.getAttribute('aria-valuenow')).toBe('55');

    // Persisted the way ProjectWorkspaceLayout does it, and read back by loadDock.
    localStorage.setItem('swarmery.term.dock.proj', JSON.stringify(current()));
    const [sa, sb] = sizesOf(loadDock('proj'));
    expect(sa).toBeCloseTo(0.55, 6);
    expect(sb).toBeCloseTo(0.45, 6);
    localStorage.removeItem('swarmery.term.dock.proj');

    // 99.6 px of pointer travel is 0.1 of the 996 px the children share.
    fireEvent.pointerDown(handle, { clientX: 550, button: 0, pointerId: 1 });
    fireEvent.pointerMove(handle, { clientX: 649.6, pointerId: 1 });
    fireEvent.pointerUp(handle, { clientX: 649.6, pointerId: 1 });
    const [pa, pb] = sizesOf(current());
    expect(pa).toBeCloseTo(0.65, 6);
    expect(pb).toBeCloseTo(0.35, 6);

    // A move after the release does nothing.
    fireEvent.pointerMove(handle, { clientX: 900, pointerId: 1 });
    expect(sizesOf(current())[0]).toBeCloseTo(0.65, 6);

    // The keyboard stops at layout.ts's 0.1 floor.
    for (let i = 0; i < 20; i++) fireEvent.keyDown(handle, { key: 'ArrowLeft' });
    expect(sizesOf(current())[0]).toBeCloseTo(0.1, 6);
    expect(unmounts).toBe(0);
  });

  it('keeps the last real layout while the collapsed body measures 0 × 0', async () => {
    let body = rect(0, 0, 1000, 400);
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
      return this.dataset.testid === 'dock-body' ? body : rect(0, 0, 0, 0);
    });
    const observers: (() => void)[] = [];
    vi.stubGlobal(
      'ResizeObserver',
      class {
        constructor(cb: () => void) {
          observers.push(cb);
        }
        observe(): void {}
        disconnect(): void {}
      },
    );
    try {
      await renderDock(twoGroups());
      const handle = screen.getByRole('separator', { name: 'Resize split' });
      expect(handle.style.left).toBe('498px');

      body = rect(0, 0, 0, 0);
      act(() => {
        for (const cb of observers) cb();
      });
      expect(handle.style.left).toBe('498px');

      body = rect(0, 0, 600, 300);
      act(() => {
        for (const cb of observers) cb();
      });
      expect(handle.style.left).toBe('298px');
      expect(unmounts).toBe(0);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it('a column split takes ↑/↓ and a row cursor', async () => {
    await renderDock(nested());
    const handles = screen.getAllByRole('separator', { name: 'Resize split' });
    const column = handles.find((h) => h.getAttribute('aria-orientation') === 'horizontal');
    if (column === undefined) throw new Error('no column handle');
    expect(column.className).toContain('cursor-row-resize');
    fireEvent.keyDown(column, { key: 'ArrowDown' });
    const root = current().layout;
    const inner = root.kind === 'split' ? root.children[1] : undefined;
    expect(inner?.kind === 'split' ? inner.sizes[0] : undefined).toBeCloseTo(0.55, 6);
  });
});

describe('header and focused group (SC-15)', () => {
  it('6f: a lone group keeps its strip — and "+" — in the header, and fills the body', async () => {
    await renderDock(threeTabs());
    const header = screen.getByTestId('dock-header');
    const strip = screen.getByTestId('tab-strip');
    expect(header.contains(strip)).toBe(true);
    expect(strip.contains(screen.getByRole('button', { name: 'new terminal' }))).toBe(true);
    expect(screen.queryByTestId('group-focus')).toBeNull();
    expect(screen.queryAllByRole('separator', { name: 'Resize split' })).toHaveLength(0);
    const wrapper = document.querySelector<HTMLElement>('[data-term-tab="t1"]');
    expect(wrapper?.style.inset === '0px' || wrapper?.style.left === '0px').toBe(true);
    expect(wrapper?.style.right).toBe('0px');
    expect(wrapper?.style.bottom).toBe('0px');
  });

  it('6f: split groups each get a strip and "+" / Claude Code open in the focused group', async () => {
    await renderDock(twoGroups('gB'));
    const header = screen.getByTestId('dock-header');
    const strips = screen.getAllByTestId('tab-strip');
    expect(strips).toHaveLength(2);
    for (const s of strips) expect(header.contains(s)).toBe(false);
    expect(screen.getByTestId('group-focus').dataset.groupId).toBe('gB');

    fireEvent.click(screen.getByRole('button', { name: 'new terminal' }));
    const [gA, gB] = allGroups(current().layout);
    expect(gA?.tabIds).toEqual(['t1', 't3']);
    expect(gB?.tabIds).toHaveLength(2);
    const added = gB?.tabIds[1];
    expect(gB?.activeId).toBe(added);
    expect(activeTabId(current())).toBe(added);
    await vi.waitFor(() => expect(mounts).toBe(4));

    // A press in group 1's terminal focuses group 1; Claude Code then opens there.
    const t1 = document.querySelector<HTMLElement>('[data-term-tab="t1"]');
    if (t1 === null) throw new Error('no t1 wrapper');
    fireEvent.pointerDown(t1);
    expect(current().focusedGroupId).toBe('gA');
    expect(screen.getByTestId('group-focus').dataset.groupId).toBe('gA');
    fireEvent.click(screen.getByRole('button', { name: 'open Claude Code in a new terminal' }));
    const [gA2] = allGroups(current().layout);
    expect(gA2?.tabIds).toHaveLength(3);
    expect(current().tabs.find((t) => t.id === gA2?.tabIds[2])?.run).toBe('claude');
    await vi.waitFor(() => expect(mounts).toBe(5));
    expect(unmounts).toBe(0);
  });
});

describe('restoring a pre-split dock (SC-9 wiring)', () => {
  it('6g: a v1 saved value goes through loadDock and renders every tab', async () => {
    localStorage.setItem(
      'swarmery.term.dock.legacy',
      JSON.stringify({ open: true, tabs: [tab(1), tab(2), tab(3)], activeId: 't2' }),
    );
    const state = loadDock('legacy');
    localStorage.removeItem('swarmery.term.dock.legacy');
    expect(state.version).toBe(2);
    expect(activeTabId(state)).toBe('t2');

    await renderDock(state);
    const strip = within(screen.getByTestId('dock-header')).getByTestId('tab-strip');
    expect(stripTabIds(strip)).toEqual(['t1', 't2', 't3']);
    for (const n of [1, 2, 3]) expect(within(strip).getByRole('button', { name: `shell ${n}` })).toBeTruthy();
    // Only the active tab's terminal shows.
    const shown = Array.from(document.querySelectorAll<HTMLElement>('[data-term-tab]')).filter(
      (el) => el.style.visibility === 'visible',
    );
    expect(shown.map((el) => el.dataset.termTab)).toEqual(['t2']);
  });

  it('falls back to an empty dock for garbage', () => {
    localStorage.setItem('swarmery.term.dock.broken', '{not json');
    expect(loadDock('broken').tabs).toEqual([]);
    localStorage.removeItem('swarmery.term.dock.broken');
  });
});
