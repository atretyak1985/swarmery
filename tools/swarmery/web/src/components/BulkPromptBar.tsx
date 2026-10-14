// "Continue phase" bulk action on the Sessions page (mods-swarmery-link phase
// 4, SC-6): one prompt, sent through each selected session's link mod as the
// mod's message (POST /api/sessions/link/bulk-prompt). The server answers with
// what it queued and what it skipped and why; both are listed so a mixed
// selection never fails silently.
//
// The server takes at most BULK_PROMPT_MAX ids per call: a larger selection is
// refused here, in the operator's language, rather than by the server's 400.
// The page passes only the selected rows that are currently visible; the ones
// a filter hides are counted in `hiddenCount` and never sent.

import { useLingui } from '@lingui/react/macro';
import { useState } from 'react';
import { bulkLinkPrompt } from '../api';
import type { LinkBulkPromptResult, Session } from '../api/types';

/** The server's per-call id limit (internal/api/link_commands.go maxBulkSessions). */
export const BULK_PROMPT_MAX = 50;

/** A row label for a session id: its title, else the short uuid, else #id. */
function sessionLabel(byId: ReadonlyMap<number, Session>, id: number): string {
  const s = byId.get(id);
  if (s === undefined) return `#${String(id)}`;
  return s.title ?? s.sessionUuid.slice(0, 8);
}

export function BulkPromptBar({
  selected,
  hiddenCount = 0,
  selectableCount,
  byId,
  onSelectAll,
  onClear,
}: {
  /** Selected session ids that are visible now, in selection order: exactly
   * what Send sends. */
  selected: readonly number[];
  /** Selected sessions a filter hides now; named, never sent. */
  hiddenCount?: number;
  /** How many loaded sessions could be selected (link.controllable). */
  selectableCount: number;
  /** Loaded sessions by id, to name the rows of the result. */
  byId: ReadonlyMap<number, Session>;
  onSelectAll: () => void;
  onClear: () => void;
}): JSX.Element {
  const { t } = useLingui();
  const [text, setText] = useState(
    () => t`Continue with the next phase of the plan per its phase doc; do not ask for confirmation if the criteria are clear`,
  );
  const [sending, setSending] = useState(false);
  const [result, setResult] = useState<LinkBulkPromptResult | null>(null);
  const [error, setError] = useState<string | null>(null);

  const n = selected.length;
  const max = BULK_PROMPT_MAX;
  const overCap = n > max;
  const queuedCount = result?.queued.length ?? 0;
  const skippedCount = result?.skipped.length ?? 0;
  const send = async (): Promise<void> => {
    setSending(true);
    setError(null);
    setResult(null);
    try {
      setResult(await bulkLinkPrompt([...selected], text));
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setSending(false);
    }
  };

  return (
    <section
      aria-label={t`Continue phase on selected sessions`}
      data-testid="bulk-prompt"
      className="mt-4 flex flex-col gap-2 rounded-lg border border-line bg-surface px-3 py-2.5"
    >
      <div className="flex flex-wrap items-center gap-2 font-mono text-[11px] text-ink-dim">
        <span>{t`${n} selected of ${selectableCount} with a polling link mod`}</span>
        <button type="button" onClick={onSelectAll} className="text-ink-dim underline hover:text-ink">
          {t`select all`}
        </button>
        <button type="button" onClick={onClear} className="text-ink-dim underline hover:text-ink">
          {t`clear`}
        </button>
      </div>
      {selectableCount > max && (
        <div className="font-mono text-[10.5px] text-ink-faint" data-testid="bulk-cap-note">
          {t`One send reaches at most ${max} sessions; select all takes the first ${max}.`}
        </div>
      )}
      {hiddenCount > 0 && (
        <div className="font-mono text-[10.5px] text-amber" data-testid="bulk-hidden-note">
          {t`${hiddenCount} selected sessions are hidden by the filter and will not be sent`}
        </div>
      )}
      <textarea
        value={text}
        onChange={(e) => setText(e.target.value)}
        rows={2}
        aria-label={t`Prompt for the selected sessions`}
        className="w-full resize-y rounded border border-line bg-surface2 px-2 py-1 font-mono text-[11.5px] text-ink"
      />
      <div className="flex flex-wrap items-center gap-2">
        <button
          type="button"
          disabled={n === 0 || overCap || text.trim() === '' || sending}
          onClick={() => void send()}
          className="rounded border border-brand/50 bg-brand/10 px-2.5 py-1 font-mono text-[11px] font-medium text-brand transition-colors hover:bg-brand/20 disabled:cursor-not-allowed disabled:opacity-50"
        >
          {sending
            ? t`sending…`
            : overCap
              ? t`Select at most ${max} sessions (${n} selected)`
              : t`Continue phase on ${n} sessions`}
        </button>
        <span className="font-mono text-[10.5px] text-ink-faint">
          {t`Sent as a message from the swarmery link mod, not as you.`}
        </span>
      </div>
      {error !== null && (
        <div role="alert" className="font-mono text-[11px] text-red">
          {error}
        </div>
      )}
      {result !== null && (
        <div role="status" data-testid="bulk-result" className="flex flex-col gap-1 font-mono text-[11px]">
          <span className="text-green">{t`queued: ${queuedCount}`}</span>
          {result.queued.map((q) => (
            <span key={`q-${String(q.id)}`} className="text-ink-dim" data-testid="bulk-queued">
              {sessionLabel(byId, q.id)}
            </span>
          ))}
          {result.skipped.length > 0 && (
            <span className="text-amber">{t`skipped: ${skippedCount}`}</span>
          )}
          {result.skipped.map((s) => {
            const label = sessionLabel(byId, s.id);
            // The server's two reasons are known; anything newer shows as sent.
            const why =
              // i18n-ignore: the server's skip reason (protocol), not UI copy
              s.reason === 'session has no link'
                ? t`no polling link mod`
                : // i18n-ignore: the server's skip reason (protocol), not UI copy
                  s.reason === 'not found'
                  ? t`session not found`
                  : s.reason;
            return (
              <span key={`s-${String(s.id)}`} className="text-ink-dim" data-testid="bulk-skipped">
                {t`${label} — ${why}`}
              </span>
            );
          })}
        </div>
      )}
    </section>
  );
}
