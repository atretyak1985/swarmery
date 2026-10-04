// @vitest-environment jsdom
//
// Tabs (Canvas v3 phase 2): the keyboard half of the WAI-ARIA tabs pattern —
// ArrowLeft/ArrowRight with wraparound, Home/End, selection follows focus, a
// roving tabindex — plus useTabParam's `?tab=` mirror.
//
// Runs with the rest of the web suite: `npm test` (vitest, also a swarmery-ci
// step). On its own: `npx vitest run src/components/Tabs.test.tsx`.
// web/tsconfig.json EXCLUDES *.test.tsx, and vitest transpiles without type
// checking, so NOTHING type-checks this file — treat its types as documentation.

import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { useState } from 'react';
import { MemoryRouter, useLocation } from 'react-router-dom';
import { afterEach, describe, expect, it } from 'vitest';
import { Tabs, nextTabIndex, useTabParam } from './Tabs';

type Id = 'overview' | 'agents' | 'advisor';
const TABS = [
  { id: 'overview' as const, label: 'Overview' },
  { id: 'agents' as const, label: 'Agents', count: 7 },
  { id: 'advisor' as const, label: 'Advisor', count: '4 open' },
];

function Harness(): JSX.Element {
  const [value, setValue] = useState<Id>('overview');
  return <Tabs tabs={TABS} value={value} onChange={setValue} ariaLabel="health sections" />;
}

afterEach(cleanup);

function tab(name: RegExp): HTMLElement {
  return screen.getByRole('tab', { name });
}

describe('Tabs', () => {
  it('renders a labelled tablist with counts after the labels', () => {
    render(<Harness />);
    expect(screen.getByRole('tablist', { name: 'health sections' })).toBeTruthy();
    expect(tab(/^Agents/).textContent).toBe('Agents7');
    expect(tab(/^Advisor/).textContent).toBe('Advisor4 open');
  });

  it('moves selection and focus with the arrow keys, wrapping at both ends', () => {
    render(<Harness />);
    const list = screen.getByRole('tablist');
    fireEvent.keyDown(list, { key: 'ArrowRight' });
    expect(tab(/^Agents/).getAttribute('aria-selected')).toBe('true');
    expect(document.activeElement).toBe(tab(/^Agents/));
    fireEvent.keyDown(list, { key: 'ArrowRight' });
    fireEvent.keyDown(list, { key: 'ArrowRight' }); // wraps to the first
    expect(tab(/^Overview/).getAttribute('aria-selected')).toBe('true');
    fireEvent.keyDown(list, { key: 'ArrowLeft' }); // wraps to the last
    expect(tab(/^Advisor/).getAttribute('aria-selected')).toBe('true');
    expect(document.activeElement).toBe(tab(/^Advisor/));
  });

  it('jumps with Home and End', () => {
    render(<Harness />);
    const list = screen.getByRole('tablist');
    fireEvent.keyDown(list, { key: 'End' });
    expect(tab(/^Advisor/).getAttribute('aria-selected')).toBe('true');
    fireEvent.keyDown(list, { key: 'Home' });
    expect(tab(/^Overview/).getAttribute('aria-selected')).toBe('true');
  });

  it('keeps exactly one tab in the tab order', () => {
    render(<Harness />);
    fireEvent.click(tab(/^Agents/));
    const stops = screen.getAllByRole('tab').filter((t) => t.getAttribute('tabindex') === '0');
    expect(stops).toEqual([tab(/^Agents/)]);
  });

  it('leaves unrelated keys alone', () => {
    expect(nextTabIndex('a', 0, 3)).toBeNull();
    expect(nextTabIndex('ArrowRight', 0, 0)).toBeNull();
  });
});

function ParamHarness(): JSX.Element {
  const [value, setValue] = useTabParam<Id>('tab', ['overview', 'agents', 'advisor'], 'overview');
  const { search } = useLocation();
  return (
    <>
      <Tabs tabs={TABS} value={value} onChange={setValue} ariaLabel="health sections" />
      <output>{search}</output>
    </>
  );
}

describe('useTabParam', () => {
  it('reads the tab from ?tab= and falls back on unknown values', () => {
    render(
      <MemoryRouter initialEntries={['/health?tab=advisor']}>
        <ParamHarness />
      </MemoryRouter>,
    );
    expect(tab(/^Advisor/).getAttribute('aria-selected')).toBe('true');
    cleanup();
    render(
      <MemoryRouter initialEntries={['/health?tab=nope']}>
        <ParamHarness />
      </MemoryRouter>,
    );
    expect(tab(/^Overview/).getAttribute('aria-selected')).toBe('true');
  });

  it('writes the selection back and drops the param for the fallback', () => {
    render(
      <MemoryRouter initialEntries={['/health?range=14']}>
        <ParamHarness />
      </MemoryRouter>,
    );
    fireEvent.click(tab(/^Agents/));
    expect(screen.getByRole('status').textContent).toBe('?range=14&tab=agents');
    fireEvent.click(tab(/^Overview/));
    expect(screen.getByRole('status').textContent).toBe('?range=14');
  });
});
