// The Inbox's `review` kind: a plan branch review (phase_reviews scope=plan)
// that read every phase's run branch once the plan was complete, looking at the
// seams between phases. It is advisory — it blocked nothing and re-ran nothing —
// so the one decision is to acknowledge it. Two pieces live here: the list card
// (verdict badge, findings count, plan, started) and the detail body (the
// findings, monospace and scrollable, and the link to the plan). InboxDetail
// owns the Ack button, bound to `e` like every kind's primary.
//
// The verdict badge is shared with the phase panel's Runs tab (plans/RunsReviewBlock).

import { Link } from 'react-router-dom';
import type { Review, ReviewVerdict } from '../../api/reviews';
import { plansTaskHref } from '../plans/plansUrl';
import { ageLabel, findingsLabel, REVIEW_VERDICT_UI } from './inboxModel';

const VERDICT_CLS: Record<ReviewVerdict, string> = {
  pass: 'border-green/40 bg-green/10 text-green',
  fail: 'border-red/40 bg-red/10 text-red',
  // amber: the app's needs-a-human colour — an inconclusive review is not a failing grade.
  inconclusive: 'border-amber/40 bg-amber/10 text-amber',
};

/** "review failed" as a chip; the detail (class, reasons) is its tooltip. */
export function ReviewVerdictBadge({ verdict, detail = '' }: { verdict: ReviewVerdict; detail?: string }): JSX.Element {
  return (
    <span
      className={`shrink-0 rounded border px-1.5 py-px font-mono text-[9.5px] ${VERDICT_CLS[verdict] ?? VERDICT_CLS.inconclusive}`}
      {...(detail !== '' ? { 'data-tip': detail } : {})}
    >
      review {REVIEW_VERDICT_UI[verdict] ?? verdict}
    </span>
  );
}

function planName(r: Review): string {
  return r.planTitle === '' ? `plan #${String(r.taskId)}` : r.planTitle;
}

/** The list card of a plan review. No link here: the row itself is the selectable option. */
export function ReviewRow({ review, selected, now }: { review: Review; selected: boolean; now: number }): JSX.Element {
  return (
    <div className="flex gap-2.5 px-3.5 py-2.5">
      <span className="mt-[5px] h-[7px] w-[7px] shrink-0 rounded-full bg-brand" />
      <div className="min-w-0">
        <div className={`truncate text-[12.5px] ${selected ? 'font-medium text-ink' : 'text-ink-2'}`}>
          Plan review: {planName(review)}
        </div>
        <div className="mt-1 flex min-w-0 items-center gap-1.5 font-mono text-[10.5px] text-ink-faint">
          <ReviewVerdictBadge verdict={review.verdict} />
          <span className="truncate">
            {findingsLabel(review.findings)} · started {ageLabel(review.startedAt, now)} ago
          </span>
        </div>
      </div>
    </div>
  );
}

/** The detail body: what was reviewed, the verdict, the findings, and the way to the plan. */
export function ReviewDetail({ review, now }: { review: Review; now: number }): JSX.Element {
  const findings = review.findings.trim();
  return (
    <>
      <div className="mt-3 flex flex-wrap items-center gap-2 font-mono text-[10.5px] text-ink-faint">
        <ReviewVerdictBadge verdict={review.verdict} detail={review.detail} />
        <span>{findingsLabel(review.findings)}</span>
        <span>· started {ageLabel(review.startedAt, now)} ago</span>
        {review.projectSlug !== '' && (
          <Link
            to={plansTaskHref(review.projectSlug, review.taskId)}
            className="ml-auto text-ink-dim hover:text-brand hover:underline"
          >
            open the plan →
          </Link>
        )}
      </div>
      <p className="mt-3 text-[13px] leading-[1.6] text-ink-3">
        Every phase of this plan finished. A read-only reviewer read all of their run branches together, looking
        at the seams between phases — what one phase hands over and another expects. Nothing was blocked or
        re-run.
      </p>
      {review.verdict === 'inconclusive' && review.detail !== '' && (
        <div className="mt-2 font-mono text-[11px] text-amber">{review.detail}</div>
      )}
      <div className="mt-3 font-mono text-[10px] tracking-[0.1em] text-ink-faint uppercase">findings</div>
      <pre
        aria-label="review findings"
        className="mt-1.5 max-h-[320px] overflow-auto rounded-[8px] border border-line bg-bg px-3 py-2.5 font-mono text-[11.5px] whitespace-pre-wrap text-ink-2"
      >
        {findings === '' ? 'The reviewer left no findings text.' : findings}
      </pre>
    </>
  );
}
