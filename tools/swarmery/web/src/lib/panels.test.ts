// @vitest-environment jsdom
//
// The panel store (Swarmery v4). The claims:
//
//   1. Everything starts open, and a toggle flips one panel and tells subscribers.
//   2. The state is written under swarmery.v4.panels in the design's shape, and read
//      back after a reload.
//   3. Blocked or broken storage breaks nothing: the panel still toggles for the page.
//
// Runs with the rest of the web suite: `npm test`.

import { act, renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { reloadPanels, setListBadge, setPanel, togglePanel, useListBadge, usePanels } from './panels';

const KEY = 'swarmery.v4.panels';

beforeEach(() => {
  window.localStorage.clear();
  reloadPanels();
  setListBadge(null);
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('panels', () => {
  it('starts with every panel open', () => {
    const { result } = renderHook(() => usePanels());
    expect(result.current).toEqual({ nav: false, list: false, rail: false });
  });

  it('flips one panel and re-renders its readers', () => {
    const { result } = renderHook(() => usePanels());
    act(() => togglePanel('list'));
    expect(result.current).toEqual({ nav: false, list: true, rail: false });
    act(() => togglePanel('list'));
    expect(result.current.list).toBe(false);
  });

  it('sets a panel, and setting it twice is the same as once', () => {
    const { result } = renderHook(() => usePanels());
    act(() => setPanel('rail', true));
    act(() => setPanel('rail', true));
    expect(result.current.rail).toBe(true);
    expect(JSON.parse(window.localStorage.getItem(KEY) ?? 'null')).toEqual({ nav: false, list: false, rail: true });
    act(() => setPanel('rail', false));
    expect(result.current.rail).toBe(false);
  });

  it('persists under the design key and survives a reload', () => {
    togglePanel('nav');
    togglePanel('rail');
    expect(JSON.parse(window.localStorage.getItem(KEY) ?? 'null')).toEqual({ nav: true, list: false, rail: true });
    reloadPanels();
    const { result } = renderHook(() => usePanels());
    expect(result.current).toEqual({ nav: true, list: false, rail: true });
  });

  it.each(['not json', 'null', '[]', '{"nav":"yes"}'])('reads %s as everything open', (stored) => {
    window.localStorage.setItem(KEY, stored);
    reloadPanels();
    const { result } = renderHook(() => usePanels());
    expect(result.current).toEqual({ nav: false, list: false, rail: false });
  });

  it('still toggles when storage throws on read and on write', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new DOMException('blocked', 'SecurityError');
    });
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new DOMException('blocked', 'SecurityError');
    });
    reloadPanels();
    const { result } = renderHook(() => usePanels());
    expect(result.current.list).toBe(false);
    act(() => togglePanel('list'));
    expect(result.current.list).toBe(true);
  });
});

describe('useListBadge', () => {
  it('follows what the sidebar publishes', () => {
    const { result } = renderHook(() => useListBadge());
    expect(result.current).toBeNull();
    act(() => setListBadge(4));
    expect(result.current).toBe(4);
    act(() => setListBadge(null));
    expect(result.current).toBeNull();
  });
});
