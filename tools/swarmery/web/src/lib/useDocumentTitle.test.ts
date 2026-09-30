// @vitest-environment jsdom
//
// useDocumentTitle (plans-deep-links phase 2, SC-11): sets the tab title while
// mounted, follows changes, restores the previous title on unmount, and leaves
// the title alone for `null`.
//
// Dev-only suite (web/tsconfig.json excludes test files). Run with
//   npx vitest run src/lib/useDocumentTitle.test.ts
// after fetching the runner on demand:
//   npm i --no-save vitest jsdom @testing-library/react @testing-library/dom

import { cleanup, renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { useDocumentTitle } from './useDocumentTitle';

beforeEach(() => {
  document.title = 'Swarmery';
});
afterEach(cleanup);

describe('useDocumentTitle', () => {
  it('sets the title and restores the previous one on unmount', () => {
    const h = renderHook(() => {
      useDocumentTitle('Plans · swarmery — Swarmery');
    });
    expect(document.title).toBe('Plans · swarmery — Swarmery');
    h.unmount();
    expect(document.title).toBe('Swarmery');
  });

  it('follows a changed title, and still restores the original', () => {
    const h = renderHook(({ t }: { t: string }) => {
      useDocumentTitle(t);
    }, { initialProps: { t: 'A — Swarmery' } });
    h.rerender({ t: 'B — Swarmery' });
    expect(document.title).toBe('B — Swarmery');
    h.unmount();
    expect(document.title).toBe('Swarmery');
  });

  it('null leaves the title untouched', () => {
    document.title = 'set by someone else';
    const h = renderHook(() => {
      useDocumentTitle(null);
    });
    expect(document.title).toBe('set by someone else');
    h.unmount();
    expect(document.title).toBe('set by someone else');
  });
});
