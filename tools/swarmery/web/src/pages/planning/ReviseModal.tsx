// Revise-plan modal (plan-revision phase 4): asks for the one thing a revise
// wizard cannot derive — WHY the plan must change — before the caller POSTs
// /api/epics/{taskId}/revisions. Same overlay + focus-trap pattern as
// RefineModal. On the "revision already open" 409 the caller passes the open
// revision's id back in and the modal offers the review instead of a dead end.

import { Trans, useLingui } from '@lingui/react/macro';
import { useEffect, useRef, useState } from 'react';

export function ReviseModal({
  open,
  planTitle,
  busy,
  error,
  openRevisionId,
  onClose,
  onSubmit,
  onOpenRevision,
}: {
  open: boolean;
  planTitle: string;
  busy: boolean;
  /** The failed start's message (server {error} text), shown inline. */
  error: string | null;
  /** From the 409 body when a staged revision is already open. */
  openRevisionId: number | null;
  onClose: () => void;
  onSubmit: (reason: string) => void;
  /** Jump to the already-open revision's review. */
  onOpenRevision: () => void;
}): JSX.Element | null {
  const [reason, setReason] = useState('');
  if (!open) return null;
  return (
    <ReviseModalInner
      planTitle={planTitle}
      reason={reason}
      setReason={setReason}
      busy={busy}
      error={error}
      openRevisionId={openRevisionId}
      onClose={onClose}
      onSubmit={onSubmit}
      onOpenRevision={onOpenRevision}
    />
  );
}

/** Mounted only when open=true so all refs/effects start fresh every open. */
function ReviseModalInner({
  planTitle,
  reason,
  setReason,
  busy,
  error,
  openRevisionId,
  onClose,
  onSubmit,
  onOpenRevision,
}: {
  planTitle: string;
  reason: string;
  setReason: (v: string) => void;
  busy: boolean;
  error: string | null;
  openRevisionId: number | null;
  onClose: () => void;
  onSubmit: (reason: string) => void;
  onOpenRevision: () => void;
}): JSX.Element {
  const { t } = useLingui();
  const textareaRef = useRef<HTMLTextAreaElement | null>(null);
  // Remember the trigger so focus returns to it on close (WCAG 2.2 §2.4.3).
  const previouslyFocused = useRef<HTMLElement | null>(null);
  const dialogRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    previouslyFocused.current =
      document.activeElement instanceof HTMLElement ? document.activeElement : null;
    textareaRef.current?.focus();
    return () => {
      previouslyFocused.current?.focus();
    };
  }, []);

  useEffect(() => {
    // Escape closes; Tab/Shift+Tab stays trapped inside the dialog (WCAG 2.2 AA).
    const FOCUSABLE =
      'a[href],button:not([disabled]),textarea:not([disabled]),input:not([disabled]),select:not([disabled]),[tabindex]:not([tabindex="-1"])';
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') {
        e.preventDefault();
        if (!busy) onClose();
        return;
      }
      if (e.key !== 'Tab') return;
      const root = dialogRef.current;
      if (root === null) return;
      const focusable = Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
        (el) => el.offsetParent !== null,
      );
      if (focusable.length === 0) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (first === undefined || last === undefined) return;
      const activeInRoot = root.contains(document.activeElement);
      if (e.shiftKey) {
        if (!activeInRoot || document.activeElement === first) {
          e.preventDefault();
          last.focus();
        }
      } else {
        if (!activeInRoot || document.activeElement === last) {
          e.preventDefault();
          first.focus();
        }
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [busy, onClose]);

  const submit = (): void => {
    const trimmed = reason.trim();
    if (trimmed === '' || busy) return;
    onSubmit(trimmed);
  };

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-bg/70 p-4"
      role="dialog"
      aria-modal="true"
      aria-label={t`Revise this plan`}
      onClick={busy ? undefined : onClose}
    >
      <div
        ref={dialogRef}
        className="w-full max-w-lg rounded-xl border border-line bg-surface px-4 py-4"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="font-display text-[14px] font-bold text-ink">
          <Trans>Revise this plan</Trans>
        </div>
        <p className="mt-1 text-[12.5px] leading-relaxed text-ink-dim">
          <Trans>
            A revise wizard interviews you against <span className="text-ink">{planTitle}</span> and
            stages its changes as a diff — nothing is written until you approve it.
          </Trans>
        </p>

        {error !== null && (
          <div
            role="alert"
            className="mt-2.5 rounded-lg border border-red/25 bg-red/5 px-2.5 py-2 font-mono text-[11px] text-red"
          >
            {error}
            {openRevisionId !== null && (
              <button
                type="button"
                onClick={onOpenRevision}
                className="mt-1.5 block font-mono text-[11px] text-brand underline-offset-2 hover:underline"
              >
                <Trans>review the open revision →</Trans>
              </button>
            )}
          </div>
        )}

        <label
          className="mt-3 mb-1 block font-mono text-[10.5px] tracking-[0.1em] text-ink-faint uppercase"
          htmlFor="plan-revise-reason"
        >
          <Trans>what should change, and why</Trans>
        </label>
        <textarea
          ref={textareaRef}
          id="plan-revise-reason"
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          rows={4}
          disabled={busy}
          placeholder={t`For example: phase 3's library choice failed the a11y gate — replace it and add an audit phase.`}
          className="w-full resize-y rounded-lg border border-line bg-field px-2.5 py-2 text-[12.5px] leading-relaxed text-ink transition-colors outline-none placeholder:text-ink-faint focus:border-brand/50 disabled:opacity-50"
        />

        <div className="mt-3.5 flex flex-wrap justify-end gap-2">
          <button
            type="button"
            onClick={onClose}
            disabled={busy}
            className="rounded-lg border border-line bg-surface px-3.5 py-1.5 font-mono text-[11.5px] text-ink-2 transition-colors hover:bg-surface2 disabled:opacity-50"
          >
            <Trans>Cancel</Trans>
          </button>
          <button
            type="button"
            onClick={submit}
            disabled={busy || reason.trim() === ''}
            className="rounded-lg border border-brand/45 bg-brand/12 px-3.5 py-1.5 font-mono text-[11.5px] font-semibold text-brand transition-colors hover:bg-brand/20 disabled:opacity-50"
          >
            {busy ? t`starting…` : t`Start revision`}
          </button>
        </div>
      </div>
    </div>
  );
}
