// @vitest-environment jsdom
//
// The `⋯` menu button. The claims:
//
//   1. It is a WAI-ARIA menu: role=menu with role=menuitem entries, opened from the
//      keyboard onto the first item (ArrowUp: the last), moved through by arrows.
//   2. Escape closes it and puts focus back on the trigger; picking an item does too.
//   3. A click outside and Tab close it.
//   4. A disabled item stays listed with its reason in grey and does nothing.

import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '../test/render';
import { OverflowMenu, type OverflowMenuItem } from './OverflowMenu';

afterEach(cleanup);

function setup(over: Partial<Record<string, Partial<OverflowMenuItem>>> = {}): {
  trigger: HTMLElement;
  picked: Record<string, ReturnType<typeof vi.fn>>;
} {
  const picked = { extract: vi.fn(), stop: vi.fn(), rename: vi.fn() };
  const items: OverflowMenuItem[] = [
    { id: 'extract', label: 'extract tasks', onSelect: picked.extract, ...over.extract },
    { id: 'stop', label: 'stop process…', onSelect: picked.stop, tone: 'red', ...over.stop },
    { id: 'rename', label: 'rename', onSelect: picked.rename, ...over.rename },
  ];
  render(
    <div>
      <OverflowMenu label="more for this session" items={items} />
      <button type="button">outside</button>
    </div>,
  );
  return { trigger: screen.getByRole('button', { name: 'more for this session' }), picked };
}

const menu = (): HTMLElement | null => screen.queryByRole('menu', { name: 'more for this session' });
const item = (name: RegExp): HTMLElement => screen.getByRole('menuitem', { name });

describe('OverflowMenu', () => {
  it('opens from the keyboard onto the first item, as a menu of menuitems', () => {
    const { trigger } = setup();
    expect(trigger.getAttribute('aria-haspopup')).toBe('menu');
    expect(trigger.getAttribute('aria-expanded')).toBe('false');
    trigger.focus();
    fireEvent.keyDown(trigger, { key: 'Enter' });
    expect(menu()).not.toBeNull();
    expect(trigger.getAttribute('aria-expanded')).toBe('true');
    expect(screen.getAllByRole('menuitem')).toHaveLength(3);
    expect(document.activeElement).toBe(item(/extract tasks/));
  });

  it('opens on the last item with ArrowUp, and the arrows, Home and End move and wrap', () => {
    const { trigger } = setup();
    fireEvent.keyDown(trigger, { key: 'ArrowUp' });
    expect(document.activeElement).toBe(item(/rename/));
    const list = menu()!;
    fireEvent.keyDown(list, { key: 'ArrowDown' });
    expect(document.activeElement).toBe(item(/extract tasks/));
    fireEvent.keyDown(list, { key: 'ArrowDown' });
    expect(document.activeElement).toBe(item(/stop process/));
    fireEvent.keyDown(list, { key: 'ArrowUp' });
    fireEvent.keyDown(list, { key: 'ArrowUp' });
    expect(document.activeElement).toBe(item(/rename/));
    fireEvent.keyDown(list, { key: 'Home' });
    expect(document.activeElement).toBe(item(/extract tasks/));
    fireEvent.keyDown(list, { key: 'End' });
    expect(document.activeElement).toBe(item(/rename/));
  });

  it('closes on Escape and gives focus back to the trigger', () => {
    const { trigger } = setup();
    fireEvent.keyDown(trigger, { key: 'ArrowDown' });
    fireEvent.keyDown(menu()!, { key: 'Escape' });
    expect(menu()).toBeNull();
    expect(document.activeElement).toBe(trigger);
    expect(trigger.getAttribute('aria-expanded')).toBe('false');
  });

  it('runs a picked item, closes, and gives focus back to the trigger', () => {
    const { trigger, picked } = setup();
    fireEvent.click(trigger);
    fireEvent.click(item(/rename/));
    expect(picked.rename).toHaveBeenCalledTimes(1);
    expect(menu()).toBeNull();
    expect(document.activeElement).toBe(trigger);
  });

  it('closes on a click outside and on Tab', () => {
    const { trigger } = setup();
    fireEvent.click(trigger);
    fireEvent.mouseDown(screen.getByRole('button', { name: 'outside' }));
    expect(menu()).toBeNull();
    fireEvent.click(trigger);
    fireEvent.keyDown(menu()!, { key: 'Tab' });
    expect(menu()).toBeNull();
  });

  it('keeps a disabled item listed, with its reason, and does nothing on it', () => {
    const { trigger, picked } = setup({ stop: { disabledReason: 'the process already exited' } });
    fireEvent.click(trigger);
    const stop = item(/stop process/);
    expect(stop.getAttribute('aria-disabled')).toBe('true');
    expect(stop.textContent).toContain('the process already exited');
    fireEvent.click(stop);
    expect(picked.stop).not.toHaveBeenCalled();
    expect(menu()).not.toBeNull();
  });
});
