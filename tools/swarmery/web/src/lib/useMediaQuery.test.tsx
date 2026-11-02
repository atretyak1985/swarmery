// @vitest-environment jsdom
//
// useMediaQuery: follows the query live, and stays false without matchMedia.
//
// Runs with the rest of the web suite: `npm test`.

import { act, renderHook } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { useMediaQuery } from './useMediaQuery';

const QUERY = '(min-width: 1280px)';

afterEach(() => {
  vi.unstubAllGlobals();
  Reflect.deleteProperty(window, 'matchMedia');
});

describe('useMediaQuery', () => {
  it('is false where matchMedia is missing', () => {
    Reflect.deleteProperty(window, 'matchMedia');
    expect(renderHook(() => useMediaQuery(QUERY)).result.current).toBe(false);
  });

  it('follows the query as it changes', () => {
    let matches = false;
    const listeners = new Set<() => void>();
    window.matchMedia = ((): MediaQueryList =>
      ({
        get matches() {
          return matches;
        },
        addEventListener: (_: string, listener: () => void) => listeners.add(listener),
        removeEventListener: (_: string, listener: () => void) => listeners.delete(listener),
      }) as unknown as MediaQueryList) as typeof window.matchMedia;
    const { result, unmount } = renderHook(() => useMediaQuery(QUERY));
    expect(result.current).toBe(false);
    act(() => {
      matches = true;
      for (const listener of listeners) listener();
    });
    expect(result.current).toBe(true);
    unmount();
    expect(listeners.size).toBe(0);
  });
});
