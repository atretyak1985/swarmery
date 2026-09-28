// @vitest-environment jsdom
//
// StatusStrip (Canvas v3 phase 2): the sticky band on top of a place — title,
// three cells (label + value + delta, optional link, tone) and the segmented
// range control, which fires onChange only for a NEW value.
//
// Dev-only suite (web/tsconfig.json excludes *.test.tsx). Run with
//   npx vitest run src/components/StatusStrip.test.tsx

import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { StatusStrip, type StatusCell } from './StatusStrip';

type Range = '7' | '14' | '30' | '90';
const OPTIONS = [
  { value: '7' as const, label: '7 d' },
  { value: '14' as const, label: '14 d' },
  { value: '30' as const, label: '30 d' },
  { value: '90' as const, label: '90 d' },
];

const CELLS: StatusCell[] = [
  { label: 'this window', value: '142 runs · 11 % failed', delta: <span>↓ from 19 %</span> },
  { label: 'waiting on you', value: '1 Advisor finding', href: '/inbox', tone: 'amber' },
  { label: 'because of you', value: '2 changes verified' },
];

function renderStrip(onChange: (v: Range) => void, value: Range = '14'): HTMLElement {
  const { container } = render(
    <MemoryRouter>
      <StatusStrip<Range>
        title="Health"
        subtitle="how the agent fleet is doing"
        range={{ value, options: OPTIONS, onChange }}
        cells={CELLS}
      />
    </MemoryRouter>,
  );
  return container.firstElementChild as HTMLElement;
}

afterEach(cleanup);

describe('StatusStrip', () => {
  it('is sticky and shows the title, subtitle and three cells', () => {
    const strip = renderStrip(vi.fn());
    expect(strip.className).toContain('sticky');
    expect(strip.className).toContain('top-0');
    expect(screen.getByRole('heading', { level: 1, name: 'Health' })).toBeTruthy();
    expect(screen.getByText('how the agent fleet is doing')).toBeTruthy();
    for (const label of ['this window', 'waiting on you', 'because of you']) {
      expect(screen.getByText(label)).toBeTruthy();
    }
    expect(screen.getByText('↓ from 19 %')).toBeTruthy();
  });

  it('links a cell that has an href', () => {
    renderStrip(vi.fn());
    const link = screen.getByRole('link');
    expect(link.getAttribute('href')).toBe('/inbox');
    expect(link.textContent).toContain('waiting on you');
  });

  it('fires onChange when a different range is picked, and not for the current one', () => {
    const onChange = vi.fn();
    renderStrip(onChange);
    const group = screen.getByRole('radiogroup', { name: 'range' });
    expect(group).toBeTruthy();
    expect(screen.getByRole('radio', { name: '14 d' }).getAttribute('aria-checked')).toBe('true');
    fireEvent.click(screen.getByRole('radio', { name: '30 d' }));
    expect(onChange).toHaveBeenCalledWith('30');
    fireEvent.click(screen.getByRole('radio', { name: '14 d' }));
    expect(onChange).toHaveBeenCalledTimes(1);
  });
});
