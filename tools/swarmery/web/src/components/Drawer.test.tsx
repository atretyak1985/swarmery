// @vitest-environment jsdom
//
// Drawer (Canvas v3 phase 2): the overlay's focus and keyboard contract — a
// labelled modal dialog, focus moves in on open and returns to the opener on
// close, Esc closes, Tab stays inside, ↑/↓ step when onPrev/onNext are given
// (but not while typing).
//
// Runs with the rest of the web suite: `npm test` (vitest, also a swarmery-ci
// step). On its own: `npx vitest run src/components/Drawer.test.tsx`.
// web/tsconfig.json EXCLUDES *.test.tsx, and vitest transpiles without type
// checking, so NOTHING type-checks this file — treat its types as documentation.

import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { useState } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { Drawer } from './Drawer';

function Harness({ onPrev, onNext }: { onPrev?: () => void; onNext?: () => void }): JSX.Element {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>
        open phase
      </button>
      <Drawer
        open={open}
        onClose={() => setOpen(false)}
        title="API: bidding mode"
        subtitle="Phase 3 of 4"
        footer={<button type="button">revise plan</button>}
        {...(onPrev !== undefined ? { onPrev } : {})}
        {...(onNext !== undefined ? { onNext } : {})}
      >
        <input aria-label="note" />
      </Drawer>
    </>
  );
}

afterEach(cleanup);

function openDrawer(): HTMLElement {
  const opener = screen.getByRole('button', { name: 'open phase' });
  opener.focus();
  fireEvent.click(opener);
  return opener;
}

describe('Drawer', () => {
  it('renders nothing while closed', () => {
    render(<Harness />);
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('opens as a labelled modal dialog and takes focus', () => {
    render(<Harness />);
    openDrawer();
    const dialog = screen.getByRole('dialog', { name: 'API: bidding mode' });
    expect(dialog.getAttribute('aria-modal')).toBe('true');
    expect(dialog.contains(document.activeElement)).toBe(true);
  });

  it('closes on Esc and returns focus to the opener', () => {
    render(<Harness />);
    const opener = openDrawer();
    fireEvent.keyDown(document, { key: 'Escape' });
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(document.activeElement).toBe(opener);
  });

  it('closes from the esc button and on a backdrop click', () => {
    render(<Harness />);
    openDrawer();
    fireEvent.click(screen.getByRole('button', { name: 'close' }));
    expect(screen.queryByRole('dialog')).toBeNull();
    openDrawer();
    const dialog = screen.getByRole('dialog');
    fireEvent.click(dialog); // inside the panel: stays open
    expect(screen.getByRole('dialog')).toBeTruthy();
    fireEvent.click(dialog.parentElement as HTMLElement);
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('wraps Tab inside the panel', () => {
    render(<Harness />);
    openDrawer();
    const last = screen.getByRole('button', { name: 'revise plan' });
    last.focus();
    fireEvent.keyDown(document, { key: 'Tab' });
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'close' }));
  });

  it('steps with ↑/↓ when onPrev/onNext are given, but not while typing', () => {
    const onPrev = vi.fn();
    const onNext = vi.fn();
    render(<Harness onPrev={onPrev} onNext={onNext} />);
    openDrawer();
    fireEvent.keyDown(document, { key: 'ArrowDown' });
    fireEvent.keyDown(document, { key: 'ArrowUp' });
    expect(onNext).toHaveBeenCalledTimes(1);
    expect(onPrev).toHaveBeenCalledTimes(1);
    fireEvent.keyDown(screen.getByRole('textbox', { name: 'note' }), { key: 'ArrowDown' });
    expect(onNext).toHaveBeenCalledTimes(1);
    expect(screen.getByText('↑↓ next')).toBeTruthy();
  });
});
