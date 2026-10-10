// History drawer (interactive planning v2 — phase 3): right-side overlay with
// every interview turn — Q{seq}, the question, the operator's response
// (resolved from option labels / other-text / refine instructions), and a
// collapsible "Show AI Reasoning" block with the planner's pre-JSON analysis
// prose. Same overlay pattern as the Routines editor drawer.

import { Trans, useLingui } from '@lingui/react/macro';
import { useEffect, useRef } from 'react';
import type { PlanningTurn } from '../../api/types';

/** Human form of one stamped answer, resolved against the turn's options. */
function responseText(turn: PlanningTurn): string | null {
  const ans = turn.answer;
  if (ans === null) return null;
  if (ans.kind === 'refine') return ans.instructions ?? '';
  const labels = (ans.selectedOptionIds ?? []).map(
    (id) => turn.question?.options.find((o) => o.id === id)?.label ?? id,
  );
  const parts = [...labels];
  if (ans.otherText !== undefined && ans.otherText !== '') parts.push(ans.otherText);
  return parts.join(' + ');
}

export function HistoryDrawer({
  turns,
  open,
  onClose,
}: {
  turns: PlanningTurn[];
  open: boolean;
  onClose: () => void;
}): JSX.Element | null {
  if (!open) return null;
  return <HistoryDrawerInner turns={turns} onClose={onClose} />;
}

/** Mounted only when open=true so all refs/effects start fresh every open. */
function HistoryDrawerInner({
  turns,
  onClose,
}: {
  turns: PlanningTurn[];
  onClose: () => void;
}): JSX.Element {
  const { t } = useLingui();
  const closeBtnRef = useRef<HTMLButtonElement | null>(null);
  // Remember the element that had focus before this overlay opened so we can
  // return focus to it on close (WCAG 2.2 §2.4.3 Focus Order).
  const previouslyFocused = useRef<HTMLElement | null>(null);

  useEffect(() => {
    // Capture trigger on mount; restore on unmount.
    previouslyFocused.current =
      document.activeElement instanceof HTMLElement ? document.activeElement : null;
    // Move initial focus to the close button (the TaskModal pattern).
    closeBtnRef.current?.focus();
    return () => {
      previouslyFocused.current?.focus();
    };
  }, []);

  useEffect(() => {
    // Escape closes the drawer (as TaskModal does).
    const onKeyDown = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') onClose();
    };
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, [onClose]);

  return (
    <div
      className="fixed inset-0 z-50 flex justify-end bg-bg/60"
      role="dialog"
      aria-modal="true"
      aria-label={t`planning history`}
      onClick={onClose}
    >
      <div
        className="flex h-full w-full max-w-lg flex-col border-l border-line bg-surface"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="border-b border-line px-4 py-3">
          <div className="flex items-center justify-between">
            <span className="font-display text-[14px] font-bold text-ink">
              <Trans>History</Trans>
            </span>
            <button
              ref={closeBtnRef}
              type="button"
              onClick={onClose}
              aria-label={t`close`}
              className="rounded-lg border border-line px-2.5 py-1 font-mono text-[12px] text-ink-dim hover:text-ink"
            >
              ×
            </button>
          </div>
          <p className="mt-0.5 text-[11.5px] text-ink-dim">
            <Trans>Questions, answers, and AI reasoning for each plan update.</Trans>
          </p>
        </div>

        <div className="flex-1 space-y-3 overflow-y-auto px-4 py-4">
          {turns.length === 0 && (
            <div className="font-mono text-[11.5px] text-ink-dim">
              <Trans>no turns yet</Trans>
            </div>
          )}
          {turns.map((turn) => {
            const response = responseText(turn);
            // Guard: only render the response box when there is actual text
            // (empty string or null both mean "no answer recorded yet").
            const hasResponse = response !== null && response !== '';
            return (
              <div key={turn.seq} className="rounded-[10px] border border-line bg-bg px-3 py-2.5">
                <div className="flex items-start gap-2">
                  <span className="mt-[1px] shrink-0 rounded border border-line px-1.5 py-0.5 font-mono text-[10px] text-ink-dim">
                    Q{turn.seq}
                  </span>
                  <div className="min-w-0 text-[12.5px] leading-relaxed whitespace-pre-wrap text-ink">
                    {turn.question?.question ?? t`(question could not be parsed)`}
                  </div>
                </div>

                {hasResponse && (
                  <div className="mt-2 rounded-lg border border-line bg-surface px-2.5 py-2">
                    <div className="mb-0.5 font-mono text-[10px] tracking-[0.08em] text-ink-faint uppercase">
                      {turn.answer?.kind === 'refine' ? <Trans>Your refinement</Trans> : <Trans>Your response</Trans>}
                    </div>
                    <div className="text-[12px] leading-relaxed whitespace-pre-wrap text-ink-2">
                      {response}
                    </div>
                  </div>
                )}

                {turn.reasoning !== '' && (
                  <details className="mt-2">
                    <summary className="cursor-pointer font-mono text-[10.5px] text-ink-faint select-none hover:text-ink-2">
                      <Trans>Show AI Reasoning</Trans>
                    </summary>
                    <pre className="mt-1.5 max-h-64 overflow-y-auto rounded-lg border border-line bg-surface px-2.5 py-2 font-mono text-[10.5px] leading-relaxed whitespace-pre-wrap text-ink-2">
                      {turn.reasoning}
                    </pre>
                  </details>
                )}
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
}
