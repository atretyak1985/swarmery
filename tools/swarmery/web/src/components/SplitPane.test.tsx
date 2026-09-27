// @vitest-environment jsdom
//
// SplitPane (Canvas v3 phase 2): the triage keyboard — j/k move the selection,
// keymap keys act on the selected item, every key is suppressed while typing in
// a field or with a modifier held, and the footer hints come from the keymap.
//
// Dev-only suite (web/tsconfig.json excludes *.test.tsx). Run with
//   npx vitest run src/components/SplitPane.test.tsx

import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { useState } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { SplitPane, type SplitPaneKey } from './SplitPane';

interface Item {
  id: string;
  title: string;
}
const ITEMS: Item[] = [
  { id: 'a', title: 'Bash · reinstall node_modules' },
  { id: 'b', title: 'AskUserQuestion · 2 questions' },
  { id: 'c', title: 'New lesson' },
];

function Harness({ keymap, initial = 'a' }: { keymap?: SplitPaneKey<Item>[]; initial?: string | null }): JSX.Element {
  const [selected, setSelected] = useState<string | null>(initial);
  return (
    <>
      <input aria-label="search" />
      <SplitPane
        items={ITEMS}
        getId={(it) => it.id}
        renderRow={(it) => <span>{it.title}</span>}
        renderDetail={(it) => <h2>detail {it.id}</h2>}
        selectedId={selected}
        onSelect={setSelected}
        ariaLabel="inbox"
        {...(keymap !== undefined ? { keymap } : {})}
      />
    </>
  );
}

afterEach(cleanup);

function selectedDetail(): string | null {
  return screen.queryByRole('heading')?.textContent ?? null;
}

describe('SplitPane', () => {
  it('renders the list and the selected item detail', () => {
    render(<Harness />);
    expect(screen.getAllByRole('option')).toHaveLength(3);
    expect(screen.getByRole('option', { selected: true }).textContent).toContain('reinstall');
    expect(selectedDetail()).toBe('detail a');
  });

  it('moves down with j and up with k, clamped at both ends', () => {
    render(<Harness />);
    fireEvent.keyDown(window, { key: 'j' });
    expect(selectedDetail()).toBe('detail b');
    fireEvent.keyDown(window, { key: 'j' });
    fireEvent.keyDown(window, { key: 'j' });
    expect(selectedDetail()).toBe('detail c');
    fireEvent.keyDown(window, { key: 'k' });
    fireEvent.keyDown(window, { key: 'k' });
    fireEvent.keyDown(window, { key: 'k' });
    expect(selectedDetail()).toBe('detail a');
  });

  it('starts at the first item when nothing is selected', () => {
    render(<Harness initial={null} />);
    expect(selectedDetail()).toBeNull();
    fireEvent.keyDown(window, { key: 'j' });
    expect(selectedDetail()).toBe('detail a');
  });

  it('runs keymap bindings on the selected item and lists them in the footer', () => {
    const approve = vi.fn();
    const deny = vi.fn();
    render(
      <Harness
        keymap={[
          { key: 'e', label: 'approve', run: approve },
          { key: 'x', label: 'deny', run: deny },
        ]}
      />,
    );
    fireEvent.keyDown(window, { key: 'j' });
    fireEvent.keyDown(window, { key: 'e' });
    expect(approve).toHaveBeenCalledWith(ITEMS[1]);
    expect(deny).not.toHaveBeenCalled();
    expect(screen.getByText('j/k move')).toBeTruthy();
    expect(screen.getByText('e approve')).toBeTruthy();
    expect(screen.getByText('x deny')).toBeTruthy();
  });

  it('ignores keys while typing in an input or with a modifier held', () => {
    const approve = vi.fn();
    render(<Harness keymap={[{ key: 'e', label: 'approve', run: approve }]} />);
    const input = screen.getByRole('textbox', { name: 'search' });
    fireEvent.keyDown(input, { key: 'j' });
    fireEvent.keyDown(input, { key: 'e' });
    expect(selectedDetail()).toBe('detail a');
    expect(approve).not.toHaveBeenCalled();
    fireEvent.keyDown(window, { key: 'j', metaKey: true });
    expect(selectedDetail()).toBe('detail a');
  });

  it('selects on click', () => {
    render(<Harness />);
    fireEvent.click(screen.getByText('New lesson'));
    expect(selectedDetail()).toBe('detail c');
  });
});
