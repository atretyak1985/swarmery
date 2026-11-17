// @vitest-environment jsdom
//
// The composer never locks while the session works (as in Claude Code). Claims:
//
//   1. Idle: Send resumes the conversation at once.
//   2. A headless reply in flight: the box stays editable, Send queues, and the
//      queue goes out as ONE message when the reply finishes.
//   3. Stop during a reply cancels it and hands the queued text back to the box
//      instead of sending it.
//   4. A queued message can be removed before it goes.
//   5. A live terminal with a link mod: Send goes to the mod as the user's
//      prompt with the long wait window; Stop aborts the turn, not the process.
//   6. A live terminal without a link: the box is editable but has no Send.
//   7. Enter sends; Shift+Enter and an IME composition do not.
//
// Runs with the rest of the web suite: `npm test`.

import { afterEach, describe, expect, it, vi } from 'vitest';
import * as api from '../../api';
import type { ProcState } from '../../api/types';
import { act, cleanup, fireEvent, render, screen } from '../../test/render';
import { CommandInput, LINK_PROMPT_STALE_MS } from './CommandInput';

vi.mock('../../api', () => ({
  sendSessionMessage: vi.fn(async () => undefined),
  cancelSessionMessage: vi.fn(async () => undefined),
  killSession: vi.fn(async () => undefined),
  sendLinkPrompt: vi.fn(async () => ({ commandId: 1 })),
  stopLinkTurn: vi.fn(async () => ({ commandId: 2 })),
}));

interface Props {
  procState?: ProcState | null;
  resumeInFlight?: boolean;
  linkControllable?: boolean;
}

function setup(initial: Props = {}): { rerender: (p: Props) => void; onSent: ReturnType<typeof vi.fn> } {
  const onSent = vi.fn();
  const el = (p: Props): JSX.Element => (
    <CommandInput
      sessionId={7}
      procState={p.procState ?? 'exited'}
      resumeInFlight={p.resumeInFlight ?? false}
      linkControllable={p.linkControllable ?? false}
      onSent={onSent}
    />
  );
  const { rerender } = render(el(initial));
  return { rerender: (p) => rerender(el(p)), onSent };
}

const box = (): HTMLTextAreaElement => screen.getByRole('textbox', { name: 'Message this session' }) as HTMLTextAreaElement;
const type = (text: string): void => {
  fireEvent.change(box(), { target: { value: text } });
};
const button = (name: string): HTMLButtonElement => screen.getByRole('button', { name }) as HTMLButtonElement;

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe('CommandInput', () => {
  it('sends straight away while the session is idle', () => {
    const { onSent } = setup();
    type('hello');
    fireEvent.click(button('Send'));
    expect(api.sendSessionMessage).toHaveBeenCalledWith(7, 'hello');
    expect(onSent).toHaveBeenCalledWith('hello');
    expect(box().value).toBe('');
  });

  it('queues while a reply is generated and sends the queue as one message after it', async () => {
    const { rerender, onSent } = setup({ resumeInFlight: true });
    expect(box().disabled).toBe(false);
    type('first');
    fireEvent.click(button('Queue'));
    type('second');
    fireEvent.keyDown(box(), { key: 'Enter' });
    expect(api.sendSessionMessage).not.toHaveBeenCalled();
    expect(screen.getByRole('list', { name: 'Queued messages' }).textContent).toContain('second');

    await act(async () => {
      rerender({ resumeInFlight: false });
    });
    expect(api.sendSessionMessage).toHaveBeenCalledTimes(1);
    expect(api.sendSessionMessage).toHaveBeenCalledWith(7, 'first\n\nsecond');
    expect(onSent).toHaveBeenCalledWith('first\n\nsecond');
    expect(screen.queryByRole('list', { name: 'Queued messages' })).toBeNull();
  });

  it('hands the queue back to the box when the reply is stopped', async () => {
    const { rerender } = setup({ resumeInFlight: true });
    type('queued one');
    fireEvent.click(button('Queue'));
    type('still typing');
    fireEvent.click(button('Stop'));
    expect(api.cancelSessionMessage).toHaveBeenCalledWith(7);

    await act(async () => {
      rerender({ resumeInFlight: false });
    });
    expect(api.sendSessionMessage).not.toHaveBeenCalled();
    expect(box().value).toBe('queued one\n\nstill typing');
  });

  it('drops a queued message on remove', async () => {
    const { rerender } = setup({ resumeInFlight: true });
    type('never mind');
    fireEvent.click(button('Queue'));
    fireEvent.click(button('Remove queued message'));
    await act(async () => {
      rerender({ resumeInFlight: false });
    });
    expect(api.sendSessionMessage).not.toHaveBeenCalled();
  });

  it('routes a live linked session through the mod and stops only the turn', async () => {
    const { onSent } = setup({ procState: 'running', linkControllable: true });
    type('also check the logs');
    fireEvent.click(button('Send'));
    expect(api.sendLinkPrompt).toHaveBeenCalledWith(7, 'also check the logs', true);
    expect(onSent).toHaveBeenCalledWith('also check the logs', LINK_PROMPT_STALE_MS);

    await act(async () => {
      fireEvent.click(button('Stop'));
    });
    expect(api.stopLinkTurn).toHaveBeenCalledWith(7);
    expect(api.killSession).not.toHaveBeenCalled();
    expect(button('Stop').disabled).toBe(false);
  });

  it('sends on Enter and leaves Shift+Enter and IME composition to the textarea', () => {
    setup();
    type('line one');
    const shift = fireEvent.keyDown(box(), { key: 'Enter', shiftKey: true });
    expect(shift).toBe(true); // not prevented: the browser inserts the newline
    fireEvent.keyDown(box(), { key: 'Enter', isComposing: true });
    expect(api.sendSessionMessage).not.toHaveBeenCalled();
    const plain = fireEvent.keyDown(box(), { key: 'Enter' });
    expect(plain).toBe(false); // prevented: no stray newline
    expect(api.sendSessionMessage).toHaveBeenCalledWith(7, 'line one');
  });

  it('keeps the box editable but offers no Send for a live terminal without a link', () => {
    setup({ procState: 'running' });
    expect(box().disabled).toBe(false);
    expect(screen.queryByRole('button', { name: 'Send' })).toBeNull();
    fireEvent.click(button('Stop'));
    expect(api.killSession).toHaveBeenCalledWith(7);
  });
});
