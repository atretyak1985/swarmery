// The phase panel's Runs tab "Review" block: the latest code review of this
// phase's run (phase_reviews scope=phase, written by the review stage of a
// `**Review:** on` phase before the verifier grades it). Verdict, the reviewer's
// detail, which fix round it was, and the findings collapsed. A phase that was
// never reviewed renders nothing — review is opt-in per phase doc.
//
// The list is fetched per phase and refetched when the run moves on (`version`):
// a review is recorded at the end of a run, and a FAIL re-runs the phase once.

import { useEffect, useState } from 'react';
import { countFindings, fetchPhaseReviews, type Review } from '../../api/reviews';
import { fmtAgo } from '../../lib/format';
import { ReviewVerdictBadge } from '../inbox/ReviewItem';

/** "first review" / "review of the fix re-run". */
function roundLabel(round: number): string {
  if (round === 0) return 'first review';
  if (round === 1) return 'review of the fix re-run';
  return `fix round ${String(round)}`;
}

export function RunsReviewBlock({
  taskId,
  phaseId,
  version,
}: {
  taskId: number;
  phaseId: number;
  /** Anything that changes when the phase's run does (state, session, doc time). */
  version: string;
}): JSX.Element | null {
  const [latest, setLatest] = useState<Review | null>(null);
  const [error, setError] = useState<string | null>(null);

  // `version` is read by nobody inside: it is the refetch trigger.
  useEffect(() => {
    let live = true;
    fetchPhaseReviews(taskId, phaseId)
      .then((rows) => {
        if (!live) return;
        setLatest(rows[0] ?? null);
        setError(null);
      })
      .catch((e: unknown) => {
        if (live) setError(e instanceof Error ? e.message : String(e));
      });
    return () => {
      live = false;
    };
  }, [taskId, phaseId, version]);

  if (error !== null) {
    return <div className="mt-3 font-mono text-[10.5px] text-ink-faint">couldn't load the review: {error}</div>;
  }
  if (latest === null) return null;

  const n = countFindings(latest.findings);
  const findings = latest.findings.trim();
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
