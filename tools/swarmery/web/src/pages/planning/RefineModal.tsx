// Refine modal (interactive planning v2 — phase 3): free-form
// course-correction instructions — the plan updates and the next questions
// follow the operator's direction. Same overlay pattern as ImproveModal /
// ConfirmDialog.

import { Trans, useLingui } from '@lingui/react/macro';
import { useEffect, useRef, useState } from 'react';

export function RefineModal({
  open,
  busy,
  onClose,
  onApply,
}: {
  open: boolean;
  busy: boolean;
  onClose: () => void;
  onApply: (instructions: string) => void;
}): JSX.Element | null {
  const [text, setText] = useState('');
  if (!open) return null;
  return (
    <RefineModalInner text={text} setText={setText} busy={busy} onClose={onClose} onApply={onApply} />
  );
}

/** Mounted only when open=true so all refs/effects start fresh every open. */
function RefineModalInner({
  text,
  setText,
  busy,
  onClose,
  onApply,
}: {
  text: string;
  setText: (v: string) => void;
  busy: boolean;
  onClose: () => void;
  onApply: (instructions: string) => void;
}): JSX.Element {
  const { t } = useLingui();
  const textareaRef = useRef<HTMLTextAreaElement | null>(null);
  // Remember the element that had focus before this modal opened so we can
  // return focus to it on close (WCAG 2.2 §2.4.3 Focus Order).
  const previouslyFocused = useRef<HTMLElement | null>(null);
  // Tab focus trap selector — see FOCUSABLE below.
  const dialogRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    // Capture trigger on mount; restore on unmount.
    previouslyFocused.current =
      document.activeElement instanceof HTMLElement ? document.activeElement : null;
    // Auto-focus the textarea so the user can type immediately (modal pattern:
    // focus the primary interactive element on open).
    textareaRef.current?.focus();
    return () => {
      previouslyFocused.current?.focus();
    };
  }, []);

  useEffect(() => {
    // Escape closes the modal; Tab/Shift+Tab stays trapped inside the dialog
    // (WCAG 2.2 AA).
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

  const apply = (): void => {
    const trimmed = text.trim();
    if (trimmed === '' || busy) return;
    onApply(trimmed);
    setText('');
  };

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-bg/70 p-4"
      role="dialog"
      aria-modal="true"
      aria-label={t`Refine the plan and next questions`}
      onClick={busy ? undefined : onClose}
    >
      <div
        ref={dialogRef}
        className="w-full max-w-lg rounded-xl border border-line bg-surface px-4 py-4"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="font-display text-[14px] font-bold text-ink">
          <Trans>Refine the plan and next questions</Trans>
        </div>
        <p className="mt-1 text-[12.5px] leading-relaxed text-ink-dim">
          <Trans>
            Describe what should change. The plan will update, and the next questions will follow your direction.
          </Trans>
        </p>

        <label
          className="mt-3 mb-1 block font-mono text-[10.5px] tracking-[0.1em] text-ink-faint uppercase"
          htmlFor="planning-refine-instructions"
        >
          <Trans>refinement instructions</Trans>
        </label>
        <textarea
          ref={textareaRef}
          id="planning-refine-instructions"
          value={text}
          onChange={(e) => setText(e.target.value)}
          rows={4}
          disabled={busy}
          placeholder={t`For example: add a phased rollout, cover disaster recovery, and ask about migration risks.`}
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
            onClick={apply}
            disabled={busy || text.trim() === ''}
            className="rounded-lg border border-green/45 bg-green/12 px-3.5 py-1.5 font-mono text-[11.5px] font-semibold text-green transition-colors hover:bg-green/20 disabled:opacity-50"
          >
            {busy ? '…' : <Trans>Apply refinement</Trans>}
          </button>
        </div>
      </div>
    </div>
  );
}
