// A phase's reopen ledger in its Runs tab (phase-run outcomes plan, phase 1):
// the "Reopened" history — every time the finished phase was sent back because a
// defect slipped past its gates, and which gate caught it — and, on a Done
// phase, the Reopen action. Reopening records the reason, the fix URL and the
// gate, and unticks the chosen criteria in the doc (POST …/reopen); the form's
// checkbox list is the doc's ticked criteria as the server normalises them, so a
// chosen label always matches.

import { t } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { useEffect, useId, useState } from 'react';
import { fetchPhaseReopens, reopenPhase } from '../../api/phasereport';
import type { PhaseReopen, ReopenCaughtBy } from '../../api/types';
import { fmtAgo } from '../../lib/format';

/** The gates in the form's select. Labels are locale getters, read at render. */
export const CAUGHT_BY: readonly { value: ReopenCaughtBy; label: string }[] = [
  {
    value: 'operator',
    get label() {
      return t`operator`;
    },
  },
  {
    value: 'verifier',
    get label() {
      return t`verifier`;
    },
  },
  {
    value: 'review',
    get label() {
      return t`review`;
    },
  },
  {
    value: 'none',
    get label() {
      return t`nobody (it shipped)`;
    },
  },
];

const CAUGHT_CLS: Record<ReopenCaughtBy, string> = {
  verifier: 'border-green/40 text-green',
  review: 'border-green/40 text-green',
  operator: 'border-amber/40 text-amber',
  none: 'border-red/40 text-red',
};

/** "caught by review" / "caught by nobody" — the chip text. */
export function caughtByText(c: ReopenCaughtBy): string {
  if (c === 'none') return t`caught by nobody`;
  const gate = CAUGHT_BY.find((g) => g.value === c)?.label ?? c;
  return t`caught by ${gate}`;
}

export interface PhaseReopensProps {
  taskId: number;
  phaseId: number;
  reopens: PhaseReopen[];
  /** The phase is Done and not running — the only state a reopen makes sense in. */
  canReopen: boolean;
  /** After a reopen: refetch the plan (the doc and its counts changed). */
  onReopened: () => void;
}

function ReopenForm({
  taskId,
  phaseId,
  onDone,
  onCancel,
}: {
  taskId: number;
  phaseId: number;
  onDone: () => void;
  onCancel: () => void;
}): JSX.Element {
  const { t } = useLingui();
  const id = useId();
  const [ticked, setTicked] = useState<string[] | null>(null);
  const [chosen, setChosen] = useState<Set<string>>(new Set());
  const [reason, setReason] = useState('');
  const [fixUrl, setFixUrl] = useState('');
  const [caughtBy, setCaughtBy] = useState<ReopenCaughtBy>('operator');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    fetchPhaseReopens(taskId, phaseId)
      .then((r) => {
        if (live) setTicked(r.ticked);
      })
      .catch((e: unknown) => {
        if (live) setErr(e instanceof Error ? e.message : String(e));
      });
    return () => {
      live = false;
    };
  }, [taskId, phaseId]);

  const toggle = (label: string): void => {
    setChosen((prev) => {
      const next = new Set(prev);
      if (next.has(label)) next.delete(label);
      else next.add(label);
      return next;
    });
  };
  const ready = reason.trim() !== '' && chosen.size > 0 && !busy;
  const submit = (): void => {
    if (!ready) return;
    setBusy(true);
    setErr(null);
    reopenPhase(taskId, phaseId, {
      reason: reason.trim(),
      ...(fixUrl.trim() !== '' ? { fixUrl: fixUrl.trim() } : {}),
      caughtBy,
      // Document order, not click order.
      criteria: (ticked ?? []).filter((l) => chosen.has(l)),
    })
      .then(onDone)
      .catch((e: unknown) => setErr(e instanceof Error ? e.message : String(e)))
      .finally(() => setBusy(false));
  };

  return (
    <form
      aria-label={t`reopen this phase`}
      className="mt-2 grid gap-2 rounded-md border border-line bg-surface-2 px-3 py-2.5 text-[12px]"
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
    >
      <label className="grid gap-1">
        <span className="font-mono text-[10px] text-ink-faint">
          <Trans>what slipped through</Trans>
        </span>
        <textarea
          required
          rows={2}
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          className="rounded border border-line bg-surface px-2 py-1 text-ink"
        />
      </label>
      <label className="grid gap-1">
        <span className="font-mono text-[10px] text-ink-faint">
          <Trans>fix URL (optional)</Trans>
        </span>
        <input
          type="url"
          value={fixUrl}
          onChange={(e) => setFixUrl(e.target.value)}
          className="rounded border border-line bg-surface px-2 py-1 text-ink"
        />
      </label>
      <label className="grid gap-1">
        <span className="font-mono text-[10px] text-ink-faint">
          <Trans>caught by</Trans>
        </span>
        <select
          value={caughtBy}
          onChange={(e) => setCaughtBy(e.target.value as ReopenCaughtBy)}
          className="rounded border border-line bg-surface px-2 py-1 text-ink"
        >
          {CAUGHT_BY.map((c) => (
            <option key={c.value} value={c.value}>
              {c.label}
            </option>
          ))}
        </select>
      </label>
      <fieldset className="grid gap-1" aria-describedby={`${id}-crit`}>
        <legend id={`${id}-crit`} className="font-mono text-[10px] text-ink-faint">
          <Trans>criteria to untick</Trans>
        </legend>
        {ticked === null && err === null && <span className="text-ink-faint">
            <Trans>loading criteria…</Trans>
          </span>}
        {ticked !== null && ticked.length === 0 && (
          <span className="text-ink-dim">
            <Trans>No ticked criteria in this phase&apos;s doc — nothing to reopen.</Trans>
          </span>
        )}
        {(ticked ?? []).map((label, i) => (
          <label key={`${i}-${label}`} className="flex items-start gap-2 text-ink-2">
            <input
              type="checkbox"
              checked={chosen.has(label)}
              onChange={() => toggle(label)}
              className="mt-0.5"
            />
            <span>{label}</span>
          </label>
        ))}
      </fieldset>
      {err !== null && (
        <div role="alert" className="text-red">
          {err}
        </div>
      )}
      <div className="flex gap-2">
        <button
          type="submit"
          disabled={!ready}
          className="rounded-md border border-amber/40 px-2 py-1 font-mono text-[10px] text-amber hover:bg-amber/10 disabled:cursor-not-allowed disabled:opacity-50"
        >
          {busy ? t`reopening…` : t`Reopen and untick`}
        </button>
        <button
          type="button"
          onClick={onCancel}
          className="rounded-md border border-line px-2 py-1 font-mono text-[10px] text-ink-dim hover:text-ink"
        >
          <Trans>cancel</Trans>
        </button>
      </div>
    </form>
  );
}

export function PhaseReopens({ taskId, phaseId, reopens, canReopen, onReopened }: PhaseReopensProps): JSX.Element | null {
  const { t } = useLingui();
  const [open, setOpen] = useState(false);
  // Stepping to another phase closes a half-filled form.
  useEffect(() => {
    setOpen(false);
  }, [phaseId]);
  if (reopens.length === 0 && !canReopen) return null;
  const count = String(reopens.length);
  return (
    <div className="mt-3">
      {reopens.length > 0 && (
        <section aria-label={t`Reopened`}>
          <h3 className="font-mono text-[10px] tracking-[0.14em] text-amber uppercase">
            <Trans>Reopened · {count}</Trans>
          </h3>
          <ul className="mt-1 grid gap-1.5">
            {reopens.map((r) => (
              <li key={r.id} className="rounded-md border border-amber/30 px-2.5 py-1.5 text-[12px]">
                <div className="flex flex-wrap items-center gap-1.5 font-mono text-[10px] text-ink-faint">
                  <span className={`rounded border px-1.5 py-px text-[9px] ${CAUGHT_CLS[r.caughtBy]}`}>
                    {caughtByText(r.caughtBy)}
                  </span>
                  <span title={r.createdAt}>{fmtAgo(r.createdAt)}</span>
                  {r.fixUrl !== '' && (
                    <a href={r.fixUrl} target="_blank" rel="noreferrer" className="text-brand hover:underline">
                      <Trans>fix →</Trans>
                    </a>
                  )}
                </div>
                <p className="mt-1 text-ink-2">{r.reason}</p>
                {r.criteria.length > 0 && (
                  <ul className="mt-1 list-inside list-disc font-mono text-[10.5px] text-ink-dim">
                    {r.criteria.map((c) => (
                      <li key={c}>{c}</li>
                    ))}
                  </ul>
                )}
              </li>
            ))}
          </ul>
        </section>
      )}
      {canReopen && !open && (
        <button
          type="button"
          onClick={() => setOpen(true)}
          className="mt-2 rounded-md border border-amber/40 px-2 py-1 font-mono text-[10px] text-amber hover:bg-amber/10"
        >
          <Trans>Reopen…</Trans>
        </button>
      )}
      {canReopen && open && (
        <ReopenForm
          taskId={taskId}
          phaseId={phaseId}
          onCancel={() => setOpen(false)}
          onDone={() => {
            setOpen(false);
            onReopened();
          }}
        />
      )}
    </div>
  );
}
