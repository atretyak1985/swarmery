// The Review tab's state block (plan D20, phase 5 step 4): one row per fact —
// Branch/Landing, Pull request, CI, Review, Criteria, Guard — replacing the
// old mono "перевірки:" strip of badges. Every row reads by its glyph and word
// first, color second (SC on readability without color): a plain `·` for
// neutral, `✓` for good, `✗` for bad, `!` for waiting on a person.
//
// Data is read, never recomputed: `stateChip` / `ciChip` / `reviewChip` and
// `landingLabel` / `prLinkText` (landingModel.ts) are the same functions the
// old inline strip used, and `guardFindings` (api/guard.ts) is the same the
// GuardBadge pill used. This block only lays them out as rows instead of a
// line of chips, and gives the block's header the "checked N ago" + Refresh
// LandingStatus used to render inline (phase 5 step 4's last sentence).
//
// Below the `desk` breakpoint each row stacks into two lines — name + status,
// the fact underneath — with the link staying at the right (phase 5 step 7).

import { plural } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import type { NeedsYouDecision, PhaseLanding, ProviderTerms } from '../../api/types';
import { guardFindings } from '../../api/guard';
import { safeHref } from '../../lib/safeHref';
import { fmtAgo } from '../../lib/format';
import { LandingStatus, ciChip, reviewChip, stateChip, type Chip } from './LandingStatus';
import { landingLabel, prLinkText } from './landingModel';

const ROW_GRID = 'desk:grid desk:grid-cols-[18px_104px_minmax(0,1fr)_auto] desk:items-center desk:gap-2';
const DOT = { success: 'text-green', danger: 'text-red', warn: 'text-amber', neutral: 'text-ink-faint' } as const;
type RowTone = keyof typeof DOT;
const GLYPH: Record<RowTone, string> = { success: '✓', danger: '✗', warn: '!', neutral: '·' };

function Row({
  label,
  tone,
  status,
  detail,
  link,
  testId,
}: {
  label: string;
  tone: RowTone;
  status: string;
  detail?: string;
  link?: { href: string; text: string } | null;
  testId: string;
}): JSX.Element {
  return (
    <div data-testid={testId} className={`flex flex-col gap-0.5 border-b border-line py-1.5 last:border-b-0 ${ROW_GRID}`}>
      <span aria-hidden="true" className={`hidden font-bold desk:inline-block ${DOT[tone]}`}>
        {GLYPH[tone]}
      </span>
      <span className="font-mono text-[10px] tracking-[0.08em] text-ink-faint uppercase">{label}</span>
      <span className="min-w-0 text-[12.5px] leading-[1.4] text-ink-2">
        <span className={`mr-1.5 font-mono text-[11px] font-bold desk:hidden ${DOT[tone]}`} aria-hidden="true">
          {GLYPH[tone]}
        </span>
        {status}
        {detail !== undefined && detail !== '' && <span className="block text-[11px] text-ink-faint desk:inline desk:ml-1.5">{detail}</span>}
      </span>
      {link != null ? (
        <a href={safeHref(link.href) ?? undefined} target="_blank" rel="noreferrer" className="justify-self-end font-mono text-[10.5px] text-brand hover:underline">
          {link.text}
        </a>
      ) : (
        <span />
      )}
    </div>
  );
}

const chipTone = (c: Chip): RowTone => (c.tone.includes('green') ? 'success' : c.tone.includes('red') ? 'danger' : c.tone.includes('amber') ? 'warn' : 'neutral');

export interface ReviewStateLedgerProps {
  taskId: number;
  phaseId: number;
  runBranch: string;
  landing: PhaseLanding | null;
  terms: ProviderTerms;
  checkboxesDone: number;
  checkboxesTotal: number;
  manualOpen: number;
  guardDecision?: NeedsYouDecision | null;
  guard: Parameters<typeof guardFindings>[0];
  /** D26: an open empty guard decision closes with one button, right here. */
  onCloseEmptyGuard?: (() => void) | undefined;
  closingGuard?: boolean;
  onRefreshed: (next: PhaseLanding) => void;
}

export function ReviewStateLedger({
  taskId,
  phaseId,
  runBranch,
  landing,
  terms,
  checkboxesDone,
  checkboxesTotal,
  manualOpen,
  guardDecision = null,
  guard,
  onCloseEmptyGuard,
  closingGuard = false,
  onRefreshed,
}: ReviewStateLedgerProps): JSX.Element {
  const { t } = useLingui();
  const state = landing?.state ?? 'none';
  const status = landing?.prStatus ?? null;
  const stChip = status !== null ? stateChip(status) : null;
  const ci = status !== null ? ciChip(status.ci) : null;
  const review = status !== null ? reviewChip(status.review) : null;
  const link = landing !== null ? prLinkText(landing, terms) : null;

  const branchLabel = runBranch === '' ? t`Branch` : t`Landing`;
  const branchTone: RowTone = state === 'merged' ? 'success' : state === 'returned' ? 'warn' : 'neutral';
  const branchStatus = runBranch === '' ? t`no run branch yet` : landingLabel(state, terms);
  const mergedAgo = landing?.state === 'merged' && landing.landedAt !== null ? fmtAgo(landing.landedAt) : '';

  const f = guardFindings(guard);
  const guardClean = f === null;
  const guardTone: RowTone = guardClean ? 'success' : 'danger';
  const guardParts: string[] = [];
  if (f !== null && f.changed.length > 0) {
    guardParts.push(
      plural(f.changed.length, {
        one: '# protected file changed',
        few: '# protected files changed',
        many: '# protected files changed',
        other: '# protected files changed',
      }),
    );
  }
  if ((f?.blocked ?? 0) > 0) {
    guardParts.push(
      plural(f?.blocked ?? 0, { one: '# blocked attempt', few: '# blocked attempts', many: '# blocked attempts', other: '# blocked attempts' }),
    );
  }
  if (f?.docCopyMismatch === true) guardParts.push(t`doc copy mismatch`);
  if (f?.unreadable === true) guardParts.push(t`guard block unreadable`);
  const guardStatus = guardClean ? t`clean` : guardParts.join(' · ');

  const open = checkboxesTotal - checkboxesDone;
  const criteriaTone: RowTone = open === 0 ? 'success' : manualOpen > 0 ? 'warn' : 'neutral';
  const criteriaStatus = `${String(checkboxesDone)}/${String(checkboxesTotal)}`;
  const criteriaDetail = manualOpen > 0 ? plural(manualOpen, { one: '# manual check open', few: '# manual checks open', many: '# manual checks open', other: '# manual checks open' }) : '';

  return (
    <section aria-label={t`Review state`} className="rounded-lg border border-line bg-surface/40 px-2.5 py-2">
      <div className="mb-1 flex flex-wrap items-center justify-between gap-1.5">
        <h3 className="font-mono text-[10px] tracking-[0.14em] text-ink-2 uppercase">
          <Trans>State</Trans>
        </h3>
        {landing !== null && (state === 'pr_open' || state === 'merged') && (
          <LandingStatus taskId={taskId} phaseId={phaseId} landing={landing} onRefreshed={onRefreshed} />
        )}
      </div>
      <Row
        label={branchLabel}
        tone={branchTone}
        status={branchStatus}
        detail={mergedAgo !== '' ? t`merged ${mergedAgo}` : runBranch}
        testId="review-ledger-branch"
      />
      <Row
        label={t`Pull request`}
        tone={stChip !== null ? chipTone(stChip) : 'neutral'}
        status={link ?? t`not opened yet`}
        {...(link !== null && landing?.prUrl !== null && landing?.prUrl !== undefined ? { link: { href: landing.prUrl, text: t`open ↗` } } : {})}
        testId="review-ledger-pr"
      />
      <Row label={t`CI`} tone={ci !== null ? chipTone(ci) : 'neutral'} status={ci !== null ? `${ci.glyph ?? ''} ${ci.text}`.trim() : t`—`} testId="review-ledger-ci" />
      <Row
        label={t`Review`}
        tone={review !== null ? chipTone(review) : 'neutral'}
        status={review !== null ? `${review.glyph ?? ''} ${review.text}`.trim() : t`—`}
        testId="review-ledger-review"
      />
      <Row label={t`Criteria`} tone={criteriaTone} status={criteriaStatus} detail={criteriaDetail} testId="review-ledger-criteria" />
      <Row label={t`Guard`} tone={guardTone} status={guardStatus} testId="review-ledger-guard" />
      {guardDecision != null && onCloseEmptyGuard !== undefined && (
        <div className="mt-1.5 flex justify-end">
          <button
            type="button"
            onClick={onCloseEmptyGuard}
            disabled={closingGuard}
            className="rounded-lg border border-line bg-surface px-3 py-1.5 font-mono text-[11px] text-ink-2 transition-colors hover:bg-surface2 disabled:opacity-50"
          >
            {closingGuard ? t`Closing…` : t`Close · allow nothing`}
          </button>
        </div>
      )}
    </section>
  );
}
