// Health → Phase runs: the phase-run baseline (internal/phasereport) over an
// explicit from/to window — the same table `swarmery phase-report` prints. The
// window starts as Health's own range (`?days=`) and can be narrowed with the two
// date inputs; it is bookkeeping, so every row is shown however small.

import { t } from '@lingui/core/macro';
import { Plural, Trans } from '@lingui/react/macro';
import { useEffect, useState } from 'react';
import { type PhaseRunsReport, type PhaseRunsRow, fetchPhaseRunsReport } from '../../api/phasereport';

/** Reopen rows are the defect signal — the one row family the eye should find first. */
export function isReopenRow(row: PhaseRunsRow): boolean {
  return row.key === 'reopens' || row.key.startsWith('reopens_');
}

/** "noop · waiting on push/PR (est.)" — the label as the table shows it. */
export function rowLabel(row: PhaseRunsRow): string {
  const label = row.label;
  return row.estimated ? t`${label} (est.)` : label;
}

function usd(v: number): string {
  return `$${v.toFixed(2)}`;
}

/** The report body for one fetched report. Exported for the test. */
export function PhaseRunsTable({ rep }: { rep: PhaseRunsReport }): JSX.Element {
  const fallback = Object.entries(rep.fallbackRows.byOutcome)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([k, n]) => `${k} ${String(n)}`)
    .join(', ');
  const from = rep.from;
  const to = rep.to;
  const fallbackN = rep.fallbackRows.n;
  return (
    <>
      <table className="mt-3 w-full max-w-2xl border-collapse font-mono text-[11px]">
        <caption className="sr-only">
          <Trans>
            Phase runs from {from} to {to}
          </Trans>
        </caption>
        <thead>
          <tr className="border-b border-line text-left text-[10px] text-ink-faint">
            <th scope="col" className="py-1 pr-2 font-normal">
              <Trans>row</Trans>
            </th>
            <th scope="col" className="py-1 pr-2 text-right font-normal">
              n
            </th>
            <th scope="col" className="py-1 text-right font-normal">
              <Trans>cost</Trans>
            </th>
          </tr>
        </thead>
        <tbody>
          {rep.rows.map((row) => (
            <tr
              key={row.key}
              data-key={row.key}
              className={`border-b border-line ${
                isReopenRow(row) ? (row.n > 0 ? 'bg-amber/10 text-amber' : 'bg-amber/5 text-ink-2') : 'text-ink-2'
              }`}
            >
              <th scope="row" className="py-1 pr-2 text-left font-normal">
                {rowLabel(row)}
              </th>
              <td className="py-1 pr-2 text-right tabular-nums">{String(row.n)}</td>
              <td className="py-1 text-right tabular-nums">{row.costUsd !== undefined ? usd(row.costUsd) : ''}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {rep.fallbackRows.n > 0 && (
        <p className="mt-2 font-mono text-[10.5px] text-ink-dim">
          <Plural
            value={fallbackN}
            one={`# more run(s) ended in the window with no actuals row (${fallback}) — counted here only.`}
            few={`# more run(s) ended in the window with no actuals row (${fallback}) — counted here only.`}
            many={`# more run(s) ended in the window with no actuals row (${fallback}) — counted here only.`}
            other={`# more run(s) ended in the window with no actuals row (${fallback}) — counted here only.`}
          />
        </p>
      )}
      {rep.notes.length > 0 && (
        <ul className="mt-2 grid gap-0.5 font-mono text-[10px] text-ink-faint">
          {rep.notes.map((n) => (
            <li key={n}>{n}</li>
          ))}
        </ul>
      )}
    </>
  );
}

export interface PhaseRunsTabProps {
  /** Health's range, YYYY-MM-DD — the inputs' starting values. */
  from: string;
  to: string;
}

export function PhaseRunsTab({ from: initialFrom, to: initialTo }: PhaseRunsTabProps): JSX.Element {
  const [from, setFrom] = useState(initialFrom);
  const [to, setTo] = useState(initialTo);
  const [rep, setRep] = useState<PhaseRunsReport | null>(null);
  const [err, setErr] = useState<string | null>(null);

  // A new Health range (the strip's preset) resets the inputs.
  useEffect(() => {
    setFrom(initialFrom);
    setTo(initialTo);
  }, [initialFrom, initialTo]);

  useEffect(() => {
    if (from === '' || to === '') return;
    let live = true;
    setErr(null);
    fetchPhaseRunsReport(from, to)
      .then((r) => {
        if (live) setRep(r);
      })
      .catch((e: unknown) => {
        if (!live) return;
        setRep(null);
        setErr(e instanceof Error ? e.message : String(e));
      });
    return () => {
      live = false;
    };
  }, [from, to]);

  return (
    <section className="px-4 py-4 desk:px-7" aria-labelledby="phase-runs-heading">
      <h2 id="phase-runs-heading" className="text-sm text-ink">
        <Trans>Phase runs</Trans>
      </h2>
      <p className="mt-1 max-w-2xl text-[12px] text-ink-dim">
        <Trans>
          How plan-phase runs ended, what the noops were waiting on, what the router and the verifier did, and how
          many finished phases came back. Rows marked (est.) are read from the runs&apos; own words, not a recorded
          fact.
        </Trans>
      </p>
      <div className="mt-2 flex flex-wrap items-center gap-3 font-mono text-[11px] text-ink-dim">
        <label className="flex items-center gap-1.5">
          <Trans>from</Trans>
          <input
            type="date"
            value={from}
            max={to}
            onChange={(e) => setFrom(e.target.value)}
            className="rounded border border-line bg-surface px-1.5 py-0.5 text-ink"
          />
        </label>
        <label className="flex items-center gap-1.5">
          <Trans>to</Trans>
          <input
            type="date"
            value={to}
            min={from}
            onChange={(e) => setTo(e.target.value)}
            className="rounded border border-line bg-surface px-1.5 py-0.5 text-ink"
          />
        </label>
      </div>
      {err !== null && (
        <div role="alert" className="mt-2 text-[12px] text-red">
          {err}
        </div>
      )}
      {rep === null && err === null && <div className="mt-3 text-[12px] text-ink-faint">
          <Trans>loading…</Trans>
        </div>}
      {rep !== null && <PhaseRunsTable rep={rep} />}
    </section>
  );
}
