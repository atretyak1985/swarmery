// Which side panels are collapsed (Swarmery v4): the sidebar, the list, the session
// rail. `true` means collapsed. An external store, because the toggles and what they
// resize sit in different branches of the tree: the Sidebar holds the keys, ListPane
// and PlaceLayout react, and neither shell wraps them in anything shared.
//
// The state survives a reload through localStorage. Storage can be blocked, so every
// read and write is guarded and the in-memory value stays the truth for the page.
//
// Below `desk` nothing collapses (D-3): the classes that read this state are all
// `desk:` ones, so a stored state simply waits for a wider window.

import { useSyncExternalStore } from 'react';

const STORAGE_KEY = 'swarmery.v4.panels';

export type PanelKey = 'nav' | 'list' | 'rail';
export type Panels = Readonly<Record<PanelKey, boolean>>;

/** The design's 0.18s curve, on whichever property the panel animates. */
export const PANEL_EASE =
  'duration-[180ms] ease-[cubic-bezier(0.22,1,0.36,1)] motion-reduce:transition-none';

function readPanels(): Panels {
  try {
    const stored = JSON.parse(window.localStorage.getItem(STORAGE_KEY) ?? '{}') as Partial<Panels> | null;
    return { nav: stored?.nav === true, list: stored?.list === true, rail: stored?.rail === true };
  } catch {
    return { nav: false, list: false, rail: false };
  }
}

function writePanels(panels: Panels): void {
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(panels));
  } catch {
    // Blocked storage: the panel still toggles, it just forgets on reload.
  }
}

let panels = readPanels();
let listBadge: number | null = null;
const listeners = new Set<() => void>();

function emit(): void {
  for (const listener of listeners) listener();
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

/** Sets one panel, and does nothing when it is already there: safe from an effect,
 * which StrictMode runs twice on mount. */
export function setPanel(key: PanelKey, collapsed: boolean): void {
  if (panels[key] === collapsed) return;
  panels = { ...panels, [key]: collapsed };
  writePanels(panels);
  emit();
}

export function togglePanel(key: PanelKey): void {
  setPanel(key, !panels[key]);
}

/** Re-reads storage, for a test that seeds it after this module loaded. */
export function reloadPanels(): void {
  panels = readPanels();
  emit();
}

export function usePanels(): Panels {
  return useSyncExternalStore(subscribe, () => panels);
}

/** The Sidebar publishes the active place's badge here: the collapsed list strip
 * repeats it, and the two are siblings under a shell that passes nothing down. */
export function setListBadge(count: number | null): void {
  if (count === listBadge) return;
  listBadge = count;
  emit();
}

export function useListBadge(): number | null {
  return useSyncExternalStore(subscribe, () => listBadge);
}
