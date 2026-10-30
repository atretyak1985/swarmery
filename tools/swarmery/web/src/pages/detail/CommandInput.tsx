// Chat composer: talk to this session's conversation from the dashboard.
//
// The button is Send unless the session is BUSY, in which case it is Stop:
//
//   • live terminal process (proc_state running/orphaned) → Stop = SIGTERM the
//     session (POST /api/sessions/{id}/kill). Take it over, then continue here.
//   • our own headless resume in flight (resumeInFlight) → Stop = cancel that
//     run (POST /api/sessions/{id}/message/cancel), killing the child claude.
//
// Otherwise (idle/finished, no run in flight) → Send resumes the conversation
// headlessly (`claude -r <uuid> -p`), whose new turns arrive via the WS bus.
// Gating on the live PROCESS (not the time-based status) means a session that
// merely reads "active" because we just appended to it stays writable.
//
// Send is optimistic: onSent(text) fires SYNCHRONOUSLY on submit so the parent
// paints a pending user bubble immediately; if the POST rejects, onSendFailed
// marks that bubble failed (its retry affordance re-sends). Sending on
// Cmd/Ctrl+Enter; a bare Enter inserts a newline (multi-line prompts).

import { useLingui } from '@lingui/react/macro';
import { useEffect, useState, type RefObject } from 'react';
import type { ProcState } from '../../api/types';
import { cancelSessionMessage, killSession, sendSessionMessage } from '../../api';

function hasLiveProcess(procState: ProcState | null | undefined): boolean {
  return procState === 'running' || procState === 'orphaned';
}

export function CommandInput({
  sessionId,
  procState,
  resumeInFlight = false,
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
  /** Called synchronously on submit with the sent text — the parent echoes it
   * as a pending bubble immediately (before the POST resolves). */
  onSent?: (text: string) => void;
  /** Called with the same text if the POST rejects, plus the server's reason, so
   * the parent can flip that optimistic bubble to a failed/retry state AND show
   * why. A refusal here is usually actionable — a live process to stop, a run
   * whose branch is gone — and a bare "failed" sends the operator guessing. */
  onSendFailed?: ((text: string, reason?: string) => void) | undefined;
}): JSX.Element {
  const { t } = useLingui();
  const [text, setText] = useState('');
  const [sending, setSending] = useState(false);
  const [stopping, setStopping] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const live = hasLiveProcess(procState);
  const busy = live || resumeInFlight;

  // Once the session is no longer busy (kill/cancel reflected via WS), reset.
  useEffect(() => {
    if (!busy) setStopping(false);
  }, [busy]);

  const submit = (): void => {
    const trimmed = text.trim();
    if (trimmed === '' || sending || busy) return;
    setSending(true);
    setError(null);
    // Optimistic: paint the bubble now, clear the box now — the failure path
    // (below) flips the bubble to retry rather than blocking the composer.
    onSent?.(trimmed);
    setText('');
    sendSessionMessage(sessionId, trimmed)
      .catch((e: unknown) => {
        // The pending bubble carries both the failure and its reason; the
        // composer itself stays clean (no duplicate inline error for a send).
        onSendFailed?.(trimmed, e instanceof Error ? e.message : String(e));
      })
      .finally(() => setSending(false));
  };

  const stop = (): void => {
    if (stopping) return;
    setStopping(true);
    setError(null);
    // A live terminal process is killed; otherwise it's our own resume run.
    const action = live ? killSession(sessionId) : cancelSessionMessage(sessionId);
    action.catch((e: unknown) => {
      setError(e instanceof Error ? e.message : String(e));
      setStopping(false);
    });
  };

  const onKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>): void => {
    // Cmd/Ctrl+Enter sends; a bare Enter inserts a newline.
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      submit();
    }
  };

  const hint = live
    ? t`session is running in a terminal — Stop it to take over and continue the conversation here.`
    : resumeInFlight
      ? t`a reply is being generated — Stop to cancel it.`
      : null;

  return (
    <div className="sticky bottom-0 mt-1.5 max-w-[760px] bg-surface pt-2 pb-3">
      {hint !== null ? (
        <p className="mb-2 font-mono text-[10.5px] leading-relaxed text-ink-faint">{hint}</p>
      ) : (
        error !== null && (
          <p className="mb-2 font-mono text-[10.5px] leading-relaxed text-red">{error}</p>
        )
      )}
      <div className="flex items-end gap-2">
        <textarea
          ref={inputRef}
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={onKeyDown}
          disabled={busy || sending}
          rows={1}
          placeholder={busy ? t`session in progress…` : (placeholder ?? t`Reply to this session…  (⌘/Ctrl+Enter to send)`)}
          aria-label={t`Message this session`}
          className="max-h-40 min-h-[38px] flex-1 resize-y rounded-[8px] border border-line bg-bg px-3 py-[9px] text-[13px] leading-[normal] text-ink placeholder:text-ink-faint focus-visible:border-line-strong focus-visible:outline-none disabled:opacity-50"
        />
        {busy ? (
          <button
            type="button"
            onClick={stop}
            disabled={stopping}
            className="min-h-[38px] shrink-0 rounded-[8px] border border-red px-3.5 font-mono text-[11.5px] font-bold text-red transition-colors hover:bg-red-bg focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-red disabled:cursor-not-allowed disabled:opacity-50"
          >
            {stopping ? t`Stopping…` : t`Stop`}
          </button>
        ) : (
          <button
            type="button"
            onClick={submit}
            disabled={sending || text.trim() === ''}
            className="min-h-[38px] shrink-0 rounded-[8px] border border-brand bg-brand-bg px-3.5 font-mono text-[11.5px] font-bold text-brand transition-opacity focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand disabled:cursor-not-allowed disabled:opacity-40"
          >
            {sending ? t`Sending…` : t`Send`}
          </button>
        )}
      </div>
    </div>
  );
}
