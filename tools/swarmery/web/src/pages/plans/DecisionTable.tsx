// The plan's decision → phase → evidence table (verify-not-trust phase 12, D19),
// inline at the top of the plan tab. Every decision (`D-n`) and spec criterion
// (`SC-n`) the plan declares, the phases that claim it, what proves it, and one
// of four chips: evidence, remaining, not implemented (a report said so, with
// its reason) or not verified (the plan review was INCONCLUSIVE, so "remaining"
// would claim a check that never ran). A plan without `## Decisions` and without
// a spec has no rows, and the table renders nothing — old plans are not rewritten.

import type { MessageDescriptor } from '@lingui/core';
import { msg } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { useEffect, useState } from 'react';
import { type DecisionRow, type DecisionRowStatus, type DecisionTable as Table, fetchDecisionTable } from '../../api/evidence';

/** Chip copy + styling per row status; amber is the app's needs-a-human colour. */
const STATUS_CHIP: Record<DecisionRowStatus, { cls: string; label: MessageDescriptor }> = {
  evidence: { cls: 'border-green/40 bg-green/10 text-green', label: msg`evidence` },
  remaining: { cls: 'border-red/40 bg-red/10 text-red', label: msg`remaining` },
  'not-implemented': { cls: 'border-line text-ink-faint', label: msg`not implemented` },
  'not-verified': { cls: 'border-amber/40 bg-amber/10 text-amber', label: msg`not verified` },
};

const STATUS_ORDER: DecisionRowStatus[] = ['evidence', 'remaining', 'not-implemented', 'not-verified'];

export function DecisionStatusChip({ status, reason }: { status: DecisionRowStatus; reason?: string }): JSX.Element {
  const { i18n } = useLingui();
  const { cls, label } = STATUS_CHIP[status];
  return (
    <span
      className={`whitespace-nowrap rounded border px-1.5 py-px font-mono text-[9.5px] ${cls}`}
      data-status={status}
      data-tip={reason !== undefined && reason !== '' ? reason : undefined}
    >
      {i18n._(label)}
    </span>
  );
}

/** The row's proof, one line: the first held evidence, else the first excerpt. */
function evidenceLine(r: DecisionRow): string {
  const held = r.evidence.find((e) => e.status === 'evidence') ?? r.evidence[0];
  if (held === undefined) return '';
  const where = held.phaseSeq > 0 ? `P${String(held.phaseSeq)} · ` : '';
  return `${where}${held.kind}: ${held.excerpt}`;
}

export interface DecisionTableProps {
  taskId: number;
  /** Any value that changes when the plan does — refetches. */
  version?: string;
}

export function DecisionTable({ taskId, version }: DecisionTableProps): JSX.Element | null {
  const { t, i18n } = useLingui();
  const [table, setTable] = useState<Table | null>(null);
  const [error, setError] = useState<string | null>(null);

  // `version` is a refetch trigger, not read inside.
  useEffect(() => {
    let alive = true;
    fetchDecisionTable(taskId)
      .then((got) => {
        if (!alive) return;
        setTable(got);
        setError(null);
      })
      .catch((e: unknown) => {
        if (alive) setError(e instanceof Error ? e.message : String(e));
      });
    return () => {
      alive = false;
    };
  }, [taskId, version]);

  if (error !== null) {
    return (
      <div className="mb-3 font-mono text-[11px] text-red" role="alert">
        <Trans>decision table unavailable: {error}</Trans>
      </div>
    );
  }
  if (table === null || table.rows.length === 0) return null;

  const notVerified = table.planReview.status === 'not-verified';
  const reasonClass = table.planReview.reasonClass;
  return (
    <section className="mb-4" aria-label={t`decisions and criteria`}>
      <div className="mb-1.5 flex flex-wrap items-center gap-1.5">
        <h3 className="font-mono text-[10.5px] uppercase tracking-wide text-ink-faint">
          <Trans>decisions and criteria</Trans>
        </h3>
        {STATUS_ORDER.map((s) => {
          const n = table.counts[s] ?? 0;
          if (n === 0) return null;
          return (
            <span key={s} className="font-mono text-[10px] text-ink-faint">
              {i18n._(STATUS_CHIP[s].label)} {n}
            </span>
          );
        })}
      </div>
      {notVerified && (
        <div className="mb-1.5 rounded border border-amber/40 bg-amber/10 px-2 py-1 font-mono text-[10.5px] text-amber">
          <Trans>plan review not verified ({reasonClass}) — ids without evidence are not verified, not remaining</Trans>
        </div>
      )}
      <table className="w-full border-collapse text-left text-[11.5px]">
        <thead>
          <tr className="border-b border-line font-mono text-[9.5px] uppercase text-ink-faint">
            <th scope="col" className="py-1 pr-2 font-normal">
              <Trans>id</Trans>
            </th>
            <th scope="col" className="py-1 pr-2 font-normal">
              <Trans>decision / criterion</Trans>
            </th>
            <th scope="col" className="py-1 pr-2 font-normal">
              <Trans>phases</Trans>
            </th>
            <th scope="col" className="py-1 pr-2 font-normal">
              <Trans>evidence</Trans>
            </th>
            <th scope="col" className="py-1 font-normal">
              <Trans>status</Trans>
            </th>
          </tr>
        </thead>
        <tbody>
          {table.rows.map((r) => {
            const proof = r.status === 'not-implemented' ? r.reason : evidenceLine(r);
            return (
              <tr key={r.id} className="border-b border-line/60 align-top" data-row={r.id}>
                <td className="py-1 pr-2 font-mono text-[10.5px] text-ink">{r.id}</td>
                <td className="py-1 pr-2 text-ink">{r.text}</td>
                <td className="py-1 pr-2 font-mono text-[10px] text-ink-faint">
                  {r.claimedBy.length > 0 ? r.claimedBy.map((s) => `P${String(s)}`).join(' ') : '—'}
                </td>
                <td className="max-w-[320px] truncate py-1 pr-2 text-[10.5px] text-ink-faint" title={proof}>
                  {proof !== '' ? proof : '—'}
                </td>
                <td className="py-1">
                  <DecisionStatusChip status={r.status} reason={r.reason} />
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </section>
  );
}
