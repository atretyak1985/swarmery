// Chat composer: talk to this session's conversation from the dashboard.
//
// Like Claude Code's own prompt, the box never locks while the session works:
// you can keep typing, and what Send does depends on how the message can reach
// the conversation.
//
//   • idle / finished → Send resumes the conversation headlessly
//     (`claude -r <uuid> -p`); its new turns arrive via the WS bus.
//   • our own headless resume in flight (resumeInFlight) → Send QUEUES the
//     message here. When the reply finishes, the queue goes out as one message
//     (Claude Code merges queued prompts the same way). Stop cancels the run
//     (POST /api/sessions/{id}/message/cancel) and hands the queued text back
//     to the box for editing instead of sending it.
//   • live terminal process with a controllable link mod → Send goes to the
//     mod's command queue as the user's own prompt; the mod submits it once the
//     running turn ends. Stop aborts the running turn only (the session stays
//     open, like Esc in the terminal) — ending the process is the header's kill.
//   • live terminal process without a link → nothing can deliver the message
//     while it runs. The box stays editable (draft now), Send is off, Enter
//     keeps the draft and says why it did not go (the mod's `control` option, or
//     Stop), and Stop SIGTERMs the process (POST /api/sessions/{id}/kill) to
//     take it over.
//
// Gating on the live PROCESS (not the time-based status) means a session that
// merely reads "active" because we just appended to it stays writable.
//
// Send is optimistic: onSent(text) fires SYNCHRONOUSLY when a message actually
// leaves, so the parent paints a pending user bubble immediately; if the POST
// rejects, onSendFailed marks that bubble failed (its retry affordance
// re-sends).
//
// Keys follow Claude: Enter sends, Shift+Enter inserts a newline. Enter while
// an IME is composing (e.g. picking a candidate) never sends.

import { useLingui } from '@lingui/react/macro';
import { useEffect, useRef, useState, type RefObject } from 'react';
import type { ProcState } from '../../api/types';
import { cancelSessionMessage, killSession, sendLinkPrompt, sendSessionMessage, stopLinkTurn } from '../../api';
import { hasLiveProcess } from './redirectChannel';

/** internal/link/commands.go PromptTTL: an undelivered link prompt expires
 * after 10 minutes, so its bubble may wait that long for the turn to finish. */
export const LINK_PROMPT_STALE_MS = 10 * 60_000;

/** Several queued messages leave as one, in the order they were typed. */
export function joinQueued(queue: readonly string[]): string {
  return queue.join('\n\n');
}

type Mode = 'idle' | 'queue' | 'link' | 'blocked';

function composerMode(live: boolean, resumeInFlight: boolean, linkControllable: boolean): Mode {
  if (live) return linkControllable ? 'link' : 'blocked';
  return resumeInFlight ? 'queue' : 'idle';
}

export function CommandInput({
  sessionId,
  procState,
  resumeInFlight = false,
  linkControllable = false,
  onSent,
  onSendFailed,
  placeholder,
  inputRef,
}: {
  /** What a message does in this state; handoff §5. */
  placeholder?: string;
  /** Lets `redirect` and `resume with a reply` put the caret here. */
  inputRef?: RefObject<HTMLTextAreaElement | null>;
  sessionId: number;
  procState: ProcState | null | undefined;
  resumeInFlight?: boolean;
  /** A link mod polls this session's command queue, so a live terminal
   * session can still take a prompt. */
  linkControllable?: boolean;
  /** Called synchronously with the text the moment a message leaves — the
   * parent echoes it as a pending bubble immediately (before the POST
   * resolves). staleMs widens the bubble's wait when delivery is deferred. */
  onSent?: (text: string, staleMs?: number) => void;
  /** Called with the same text if the POST rejects, plus the server's reason, so
   * the parent can flip that optimistic bubble to a failed/retry state AND show
   * why. A refusal here is usually actionable — a live process to stop, a run
   * whose branch is gone — and a bare "failed" sends the operator guessing. */
  onSendFailed?: ((text: string, reason?: string) => void) | undefined;
}): JSX.Element {
  const { t } = useLingui();
  const [text, setText] = useState('');
  const [queue, setQueue] = useState<string[]>([]);
  const [sending, setSending] = useState(false);
  const [stopping, setStopping] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const live = hasLiveProcess(procState);
  const busy = live || resumeInFlight;
  const mode = composerMode(live, resumeInFlight, linkControllable);

  // The flush below runs on a busy → idle edge and must see the queue and the
  // "was it stopped" flag as they are at that moment, not as they were when the
  // effect was last bound.
  const queueRef = useRef(queue);
  queueRef.current = queue;
  const stoppedRef = useRef(false);
  const wasBusyRef = useRef(busy);

  const deliver = (message: string): void => {
    setSending(true);
    setError(null);
    onSent?.(message);
    sendSessionMessage(sessionId, message)
      .catch((e: unknown) => {
        // The pending bubble carries both the failure and its reason; the
        // composer itself stays clean (no duplicate inline error for a send).
        onSendFailed?.(message, e instanceof Error ? e.message : String(e));
      })
      .finally(() => setSending(false));
  };

  useEffect(() => {
    const wasBusy = wasBusyRef.current;
    wasBusyRef.current = busy;
    if (busy || !wasBusy) return;
    // The session went quiet (the run finished, or a stop/kill landed).
    setStopping(false);
    const queued = queueRef.current;
    const stopped = stoppedRef.current;
    stoppedRef.current = false;
    if (queued.length === 0) return;
    setQueue([]);
    if (stopped) {
      // Interrupted: the operator gets the queued text back to edit, the way
      // Esc in Claude Code returns queued prompts to the input.
      setText((draft) => joinQueued([...queued, draft].filter((s) => s.trim() !== '')));
      return;
    }
    deliver(joinQueued(queued));
    // deliver is recreated each render; only the busy edge matters here.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [busy]);

  const blockedReason = t`not sent: this terminal session takes no messages from here. Turn on swarmery-link's "control" option (new sessions pick it up), or Stop it to take over.`;

  const submit = (): void => {
    const trimmed = text.trim();
    if (trimmed === '' || sending) return;
    switch (mode) {
      case 'blocked':
        // Nothing can deliver it: say so in red rather than swallow the Enter.
        // The draft stays in the box for when the session can take it.
        setError(blockedReason);
        return;
      case 'queue':
        setQueue((q) => [...q, trimmed]);
        setText('');
        return;
      case 'link':
        setSending(true);
        setError(null);
        onSent?.(trimmed, LINK_PROMPT_STALE_MS);
        setText('');
        sendLinkPrompt(sessionId, trimmed, true)
          .catch((e: unknown) => onSendFailed?.(trimmed, e instanceof Error ? e.message : String(e)))
          .finally(() => setSending(false));
        return;
      case 'idle':
        // Optimistic: paint the bubble now, clear the box now — the failure
        // path flips the bubble to retry rather than blocking the composer.
        setText('');
        deliver(trimmed);
    }
  };

  const unqueue = (index: number): void => {
    setQueue((q) => q.filter((_, i) => i !== index));
  };

  const stop = (): void => {
    if (stopping) return;
    setStopping(true);
    setError(null);
    const fail = (e: unknown): void => {
      setError(e instanceof Error ? e.message : String(e));
      stoppedRef.current = false;
      setStopping(false);
    };
    if (mode === 'link') {
      // The process outlives a turn abort, so busy never drops to release the
      // button: release it once the mod has the command.
      stopLinkTurn(sessionId)
        .then(() => setStopping(false))
        .catch(fail);
      return;
    }
    // Queued text returns to the box on the busy → idle edge this causes.
    stoppedRef.current = true;
    // A live terminal process is killed; otherwise it's our own resume run.
    const action = live ? killSession(sessionId) : cancelSessionMessage(sessionId);
    action.catch(fail);
  };

  const onKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>): void => {
    // Enter sends; Shift+Enter falls through to the textarea's own newline.
    if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
      e.preventDefault();
      submit();
    }
  };

  const hint =
    mode === 'blocked'
      ? t`session is running in a terminal without link control — turn on swarmery-link's "control" option, or Stop it to take over and continue the conversation here.`
      : mode === 'queue'
        ? t`a reply is being generated — messages you send now are queued and go out when it finishes.`
        : mode === 'link'
          ? t`session is running in a terminal — a message is delivered when the current turn ends.`
          : null;

  const sendLabel = sending ? t`Sending…` : mode === 'queue' ? t`Queue` : t`Send`;

  return (
    <div className="sticky bottom-0 mt-1.5 bg-surface pt-2 pb-3">
      {error !== null ? (
        <p className="mb-2 font-mono text-[10.5px] leading-relaxed text-red">{error}</p>
      ) : (
        hint !== null && <p className="mb-2 font-mono text-[10.5px] leading-relaxed text-ink-faint">{hint}</p>
      )}
      {queue.length > 0 && (
        <ul aria-label={t`Queued messages`} className="mb-2 flex flex-col gap-1">
          {queue.map((q, i) => (
            <li
              // Queued entries are plain strings that may repeat; position is
              // the identity until they are sent.
              // eslint-disable-next-line react/no-array-index-key
              key={i}
              className="flex items-start gap-2 rounded-[8px] border border-dashed border-line bg-bg px-3 py-1.5 text-[12.5px] text-ink-dim"
            >
              <span className="shrink-0 font-mono text-[10.5px] text-amber">{t`queued`}</span>
              <span className="min-w-0 flex-1 whitespace-pre-wrap break-words">{q}</span>
              <button
                type="button"
                onClick={() => unqueue(i)}
                aria-label={t`Remove queued message`}
                className="shrink-0 rounded px-1 font-mono text-[11px] text-ink-faint hover:text-red focus-visible:outline focus-visible:outline-2 focus-visible:outline-red"
              >
                ×
              </button>
            </li>
          ))}
        </ul>
      )}
      <div className="flex items-end gap-2">
        <textarea
          ref={inputRef}
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={onKeyDown}
          rows={1}
          placeholder={
            mode === 'blocked'
              ? t`draft a reply · it can be sent once the terminal session stops`
              : (placeholder ?? t`Reply to this session…  (Enter to send · Shift+Enter for a new line)`)
          }
          aria-label={t`Message this session`}
          className="max-h-40 min-h-[38px] flex-1 resize-y rounded-[8px] border border-line bg-bg px-3 py-[9px] text-[13px] leading-[normal] text-ink placeholder:text-ink-faint focus-visible:border-line-strong focus-visible:outline-none"
        />
        {busy && (
          <button
            type="button"
            onClick={stop}
            disabled={stopping}
            className="min-h-[38px] shrink-0 rounded-[8px] border border-red px-3.5 font-mono text-[11.5px] font-bold text-red transition-colors hover:bg-red-bg focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-red disabled:cursor-not-allowed disabled:opacity-50"
          >
            {stopping ? t`Stopping…` : t`Stop`}
          </button>
        )}
        {mode !== 'blocked' && (
          <button
            type="button"
            onClick={submit}
            disabled={sending || text.trim() === ''}
            className="min-h-[38px] shrink-0 rounded-[8px] border border-brand bg-brand-bg px-3.5 font-mono text-[11.5px] font-bold text-brand transition-opacity focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand disabled:cursor-not-allowed disabled:opacity-40"
          >
            {sendLabel}
          </button>
        )}
      </div>
    </div>
  );
}
