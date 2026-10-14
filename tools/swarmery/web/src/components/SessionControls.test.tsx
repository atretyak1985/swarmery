// @vitest-environment jsdom
//
// Link-mod remote control (mods-swarmery-link phase 4, SC-6). The claims:
//
//   1. Without `link`, or with link.controllable=false, both Stop turn and
//      Send prompt are disabled and point at a reason; with controllable=true
//      both are enabled.
//   2. Stop turn takes a confirm step: the first click calls no API.
//   3. Send prompt sends {text, asUser}, and the copy says whose message it is.
//   4. The command's state follows the link_command WS frames, including a
//      frame that beats the 202, and a failure shows its message.
//   5. The bulk bar renders the server's queued and skipped (with reasons).
//
// `npx vitest run src/components/SessionControls.test.tsx`.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { LinkCommandNote, Session, SessionLink, WSMessage } from '../api/types';
import { act, cleanup, fireEvent, render, screen } from '../test/render';
import { BulkPromptBar } from './BulkPromptBar';
import { SessionControls } from './SessionControls';

const api = vi.hoisted(() => ({
  stopLinkTurn: vi.fn(),
  sendLinkPrompt: vi.fn(),
  bulkLinkPrompt: vi.fn(),
}));
vi.mock('../api', () => api);

// Capture the WS handler the watcher subscribes with.
const ws = vi.hoisted(() => ({ handlers: new Set<(m: WSMessage) => void>() }));
vi.mock('../lib/ws', async () => {
  const react = await import('react');
  return {
    useLiveUpdates: (onMessage: (m: WSMessage) => void): void => {
      react.useEffect(() => {
        ws.handlers.add(onMessage);
        return () => {
          ws.handlers.delete(onMessage);
        };
      }, [onMessage]);
    },
  };
});

function emit(note: LinkCommandNote): void {
  act(() => {
    for (const h of ws.handlers) h({ type: 'link_command', payload: note });
  });
}

beforeEach(() => {
  api.stopLinkTurn.mockReset();
  api.sendLinkPrompt.mockReset();
  api.bulkLinkPrompt.mockReset();
  ws.handlers.clear();
});
afterEach(cleanup);

function makeLink(over: Partial<SessionLink> = {}): SessionLink {
  return {
    status: 'working',
    seenAt: new Date().toISOString(),
    live: true,
    contextPct: 40,
    rateLimits: [],
    costUsd: 0.2,
    controllable: true,
    ...over,
  };
}

function makeSession(over: Partial<Session> = {}): Session {
  return {
    id: 7,
    projectId: 1,
    projectSlug: 'proj',
    projectName: 'proj',
    sessionUuid: 'uuid-7',
    model: 'claude-opus-5-5',
    gitBranch: 'main',
    cwd: '/work/proj',
    status: 'active',
    startedAt: new Date(Date.now() - 60_000).toISOString(),
    endedAt: null,
    title: 'steer me',
    source: 'both',
    ...over,
  } as Session;
}

const stopBtn = (): HTMLButtonElement => screen.getByRole('button', { name: 'Stop turn' });
const promptBtn = (): HTMLButtonElement => screen.getByRole('button', { name: 'Send prompt' });

describe('SessionControls availability', () => {
  it('is disabled with a reason when the session has no link', () => {
    render(<SessionControls session={makeSession({ link: null })} />);
    expect(stopBtn().disabled).toBe(true);
    expect(promptBtn().disabled).toBe(true);
    const reasonId = stopBtn().getAttribute('aria-describedby');
    expect(reasonId).not.toBeNull();
    expect(document.getElementById(reasonId ?? '')?.textContent).toMatch(/No swarmery link mod/);
  });

  it('is disabled with a reason when the mod is not polling', () => {
    render(<SessionControls session={makeSession({ link: makeLink({ controllable: false }) })} />);
    expect(stopBtn().disabled).toBe(true);
    expect(promptBtn().disabled).toBe(true);
    const reasonId = promptBtn().getAttribute('aria-describedby');
    expect(document.getElementById(reasonId ?? '')?.textContent).toMatch(/not polling for commands/);
  });

  it('is enabled when link.controllable', () => {
    render(<SessionControls session={makeSession({ link: makeLink() })} />);
    expect(stopBtn().disabled).toBe(false);
    expect(promptBtn().disabled).toBe(false);
    expect(stopBtn().getAttribute('aria-describedby')).toBeNull();
  });
});

describe('Stop turn', () => {
  it('asks for confirmation before calling the API, then follows the WS state', async () => {
    let resolve: (v: { commandId: number }) => void = () => undefined;
    api.stopLinkTurn.mockReturnValue(new Promise((r) => (resolve = r)));
    render(<SessionControls session={makeSession({ link: makeLink() })} />);

    fireEvent.click(stopBtn());
    expect(api.stopLinkTurn).not.toHaveBeenCalled();
    expect(screen.getByText('Abort the running turn?')).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: 'Confirm stop' }));
    expect(api.stopLinkTurn).toHaveBeenCalledWith(7);
    expect(screen.getByTestId('command-state').textContent).toBe('Stop turn · sending…');

    // The mod was waiting: "delivered" arrives before the 202 does.
    emit({ sessionId: 7, commandId: 31, kind: 'stop', state: 'queued' });
    emit({ sessionId: 7, commandId: 31, kind: 'stop', state: 'delivered' });
    await act(async () => {
      resolve({ commandId: 31 });
    });
    expect(screen.getByTestId('command-state').textContent).toBe('Stop turn · delivered to the session');

    // Another session's frame is ignored; ours lands.
    emit({ sessionId: 8, commandId: 31, kind: 'stop', state: 'failed', message: 'nope' });
    expect(screen.getByTestId('command-state').textContent).toBe('Stop turn · delivered to the session');
    emit({ sessionId: 7, commandId: 31, kind: 'stop', state: 'done' });
    expect(screen.getByTestId('command-state').textContent).toBe('Stop turn · done');
    expect(stopBtn().disabled).toBe(false);
  });

  it('cancel leaves the API untouched', () => {
    render(<SessionControls session={makeSession({ link: makeLink() })} />);
    fireEvent.click(stopBtn());
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(api.stopLinkTurn).not.toHaveBeenCalled();
    expect(stopBtn()).toBeTruthy();
  });
});

describe('Send prompt', () => {
  it('sends {text, asUser} and says whose message it is', async () => {
    api.sendLinkPrompt.mockResolvedValue({ commandId: 5 });
    render(<SessionControls session={makeSession({ link: makeLink() })} />);
    fireEvent.click(promptBtn());
    expect(screen.getByTestId('prompt-origin').textContent).toMatch(/from the swarmery link mod, not from you/);

    fireEvent.change(screen.getByRole('textbox', { name: 'Prompt to send into the session' }), {
      target: { value: 'run the tests' },
    });
    fireEvent.click(screen.getByRole('checkbox', { name: 'as user' }));
    expect(screen.getByTestId('prompt-origin').textContent).toMatch(/as if you typed it/);

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Send' }));
    });
    expect(api.sendLinkPrompt).toHaveBeenCalledWith(7, 'run the tests', true);
    expect(screen.getByTestId('command-state').textContent).toBe('Prompt · queued, waiting for the mod to pick it up');

    emit({ sessionId: 7, commandId: 5, kind: 'prompt', state: 'delivered' });
    emit({ sessionId: 7, commandId: 5, kind: 'prompt', state: 'queued', message: 'turn in progress' });
    expect(screen.getByTestId('command-state').textContent).toBe('Prompt · queued — waits for the running turn to end');
    emit({ sessionId: 7, commandId: 5, kind: 'prompt', state: 'failed', message: 'submit rejected' });
    expect(screen.getByTestId('command-state').textContent).toBe('Prompt · failed: submit rejected');
  });

  it('shows the server refusal and an expiry', async () => {
    api.sendLinkPrompt.mockRejectedValueOnce(new Error('session has no link'));
    render(<SessionControls session={makeSession({ link: makeLink() })} />);
    fireEvent.click(promptBtn());
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'x' } });
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Send' }));
    });
    expect(screen.getByTestId('command-state').textContent).toBe('Prompt · not sent: session has no link');

    api.sendLinkPrompt.mockResolvedValueOnce({ commandId: 9 });
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Send' }));
    });
    emit({ sessionId: 7, commandId: 9, kind: 'prompt', state: 'expired' });
    expect(screen.getByTestId('command-state').textContent).toBe('Prompt · expired — the mod did not pick it up in time');
  });
});

describe('BulkPromptBar', () => {
  it('sends the editable default text and lists queued and skipped with reasons', async () => {
    api.bulkLinkPrompt.mockResolvedValue({
      queued: [{ id: 1, commandId: 11 }],
      skipped: [
        { id: 2, reason: 'session has no link' },
        { id: 3, reason: 'not found' },
      ],
    });
    const byId = new Map<number, Session>([
      [1, makeSession({ id: 1, title: 'alpha' })],
      [2, makeSession({ id: 2, title: 'beta' })],
    ]);
    render(
      <BulkPromptBar selected={[1, 2, 3]} selectableCount={2} byId={byId} onSelectAll={() => undefined} onClear={() => undefined} />,
    );
    const box = screen.getByRole('textbox', { name: 'Prompt for the selected sessions' }) as HTMLTextAreaElement;
    expect(box.value).toBe(
      'Continue with the next phase of the plan per its phase doc; do not ask for confirmation if the criteria are clear',
    );
    fireEvent.change(box, { target: { value: 'next phase please' } });
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Continue phase on 3 sessions' }));
    });
    expect(api.bulkLinkPrompt).toHaveBeenCalledWith([1, 2, 3], 'next phase please');
    expect(screen.getAllByTestId('bulk-queued').map((e) => e.textContent)).toEqual(['alpha']);
    expect(screen.getAllByTestId('bulk-skipped').map((e) => e.textContent)).toEqual([
      'beta — no polling link mod',
      '#3 — session not found',
    ]);
  });

  it('cannot send with nothing selected', () => {
    render(
      <BulkPromptBar selected={[]} selectableCount={0} byId={new Map()} onSelectAll={() => undefined} onClear={() => undefined} />,
    );
    expect((screen.getByRole('button', { name: 'Continue phase on 0 sessions' }) as HTMLButtonElement).disabled).toBe(true);
  });
});
