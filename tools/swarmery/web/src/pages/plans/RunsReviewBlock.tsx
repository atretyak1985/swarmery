// The phase panel's Runs tab "Review" block: the latest code review of this
// phase's run (phase_reviews scope=phase, written by the review stage of a
// `**Review:** on` phase before the verifier grades it). Verdict, the reviewer's
// detail, which fix round it was, and the findings collapsed. A phase that was
// never reviewed renders nothing — review is opt-in per phase doc.
//
// The reviews come with the epic (`phase.reviews`, newest first): the review row
// is written minutes after the run's own state settles, and the daemon refetches
// the epic when it lands, so the block follows it without polling. The epic
// payload leaves the findings out for size; they are fetched for the latest
// review only, again whenever a newer review arrives.

import { useEffect, useState } from 'react';
import { countFindings, fetchPhaseReviews } from '../../api/reviews';
import type { PhaseReviewSummary } from '../../api/types';
import { fmtAgo } from '../../lib/format';
import { ReviewVerdictBadge } from '../inbox/ReviewItem';

/** "first review" / "review of the fix re-run". */
function roundLabel(round: number): string {
  if (round === 0) return 'first review';
  if (round === 1) return 'review of the fix re-run';
  return `fix round ${String(round)}`;
}

/** The findings of review `reviewId`: null until loaded (or when there is no review). */
function useReviewFindings(
  taskId: number,
  phaseId: number,
  reviewId: number | null,
): { findings: string | null; error: string | null } {
  const [state, setState] = useState<{ id: number | null; findings: string | null; error: string | null }>({
    id: null,
    findings: null,
    error: null,
  });

  useEffect(() => {
    if (reviewId === null) return;
    let live = true;
    fetchPhaseReviews(taskId, phaseId)
      .then((rows) => {
        if (!live) return;
        setState({ id: reviewId, findings: rows.find((r) => r.id === reviewId)?.findings ?? '', error: null });
      })
      .catch((e: unknown) => {
        if (live) setState({ id: reviewId, findings: null, error: e instanceof Error ? e.message : String(e) });
      });
    return () => {
      live = false;
    };
  }, [taskId, phaseId, reviewId]);

  // A result for an older review is not this review's.
  if (state.id !== reviewId) return { findings: null, error: null };
  return { findings: state.findings, error: state.error };
}

export function RunsReviewBlock({
  taskId,
  phaseId,
  reviews,
}: {
  taskId: number;
  phaseId: number;
  /** The phase's reviews from the epic payload, newest first. */
  reviews: PhaseReviewSummary[];
}): JSX.Element | null {
  const latest = reviews[0] ?? null;
  const { findings: loaded, error } = useReviewFindings(taskId, phaseId, latest?.id ?? null);
  if (latest === null) return null;

  const findings = (loaded ?? '').trim();
  const n = countFindings(findings);
  return (
    <section aria-label="code review" className="mt-3 rounded-md border border-line px-2.5 py-2">
      <div className="flex flex-wrap items-center gap-1.5 font-mono text-[10px] text-ink-faint">
        <span className="tracking-[0.1em] uppercase">review</span>
        <ReviewVerdictBadge verdict={latest.verdict} detail={latest.detail} />
        <span>{roundLabel(latest.fixRound)}</span>
        <span>· {fmtAgo(latest.startedAt)}</span>
      </div>
      {latest.detail !== '' && (
        <div className="mt-1.5 font-mono text-[10.5px] break-words text-ink-dim">{latest.detail}</div>
      )}
      {error !== null && (
        <div className="mt-1.5 font-mono text-[10.5px] text-ink-faint">couldn't load the findings: {error}</div>
      )}
      {findings !== '' && (
        <details className="mt-1.5">
          <summary className="cursor-pointer font-mono text-[10.5px] text-ink-dim hover:text-ink">
            {n === 0 ? 'findings (none blocking)' : `findings (${String(n)})`}
          </summary>
          <pre className="mt-1.5 max-h-[280px] overflow-auto rounded border border-line bg-bg px-2.5 py-2 font-mono text-[11px] whitespace-pre-wrap text-ink-2">
            {findings}
          </pre>
        </details>
      )}
    </section>
  );
}
