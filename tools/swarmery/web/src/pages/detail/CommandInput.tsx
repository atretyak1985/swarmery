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

import { useEffect, useState } from 'react';
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
}: {
  sessionId: number;
  procState: ProcState | null | undefined;
  resumeInFlight?: boolean;
  /** Called with the sent text so the parent can echo it optimistically. */
  onSent?: (text: string) => void;
}): JSX.Element {
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
    sendSessionMessage(sessionId, trimmed)
      .then(() => {
        setText('');
        onSent?.(trimmed);
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
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
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      submit();
    }
  };

  const hint = live
    ? 'session is running in a terminal — Stop it to take over and continue the conversation here.'
    : resumeInFlight
      ? 'a reply is being generated — Stop to cancel it.'
      : null;

  return (
    <div className="shrink-0 border-t border-line bg-bg px-4 pt-2.5 pb-3 desk:px-10 wide:px-0">
      {hint !== null ? (
        <p className="mb-2 font-mono text-[10.5px] leading-relaxed text-ink-faint">{hint}</p>
      ) : (
        error !== null && (
          <p className="mb-2 font-mono text-[10.5px] leading-relaxed text-red">{error}</p>
        )
      )}
      <div className="flex items-end gap-2">
        <textarea
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={onKeyDown}
          disabled={busy || sending}
          rows={1}
          placeholder={busy ? 'session in progress…' : 'Reply to this session…'}
          aria-label="Message this session"
          className="max-h-40 min-h-11 flex-1 resize-y rounded-[11px] border border-line-strong bg-surface2 px-3.5 py-2.5 text-[13.5px] leading-[1.55] text-ink placeholder:text-ink-faint focus-visible:border-brand focus-visible:outline-none disabled:opacity-50"
        />
        {busy ? (
          <button
            type="button"
            onClick={stop}
            disabled={stopping}
            className="min-h-11 shrink-0 rounded-[11px] border border-red/40 bg-red/12 px-4 py-2.5 text-[13px] font-medium text-red transition-colors hover:bg-red/20 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-red disabled:cursor-not-allowed disabled:opacity-50"
          >
            {stopping ? 'Stopping…' : 'Stop'}
          </button>
        ) : (
          <button
            type="button"
            onClick={submit}
            disabled={sending || text.trim() === ''}
            className="min-h-11 shrink-0 rounded-[11px] bg-brand px-4 py-2.5 text-[13px] font-medium text-bg transition-opacity hover:opacity-90 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand disabled:cursor-not-allowed disabled:opacity-40"
          >
            {sending ? 'Sending…' : 'Send'}
          </button>
        )}
      </div>
    </div>
  );
}
