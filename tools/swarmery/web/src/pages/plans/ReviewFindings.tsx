// A phase's review findings on its Review tab, inline (migration 0122): every
// `kind: review` round of the phase doc as the daemon projected it — written by
// the daemon's review stage, run-plan's orchestrator, or backfilled by hand —
// grouped by round, oldest first, each finding with its type, location,
// criterion, whether a test caught it, its evidence and whether a later round
// resolved it. A phase finished before findings existed shows the «legacy
// review» badge instead (D21): information, never a blocker.
//
// D20: when the latest round is incomplete (`complete: false` — a reviewer that
// timed out, wrote no readable block, stopped early, or whose block disagrees
// with its verdict), the tab says so above the rounds: the phase is held, it is
// not a passed review, and only the operator can lift it — repeat the review or
// accept it as is. The daemon runs at most two rounds on its own (D42).

import { t } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import type { ReviewBlock, ReviewFinding } from '../../api/types';
import { fmtAgo } from '../../lib/format';

export interface ReviewFindingsProps {
  /** The doc's rounds, oldest first (`phase.reviewBlocks ?? []`). */
  blocks: readonly ReviewBlock[];
  /** D21: Done before review findings existed, and no block. */
  legacy: boolean;
  /** D20: why the latest round is incomplete (`phase.reviewHold`), "" or
   *  absent when it is complete. */
  hold?: string;
}

/** The daemon's bound on automatic review rounds per cycle (D42). */
export const MAX_AUTO_REVIEW_ROUNDS = 2;

/** The rounds count line: "2 rounds" / "1 round" (plan round excluded). */
export function roundsText(blocks: readonly ReviewBlock[]): string {
  const n = blocks.filter((b) => b.round !== 'plan').length;
  return n === 1 ? t`1 review round` : t`${String(n)} review rounds`;
}

function HoldBanner({ hold }: { hold: string }): JSX.Element {
  const { t } = useLingui();
  const max = String(MAX_AUTO_REVIEW_ROUNDS);
  return (
    <div role="status" aria-label={t`Review incomplete`} className="rounded-lg border border-amber/40 bg-amber/10 px-2.5 py-2">
      <div className="font-mono text-[10px] tracking-[0.14em] text-amber uppercase">
        <Trans>Review incomplete — not a passed review</Trans>
      </div>
      <p className="mt-1 font-mono text-[10.5px] break-words text-ink-2">{hold}</p>
      <p className="mt-1 text-[11px] text-ink-dim">
        <Trans>
          The phase is held until the operator decides: repeat the review, or accept it as is. The daemon runs at most{' '}
          {max} review rounds on its own.
        </Trans>
      </p>
    </div>
  );
}

const VERDICT_CLS: Record<ReviewBlock['verdict'], string> = {
  PASS: 'border-green/40 text-green',
  FAIL: 'border-red/40 text-red',
  INCONCLUSIVE: 'border-amber/40 text-amber',
};

const CHIP = 'rounded border px-1.5 py-px font-mono text-[9px]';

/** "daemon review" / "interactive review" / "backfilled" — the source chip. */
export function sourceText(source: ReviewBlock['source']): string {
  switch (source) {
    case 'daemon':
      return t`daemon review`;
    case 'backfill':
      return t`backfilled`;
    default:
      return t`interactive review`;
  }
}

/** "Round 2" / "Plan round". */
export function roundText(round: string): string {
  return round === 'plan' ? t`Plan round` : t`Round ${round}`;
}

/** The finding's location: `file:line`, the bare file, or "—". */
function location(f: ReviewFinding): string {
  if (f.file === '') return '—';
  return f.line !== null ? `${f.file}:${String(f.line)}` : f.file;
}

function FindingRow({ f }: { f: ReviewFinding }): JSX.Element {
  const { t } = useLingui();
  const resolvedBy = f.resolvedBy;
  return (
    <tr className="border-t border-line/60 align-top">
      <td className="py-1 pr-2 font-mono text-[10.5px] text-ink-2">{f.id}</td>
      <td className="py-1 pr-2 font-mono text-[10.5px] text-ink-dim">{f.type === '' ? '—' : f.type}</td>
      <td className="py-1 pr-2 font-mono text-[10.5px] break-all text-ink-2">{location(f)}</td>
      <td className="py-1 pr-2 text-[11px] text-ink-2">{f.criterion === '' ? '—' : f.criterion}</td>
      <td className="py-1 pr-2 font-mono text-[10.5px]">
        {f.testCaught ? <span className="text-green">{t`yes`}</span> : <span className="text-ink-faint">{t`no`}</span>}
      </td>
      <td className="py-1 pr-2 text-[11px] break-words text-ink-2">
        {f.evidence === '' ? '—' : f.evidence}
        {f.note !== '' && <div className="mt-0.5 text-[10.5px] whitespace-pre-wrap text-ink-faint">{f.note}</div>}
      </td>
      <td className="py-1 font-mono text-[10.5px]">
        {resolvedBy !== null ? (
          <span className="text-green">{resolvedBy === 'plan' ? t`resolved in the plan round` : t`resolved in round ${resolvedBy}`}</span>
        ) : (
          <span className="text-amber">{t`open`}</span>
        )}
      </td>
    </tr>
  );
}

function Round({ block }: { block: ReviewBlock }): JSX.Element {
  const { t } = useLingui();
  const title = roundText(block.round);
  const count = String(block.findings.length);
  return (
    <section aria-label={title} className="rounded-lg border border-line bg-surface/40 px-2.5 py-2">
      <div className="flex flex-wrap items-center gap-1.5">
        <h4 className="font-mono text-[10px] tracking-[0.14em] text-ink-2 uppercase">{title}</h4>
        <span className={`${CHIP} ${VERDICT_CLS[block.verdict]}`}>{block.verdict}</span>
        <span className={`${CHIP} border-line text-ink-dim`}>{sourceText(block.source)}</span>
        {!block.complete && <span className={`${CHIP} border-amber/40 text-amber`}>{t`incomplete`}</span>}
        {block.unparsable && (
          <span
            className={`${CHIP} border-amber/40 text-amber`}
            title={t`the reviewer wrote no readable kind: review block — its own words are in the doc`}
          >
            {t`unparsable`}
          </span>
        )}
        {block.writtenAt !== '' && (
          <span className="font-mono text-[10px] text-ink-faint" title={block.writtenAt}>
            {fmtAgo(block.writtenAt)}
          </span>
        )}
      </div>
      {block.scopeLeft.length > 0 && (
        <div className="mt-1 font-mono text-[10.5px] text-amber">
          <Trans>not reviewed: {block.scopeLeft.join(', ')}</Trans>
        </div>
      )}
      {block.findings.length === 0 ? (
        <p className="mt-1 font-mono text-[10.5px] text-ink-faint">
          <Trans>No findings.</Trans>
        </p>
      ) : (
        <table className="mt-1.5 w-full border-collapse text-left">
          <caption className="sr-only">
            <Trans>
              {title}: {count} findings
            </Trans>
          </caption>
          <thead>
            <tr className="font-mono text-[9px] tracking-[0.1em] text-ink-faint uppercase">
              <th className="pr-2 font-normal">{t`id`}</th>
              <th className="pr-2 font-normal">{t`type`}</th>
              <th className="pr-2 font-normal">{t`where`}</th>
              <th className="pr-2 font-normal">{t`criterion`}</th>
              <th className="pr-2 font-normal">{t`test caught`}</th>
              <th className="pr-2 font-normal">{t`evidence`}</th>
              <th className="font-normal">{t`status`}</th>
            </tr>
          </thead>
          <tbody>
            {block.findings.map((f) => (
              <FindingRow key={f.id} f={f} />
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}

export function ReviewFindings({ blocks, legacy, hold = '' }: ReviewFindingsProps): JSX.Element | null {
  const { t } = useLingui();
  const held = hold.trim() !== '';
  if (blocks.length === 0 && !legacy && !held) return null;
  return (
    <section aria-label={t`Review findings`} className="space-y-2">
      <div className="flex flex-wrap items-center gap-1.5">
        <h3 className="font-mono text-[10px] tracking-[0.14em] text-ink-2 uppercase">
          <Trans>Review findings</Trans>
        </h3>
        {legacy && (
          <span
            className={`${CHIP} border-line text-ink-dim`}
            title={t`finished before review findings were recorded — nothing is missing and nothing is required`}
          >
            {t`legacy review`}
          </span>
        )}
        {blocks.length > 0 && <span className="font-mono text-[10px] text-ink-faint">{roundsText(blocks)}</span>}
        {held && <span className={`${CHIP} border-amber/40 text-amber`}>{t`review incomplete`}</span>}
      </div>
      {held && <HoldBanner hold={hold} />}
      {blocks.map((b) => (
        <Round key={b.round} block={b} />
      ))}
    </section>
  );
}
