// @vitest-environment jsdom
//
// The global keyboard (Swarmery v4 §10). The claims:
//
//   1. Each key reaches its handler, and a key nobody handles keeps its default.
//   2. Nothing fires while typing, in a contenteditable, under a modal dialog or
//      with a modifier held: `e` and `x` are real actions.
//   3. Two consumers coexist: the shell's keys and a list's keys both work.
//
// Runs with the rest of the web suite: `npm test`.

import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render } from '../test/render';
import { isTypingTarget, stepKey, useGlobalKeys, type GlobalKeyHandlers } from './useGlobalKeys';

function Probe({ handlers }: { handlers: GlobalKeyHandlers }): JSX.Element {
  useGlobalKeys(handlers);
  return <p>probe</p>;
}

afterEach(() => {
  cleanup();
  document.body.innerHTML = '';
});

describe('useGlobalKeys', () => {
  it('maps 1–5 to zero-based places', () => {
    const onPlace = vi.fn();
    render(<Probe handlers={{ onPlace }} />);
    for (const key of ['1', '2', '3', '4', '5']) fireEvent.keyDown(document.body, { key });
    expect(onPlace.mock.calls.map(([i]) => i)).toEqual([0, 1, 2, 3, 4]);
  });

  it('leaves 6 alone', () => {
    const onPlace = vi.fn();
    render(<Probe handlers={{ onPlace }} />);
    fireEvent.keyDown(document.body, { key: '6' });
    expect(onPlace).not.toHaveBeenCalled();
  });

  it.each([
    ['t', 'onTheme'],
    ['j', 'onNext'],
    ['ArrowDown', 'onNext'],
    ['k', 'onPrev'],
    ['ArrowUp', 'onPrev'],
    ['e', 'onPrimary'],
    ['x', 'onSecondary'],
    ['Escape', 'onEscape'],
  ] as const)('%s calls %s', (key, handler) => {
    const fn = vi.fn();
    render(<Probe handlers={{ [handler]: fn }} />);
    fireEvent.keyDown(document.body, { key });
    expect(fn).toHaveBeenCalledOnce();
  });

  it('keeps the browser default for a key nobody handles', () => {
    render(<Probe handlers={{ onTheme: () => undefined }} />);
    // fireEvent returns false only when the default was prevented.
    expect(fireEvent.keyDown(document.body, { key: 'ArrowDown' })).toBe(true);
    expect(fireEvent.keyDown(document.body, { key: 't' })).toBe(false);
  });

  it.each(['input', 'textarea', 'select'])('ignores keys typed in a %s', (tag) => {
    const onTheme = vi.fn();
    render(<Probe handlers={{ onTheme }} />);
    const field = document.body.appendChild(document.createElement(tag));
    fireEvent.keyDown(field, { key: 't' });
    expect(onTheme).not.toHaveBeenCalled();
  });

  it('ignores keys typed in a contenteditable, including its children', () => {
    const onPrimary = vi.fn();
    render(<Probe handlers={{ onPrimary }} />);
    const editor = document.body.appendChild(document.createElement('div'));
    editor.setAttribute('contenteditable', 'true');
    const inner = editor.appendChild(document.createElement('span'));
    fireEvent.keyDown(inner, { key: 'e' });
    expect(onPrimary).not.toHaveBeenCalled();
  });

  it('ignores keys while a modal dialog is open', () => {
    const onSecondary = vi.fn();
    render(<Probe handlers={{ onSecondary }} />);
    const dialog = document.body.appendChild(document.createElement('div'));
    dialog.setAttribute('aria-modal', 'true');
    fireEvent.keyDown(document.body, { key: 'x' });
    expect(onSecondary).not.toHaveBeenCalled();
  });

  it.each(['metaKey', 'ctrlKey', 'altKey'])('ignores a key held with %s', (modifier) => {
    const onPlace = vi.fn();
    render(<Probe handlers={{ onPlace }} />);
    fireEvent.keyDown(document.body, { key: '1', [modifier]: true });
    expect(onPlace).not.toHaveBeenCalled();
  });

  it('lets the shell and a list listen side by side', () => {
    const onTheme = vi.fn();
    const onNext = vi.fn();
    render(
      <>
        <Probe handlers={{ onTheme }} />
        <Probe handlers={{ onNext }} />
      </>,
    );
    fireEvent.keyDown(document.body, { key: 't' });
    fireEvent.keyDown(document.body, { key: 'j' });
    expect(onTheme).toHaveBeenCalledOnce();
    expect(onNext).toHaveBeenCalledOnce();
  });

  it('calls the latest closure, not the one from the first render', () => {
    const first = vi.fn();
    const second = vi.fn();
    const { rerender } = render(<Probe handlers={{ onTheme: first }} />);
    rerender(<Probe handlers={{ onTheme: second }} />);
    fireEvent.keyDown(document.body, { key: 't' });
    expect(first).not.toHaveBeenCalled();
    expect(second).toHaveBeenCalledOnce();
  });
});

describe('isTypingTarget', () => {
  it('is false for a plain element and for a null target', () => {
    expect(isTypingTarget(document.body)).toBe(false);
    expect(isTypingTarget(null)).toBe(false);
  });

  it('treats contenteditable="false" as not editable', () => {
    const el = document.body.appendChild(document.createElement('div'));
    el.setAttribute('contenteditable', 'false');
    expect(isTypingTarget(el)).toBe(false);
  });
});

describe('stepKey', () => {
  const keys = ['a', 'b', 'c'];

  it('steps both ways and clamps at the ends', () => {
    expect(stepKey(keys, 'a', 1)).toBe('b');
    expect(stepKey(keys, 'b', -1)).toBe('a');
    expect(stepKey(keys, 'c', 1)).toBe('c');
    expect(stepKey(keys, 'a', -1)).toBe('a');
  });

  it('starts at the first row when nothing is selected', () => {
    expect(stepKey(keys, null, 1)).toBe('a');
    expect(stepKey(keys, 'gone', 1)).toBe('a');
  });

  it('has nowhere to go in an empty list', () => {
    expect(stepKey([], null, 1)).toBeUndefined();
  });
});
