// The plan's decision → phase → evidence table (verify-not-trust phase 12, D19),
// inline on the plan's Plan tab: one row per declared decision (D-n) and spec
// criterion (SC-n) — its text, the phases that claim it, and a chip saying
// whether anything proves it was done:
//
//   evidence         a Completion Report cites it, or the plan review found it
//   remaining        nothing proves it yet (or the plan review says missing)
//   not implemented  a report says `not implemented: <id> — <why>`
//   not verified     nothing cites it and the plan review was INCONCLUSIVE —
//                    "nobody could check", not "nobody did it"
//
// Each chip carries a glyph and a word, never colour alone. A row with evidence
// or a reason expands inline to the excerpts behind its chip. The header holds
// the per-chip counts and the latest plan review's state; a `not-verified` plan
// review is said in words with its reason class, never as an empty table. A
// plan with neither decisions nor spec criteria shows an empty state — old plans
// are not backfilled.

import type { MessageDescriptor } from '@lingui/core';
import { msg } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { useState } from 'react';
import type {
  DecisionChip,
  DecisionEvidenceItem,
  DecisionTable as DecisionTableData,
  DecisionTableRow,
  PlanReviewState,
} from '../../api/types';

const CHIP = 'inline-flex items-center gap-1 rounded border px-1.5 py-px font-mono text-[9.5px] whitespace-nowrap';

const CHIP_CLS: Record<DecisionChip, string> = {
  evidence: 'border-green/40 bg-green/10 text-green',
  remaining: 'border-amber/40 bg-amber/10 text-amber',
  'not-implemented': 'border-line bg-surface2 text-ink-dim',
  'not-verified': 'border-red/40 bg-red/10 text-red',
};

/** The glyph beside each chip's word: shape as well as colour. */
const CHIP_GLYPH: Record<DecisionChip, string> = {
  evidence: '✓',
  remaining: '○',
  'not-implemented': '⊘',
  'not-verified': '?',
};

const CHIP_LABEL: Record<DecisionChip, MessageDescriptor> = {
  evidence: msg`evidence`,
  remaining: msg`remaining`,
  'not-implemented': msg`not implemented`,
  'not-verified': msg`not verified`,
};

const PLAN_REVIEW_LABEL: Record<PlanReviewState['status'], MessageDescriptor> = {
  none: msg`no plan review yet`,
  pass: msg`plan review passed`,
  fail: msg`plan review failed`,
  'not-verified': msg`not verified`,
};

const PLAN_REVIEW_CLS: Record<PlanReviewState['status'], string> = {
  none: 'border-line text-ink-dim',
  pass: 'border-green/40 text-green',
  fail: 'border-red/40 text-red',
  'not-verified': 'border-red/40 bg-red/10 text-red',
};

const EVIDENCE_KIND_LABEL: Record<DecisionEvidenceItem['kind'], MessageDescriptor> = {
  report: msg`report`,
  pr: msg`PR`,
  review: msg`review`,
};

const EVIDENCE_STATUS_LABEL: Record<DecisionEvidenceItem['status'], MessageDescriptor> = {
  evidence: msg`evidence`,
  'not-implemented': msg`not implemented`,
  missing: msg`missing`,
};

/** The chip of one row: a glyph and a word, toned by the chip. */
export function ChipBadge({ chip }: { chip: DecisionChip }): JSX.Element {
  const { i18n } = useLingui();
  return (
    <span data-chip={chip} className={`${CHIP} ${CHIP_CLS[chip]}`}>
      <span aria-hidden="true">{CHIP_GLYPH[chip]}</span>
      {i18n._(CHIP_LABEL[chip])}
    </span>
  );
}

function EvidenceLine({ e }: { e: DecisionEvidenceItem }): JSX.Element {
  const { t, i18n } = useLingui();
  const seq = e.phaseSeq;
  const why = e.reason;
  return (
    <li className="flex flex-wrap items-baseline gap-1.5">
      <span className={`${CHIP} border-line text-ink-dim`}>{i18n._(EVIDENCE_KIND_LABEL[e.kind])}</span>
      <span className="font-mono text-[10px] text-ink-faint">{i18n._(EVIDENCE_STATUS_LABEL[e.status])}</span>
      {seq !== null && <span className="font-mono text-[10px] text-ink-2">P{String(seq)}</span>}
      {e.source === 'plan-review' && <span className="font-mono text-[10px] text-ink-faint">{t`daemon plan review`}</span>}
      {e.excerpt !== '' && <span className="min-w-0 flex-1 text-[11px] break-words text-ink-2">{e.excerpt}</span>}
      {e.reason !== '' && (
        <span className="min-w-0 flex-1 text-[11px] break-words text-ink-dim">
          <Trans>why: {why}</Trans>
        </span>
      )}
    </li>
  );
}

function Row({ row }: { row: DecisionTableRow }): JSX.Element {
  const { t } = useLingui();
  const [open, setOpen] = useState(false);
  const expandable = row.evidence.length > 0 || row.reason !== '';
  const id = row.id;
  const rowReason = row.reason;
  const detailsId = `decision-evidence-${id}`;
  return (
    <>
      <tr className="border-t border-line/60 align-top">
        <td className="py-1 pr-2 font-mono text-[10.5px] whitespace-nowrap text-ink-2">{id}</td>
        <td className="py-1 pr-2 text-[11px] break-words text-ink-2">
          {row.text}
          {row.answer !== '' && <span className="text-ink-dim"> → {row.answer}</span>}
        </td>
        <td className="py-1 pr-2 font-mono text-[10.5px] whitespace-nowrap text-ink-dim">
          {row.phases.length > 0 ? row.phases.map((p) => `P${String(p)}`).join(', ') : '—'}
        </td>
        <td className="py-1 pr-2">
          <ChipBadge chip={row.chip} />
        </td>
        <td className="py-1 text-right">
          {expandable && (
            <button
              type="button"
              aria-expanded={open}
              aria-controls={detailsId}
              onClick={() => setOpen((o) => !o)}
              className="rounded border border-line px-1.5 py-px font-mono text-[9.5px] text-ink-dim hover:text-ink focus-visible:outline focus-visible:outline-2 focus-visible:outline-brand"
            >
              {open ? t`hide ${id}` : t`show ${id}`}
            </button>
          )}
        </td>
      </tr>
      {expandable && open && (
        <tr id={detailsId}>
          <td />
          <td colSpan={4} className="pb-1.5">
            {row.reason !== '' && (
              <p className="text-[11px] break-words text-ink-dim">
                <Trans>reason: {rowReason}</Trans>
              </p>
            )}
            {row.evidence.length > 0 && (
              <ul className="mt-0.5 space-y-0.5">
                {row.evidence.map((e, i) => (
                  // Evidence rows have no id of their own; (kind, phase, position) is stable per fetch.
                  <EvidenceLine key={`${e.kind}-${String(e.phaseId)}-${String(i)}`} e={e} />
                ))}
              </ul>
            )}
          </td>
        </tr>
      )}
    </>
  );
}

export interface DecisionTableProps {
  /** The epic payload's `evidence`. */
  table: DecisionTableData;
}

export function DecisionTable({ table }: DecisionTableProps): JSX.Element {
  const { t, i18n } = useLingui();
  const { counts, planReview, rows } = table;
  const notVerified = planReview.status === 'not-verified';
  const reason = planReview.reason !== '' ? planReview.reason : t`no reason recorded`;
  const total = String(rows.length);
  const countChips: { chip: DecisionChip; n: number }[] = [
    { chip: 'evidence', n: counts.evidence },
    { chip: 'remaining', n: counts.remaining },
    { chip: 'not-implemented', n: counts.notImplemented },
    { chip: 'not-verified', n: counts.notVerified },
  ];
  return (
    <section aria-label={t`Decisions and criteria`} className="mb-3 space-y-2 rounded-lg border border-line bg-surface/40 px-2.5 py-2">
      <div className="flex flex-wrap items-center gap-1.5">
        <h3 className="font-mono text-[10px] tracking-[0.14em] text-ink-2 uppercase">
          <Trans>Decisions and criteria</Trans>
        </h3>
        {rows.length > 0 && (
          <ul aria-label={t`counts per chip`} className="flex flex-wrap items-center gap-1">
            {countChips.map(({ chip, n }) => (
              <li key={chip} data-count={chip} className={`${CHIP} ${CHIP_CLS[chip]}`}>
                <span aria-hidden="true">{CHIP_GLYPH[chip]}</span>
                {String(n)} {i18n._(CHIP_LABEL[chip])}
              </li>
            ))}
          </ul>
        )}
        <span
          data-plan-review={planReview.status}
          title={planReview.detail !== '' ? planReview.detail : undefined}
          className={`${CHIP} ${PLAN_REVIEW_CLS[planReview.status]}`}
        >
          {i18n._(PLAN_REVIEW_LABEL[planReview.status])}
        </span>
      </div>
      {notVerified && (
        <div role="status" className="rounded border border-red/40 bg-red/10 px-2 py-1.5">
          <p className="font-mono text-[10.5px] text-red">
            <Trans>Not verified: {reason}</Trans>
          </p>
          <p className="mt-0.5 text-[11px] text-ink-dim">
            <Trans>
              The plan review could not check the code. An id without a cited report reads "not verified" — nobody could
              check it, which is not the same as nobody did it.
            </Trans>
          </p>
        </div>
      )}
      {rows.length === 0 ? (
        <p className="font-mono text-[10.5px] text-ink-faint">
          <Trans>
            This plan declares no decisions and no spec criteria in the machine shape — there is nothing to trace to
            evidence.
          </Trans>
        </p>
      ) : (
        <table className="w-full border-collapse text-left">
          <caption className="sr-only">
            <Trans>Decisions and criteria: {total} ids</Trans>
          </caption>
          <thead>
            <tr className="font-mono text-[9px] tracking-[0.1em] text-ink-faint uppercase">
              <th className="pr-2 font-normal">{t`id`}</th>
              <th className="pr-2 font-normal">{t`text`}</th>
              <th className="pr-2 font-normal">{t`phases`}</th>
              <th className="pr-2 font-normal">{t`status`}</th>
              <th className="font-normal">
                <span className="sr-only">{t`details`}</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <Row key={r.id} row={r} />
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}
