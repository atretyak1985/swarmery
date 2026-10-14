// Remote control of a live session through its swarmery link mod
// (mods-swarmery-link phase 4, SC-6; docs/mods-link-protocol.md "Commands").
//
//   Stop turn    — queues a `stop`: the mod runs $.turn.abort(). The session
//                  stays open. Danger action, so it takes a confirm step.
//   Send prompt  — queues a `prompt`: the mod runs $.prompt.submit(). The
//                  "as user" toggle decides whether it arrives as the mod's
//                  message (default) or as if the operator typed it.
//
// Not to be confused with StopButton (POST /api/sessions/{id}/stop), which
// ends a daemon-spawned PROCESS: both can sit on one card, so this one always
// says "turn".
//
// Both buttons are enabled only while session.link.controllable — the
// server's "a mod polls the command queue for this session" — and say why
// when they are not. The command's progress (queued → delivered → done /
// failed / expired) comes from the link_command WS frame; the socket is
// subscribed only while a command is in flight, so a card that never sends
// one costs nothing.
//
// Each kind is tracked on its own: an unsettled prompt (one bounced by a
// running turn waits up to the prompt TTL) never disables Stop turn — that
// running turn is exactly what the operator may want to abort. Stop turn waits
// only for its own unsettled stop. A WS reconnect may have dropped frames, so
// it turns every unsettled command into "unknown", which releases its button.

import { useLingui } from '@lingui/react/macro';
import { useCallback, useId, useRef, useState, type KeyboardEvent, type MouseEvent } from 'react';
import { sendLinkPrompt, stopLinkTurn } from '../api';
import type {
  LinkCommandKind,
  LinkCommandNote,
  LinkCommandState,
  Session,
  WSMessage,
} from '../api/types';
import { useLiveUpdates } from '../lib/ws';

/** Terminal command states: nothing will move after these. */
const TERMINAL: ReadonlySet<LinkCommandState> = new Set(['done', 'failed', 'expired']);

/** Nothing more will happen to a command in this state (as far as the UI
 * will ever learn: 'unknown' means frames may have been lost). */
function settled(state: Tracked['state']): boolean {
  return state === 'rejected' || state === 'unknown' || (state !== 'sending' && TERMINAL.has(state));
}

const unsettled = (tr: Tracked | undefined): boolean => tr !== undefined && !settled(tr.state);

/** The exact ack message of a prompt bounced by a running turn (protocol). */
// i18n-ignore: protocol string the mod sends, compared, never shown
const TURN_IN_PROGRESS = 'turn in progress';

/** The command this control last sent, as far as the UI knows. */
interface Tracked {
  kind: LinkCommandKind;
  /** null while the POST is in flight. */
  commandId: number | null;
  /** 'sending' until the POST answers; then the latest WS state; 'unknown'
   * once a WS reconnect may have dropped its frames. */
  state: LinkCommandState | 'sending' | 'rejected' | 'unknown';
  /** The mod's ack message, or the HTTP error for 'rejected'. */
  message?: string | undefined;
}

/** The last command of each kind this control sent. */
type TrackedByKind = Partial<Record<LinkCommandKind, Tracked>>;

/** Display order of the per-kind status lines. */
const KINDS: readonly LinkCommandKind[] = ['stop', 'prompt'];

/** Subscribes to the WS bus while mounted and forwards this session's
 * link_command frames. Mounted only while a command is in flight. */
function CommandWatcher({
  sessionId,
  onNote,
  onReconnect,
}: {
  sessionId: number;
  onNote: (n: LinkCommandNote) => void;
  onReconnect: () => void;
}): null {
  const onMessage = useCallback(
    (msg: WSMessage): void => {
      if (msg.type === 'link_command' && msg.payload.sessionId === sessionId) onNote(msg.payload);
    },
    [sessionId, onNote],
  );
  useLiveUpdates(onMessage, onReconnect);
  return null;
}

const halt = (e: MouseEvent | KeyboardEvent): void => {
  e.stopPropagation();
};

const BTN =
  'rounded border px-2 py-0.5 font-mono text-[10.5px] font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-50';

export function SessionControls({
  session,
  compact = false,
  className = '',
}: {
  session: Session;
  /** Card layout: no inline reason line (the tip and the screen-reader
   * description still carry it). */
  compact?: boolean;
  className?: string;
}): JSX.Element {
  const { t } = useLingui();
  const reasonId = useId();
  const [confirmingStop, setConfirmingStop] = useState(false);
  const [promptOpen, setPromptOpen] = useState(false);
  const [text, setText] = useState('');
  const [asUser, setAsUser] = useState(false);
  const [tracked, setTracked] = useState<TrackedByKind>({});
  // Frames that arrive before the POST answers (a waiting mod picks a command
  // up at once, so "delivered" can beat the 202): kept by command id.
  const early = useRef(new Map<number, LinkCommandNote>());

  const link = session.link ?? null;
  const controllable = link?.controllable === true;
  const reason =
    link === null
      ? t`No swarmery link mod reports on this session, so it cannot be steered from the dashboard.`
      : t`The session's link mod is not polling for commands. Enable the swarmery-link "control" option and keep the session open.`;
  // A kind is in flight from the click until a terminal state (or an HTTP
  // refusal, or a reconnect): its own button waits and the WS watcher
  // listens meanwhile. Stop turn never waits for a prompt.
  const stopBusy = unsettled(tracked.stop);
  const promptBusy = unsettled(tracked.prompt);
  const inFlight = stopBusy || promptBusy;

  const onNote = useCallback((n: LinkCommandNote): void => {
    early.current.set(n.commandId, n);
    setTracked((prev) => {
      const cur = prev[n.kind];
      return cur !== undefined && cur.commandId === n.commandId
        ? { ...prev, [n.kind]: { ...cur, state: n.state, message: n.message } }
        : prev;
    });
  }, []);

  // Frames sent while the socket was down are gone: nothing will settle an
  // unsettled command now, so say so instead of waiting forever. A POST still
  // in the air ('sending') settles from its own answer.
  const onReconnect = useCallback((): void => {
    setTracked((prev) => {
      const next: TrackedByKind = { ...prev };
      for (const k of KINDS) {
        const cur = prev[k];
        if (cur !== undefined && cur.state !== 'sending' && !settled(cur.state)) {
          next[k] = { ...cur, state: 'unknown', message: undefined };
        }
      }
      return next;
    });
  }, []);

  const put = (kind: LinkCommandKind, tr: Tracked): void => {
    setTracked((prev) => ({ ...prev, [kind]: tr }));
  };

  const send = async (kind: LinkCommandKind, run: () => Promise<{ commandId: number }>): Promise<boolean> => {
    put(kind, { kind, commandId: null, state: 'sending' });
    try {
      const { commandId } = await run();
      const seen = early.current.get(commandId);
      early.current.delete(commandId);
      put(kind, { kind, commandId, state: seen?.state ?? 'queued', message: seen?.message });
      return true;
    } catch (err) {
      put(kind, { kind, commandId: null, state: 'rejected', message: err instanceof Error ? err.message : String(err) });
      return false;
    }
  };

  const doStop = async (): Promise<void> => {
    setConfirmingStop(false);
    await send('stop', () => stopLinkTurn(session.id));
  };

  const doPrompt = async (): Promise<void> => {
    if (text.trim() === '') return;
    if (await send('prompt', () => sendLinkPrompt(session.id, text, asUser))) {
      setText('');
      setPromptOpen(false);
    }
  };

  const kindLabel = (k: LinkCommandKind): string => (k === 'stop' ? t`Stop turn` : t`Prompt`);
  const stateLabel = (tr: Tracked): string => {
    const msg = tr.message ?? '';
    switch (tr.state) {
      case 'sending':
        return t`sending…`;
      case 'rejected':
        return t`not sent: ${msg}`;
      case 'queued':
        return msg === TURN_IN_PROGRESS
          ? t`queued — waits for the running turn to end`
          : t`queued, waiting for the mod to pick it up`;
      case 'delivered':
        return t`delivered to the session`;
      case 'done':
        return t`done`;
      case 'failed':
        return msg === '' ? t`failed` : t`failed: ${msg}`;
      case 'expired':
        return t`expired — the mod did not pick it up in time`;
      case 'unknown':
        return t`state unknown after reconnect`;
    }
  };
  const stateTone = (tr: Tracked): string =>
    tr.state === 'done'
      ? 'text-green'
      : tr.state === 'failed' || tr.state === 'rejected'
        ? 'text-red'
        : tr.state === 'expired'
          ? 'text-amber'
          : 'text-ink-dim';

  const disabled = !controllable;
  const describedBy = disabled ? reasonId : undefined;

  return (
    <span
      className={`flex min-w-0 flex-col gap-1 ${className}`}
      onClick={halt}
      onKeyDown={halt}
      data-testid="session-controls"
    >
      {inFlight && <CommandWatcher sessionId={session.id} onNote={onNote} onReconnect={onReconnect} />}
      <span className="flex flex-wrap items-center gap-1.5" data-tip={disabled ? reason : undefined}>
        {confirmingStop ? (
          <>
            <span className="font-mono text-[10.5px] text-ink-dim">{t`Abort the running turn?`}</span>
            <button
              type="button"
              onClick={() => void doStop()}
              className={`${BTN} border-red/50 bg-red/10 text-red hover:bg-red/20`}
            >
              {t`Confirm stop`}
            </button>
            <button
              type="button"
              onClick={() => setConfirmingStop(false)}
              className="font-mono text-[10.5px] text-ink-dim hover:text-ink"
            >
              {t`Cancel`}
            </button>
          </>
        ) : (
          <button
            type="button"
            disabled={disabled || stopBusy}
            aria-describedby={describedBy}
            onClick={() => setConfirmingStop(true)}
            data-tip={disabled ? undefined : t`Abort the session's running turn through its link mod. The session stays open — unlike Stop, which ends a daemon-run process.`}
            className={`${BTN} border-red/40 text-red hover:bg-red/10`}
          >
            {t`Stop turn`}
          </button>
        )}
        <button
          type="button"
          disabled={disabled || promptBusy}
          aria-describedby={describedBy}
          aria-expanded={promptOpen}
          onClick={() => setPromptOpen((o) => !o)}
          className={`${BTN} border-line-strong text-ink-dim hover:border-brand/50 hover:text-brand`}
        >
          {t`Send prompt`}
        </button>
        <span id={reasonId} className={disabled && !compact ? 'font-mono text-[10.5px] text-ink-faint' : 'sr-only'}>
          {disabled ? reason : ''}
        </span>
      </span>

      {promptOpen && controllable && (
        <span className="flex max-w-[520px] flex-col gap-1.5 rounded border border-line bg-surface2 p-2">
          <textarea
            value={text}
            onChange={(e) => setText(e.target.value)}
            rows={compact ? 2 : 3}
            aria-label={t`Prompt to send into the session`}
            placeholder={t`What should the session do next?`}
            className="w-full resize-y rounded border border-line bg-surface px-2 py-1 font-mono text-[11.5px] text-ink"
          />
          <label className="flex items-center gap-1.5 font-mono text-[10.5px] text-ink-dim">
            <input type="checkbox" checked={asUser} onChange={(e) => setAsUser(e.target.checked)} />
            {t`as user`}
          </label>
          <span className="font-mono text-[10.5px] text-ink-faint" data-testid="prompt-origin">
            {asUser
              ? t`Arrives in the session as if you typed it.`
              : t`Arrives as a message from the swarmery link mod, not from you.`}
          </span>
          <span className="flex items-center gap-1.5">
            <button
              type="button"
              disabled={text.trim() === '' || promptBusy}
              onClick={() => void doPrompt()}
              className={`${BTN} border-brand/50 bg-brand/10 text-brand hover:bg-brand/20`}
            >
              {t`Send`}
            </button>
            <button
              type="button"
              onClick={() => setPromptOpen(false)}
              className="font-mono text-[10.5px] text-ink-dim hover:text-ink"
            >
              {t`Cancel`}
            </button>
          </span>
        </span>
      )}

      {KINDS.map((k) => {
        const tr = tracked[k];
        return tr === undefined ? null : (
          <span
            key={k}
            role="status"
            className={`font-mono text-[10.5px] ${stateTone(tr)}`}
            data-testid="command-state"
            data-kind={k}
          >
            {kindLabel(tr.kind)} · {stateLabel(tr)}
          </span>
        );
      })}
    </span>
  );
}
