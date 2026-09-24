import { useCallback, useEffect, useState } from 'react';
import {
  fetchDecisions,
  putDecisionMode,
  type DecideMode,
  type DecisionsResponse,
  type QuestionStats,
} from '../api/decisions';

const MODES: DecideMode[] = ['off', 'shadow', 'active'];

const LABELS: Record<string, string> = {
  'd1.run_end': 'D1 · how did the run end?',
  'd2.task_type': 'D2 · task type',
  'd2.outcome': 'D2 · outcome',
  'd2.failure_cause': 'D2 · failure cause',
  'd3.divergence_cause': 'D3 · why a run diverged from its forecast',
};

function pct(v: number | null): string {
  return v === null ? 'n/a' : `${Math.round(v * 100)}%`;
}

/** Ten confidence buckets as a tiny bar strip. */
function Histogram({ buckets }: { buckets: number[] }): JSX.Element {
  const peak = Math.max(1, ...buckets);
  return (
    <div
      className="flex h-8 items-end gap-px"
      role="img"
      aria-label={`confidence histogram: ${buckets.join(', ')}`}
    >
      {buckets.map((n, i) => (
        <div
          key={i}
          className="w-2 rounded-sm bg-brand/70"
          style={{ height: `${Math.max(n > 0 ? 8 : 2, (n / peak) * 100)}%` }}
          data-tip={`${(i / 10).toFixed(1)}–${((i + 1) / 10).toFixed(1)}: ${String(n)}`}
        />
      ))}
    </div>
  );
}

function Row({
  q,
  onMode,
  busy,
}: {
  q: QuestionStats;
  onMode: (id: string, m: DecideMode) => void;
  busy: boolean;
}): JSX.Element {
  return (
    <tr className="border-t border-line">
      <td className="py-2 pr-4">
        <div className="text-ink">{LABELS[q.questionId] ?? q.questionId}</div>
        <div className="font-mono text-[10px] text-ink-dim">
          {q.questionId} · threshold {q.threshold.toFixed(2)}
        </div>
      </td>
      <td className="py-2 pr-4 font-mono">{q.calls}</td>
      <td className="py-2 pr-4 font-mono">
        {pct(q.agreement)}
        <span className="text-ink-dim"> ({q.agreed}/{q.withTruth})</span>
      </td>
      <td className="py-2 pr-4 font-mono">{q.errors}</td>
      <td className="py-2 pr-4">
        <Histogram buckets={q.histogram} />
      </td>
      <td className="py-2">
        <fieldset className="inline-flex gap-1" aria-label={`mode for ${q.questionId}`}>
          {MODES.map((m) => (
            <button
              key={m}
              type="button"
              disabled={busy}
              aria-pressed={q.mode === m}
              onClick={() => onMode(q.questionId, m)}
              className={`rounded border px-1.5 py-px font-mono text-[10px] ${
                q.mode === m
                  ? 'border-brand text-brand'
                  : 'border-line text-ink-dim hover:text-ink'
              }`}
            >
              {m}
            </button>
          ))}
        </fieldset>
      </td>
    </tr>
  );
}

/** Decisions — the local classifier's per-question accuracy and mode switch
 *  (learning-loop phase 9). Advisory: nothing here gates a run. */
export function Decisions(): JSX.Element {
  const [data, setData] = useState<DecisionsResponse | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    fetchDecisions()
      .then(setData)
      .catch((e: unknown) => setErr(String(e)));
  }, []);

  const onMode = useCallback((id: string, m: DecideMode) => {
    setBusy(true);
    putDecisionMode(id, m)
      .then(setData)
      .catch((e: unknown) => setErr(String(e)))
      .finally(() => setBusy(false));
  }, []);

  return (
    <div className="p-6">
      <h1 className="text-lg text-ink">Decisions</h1>
      <p className="mt-1 max-w-2xl text-[12px] text-ink-dim">
        A small local classifier answers typed questions around Claude runs — never inside them.
        Every question starts in shadow: it is asked and logged, and nothing acts on it. Promote a
        question to active only once its agreement with ground truth is high.
      </p>
      {err !== null && (
        <div role="alert" className="mt-3 text-[12px] text-red">
          {err}
        </div>
      )}
      {data === null && err === null && <div className="mt-4 text-ink-dim">loading…</div>}
      {data !== null && (
        <>
          <div className="mt-3 font-mono text-[11px] text-ink-dim">
            backend: {data.configured ? '' : 'not configured (SWARMERY_DECIDE_URL unset)'}
            {data.local ? 'local' : ''}
            {data.claude ? ' + claude' : ''}
          </div>
          <table className="mt-4 w-full text-left text-[12px]">
            <thead className="text-ink-dim">
              <tr>
                <th className="pb-2 pr-4 font-normal">question</th>
                <th className="pb-2 pr-4 font-normal">calls</th>
                <th className="pb-2 pr-4 font-normal">agreement</th>
                <th className="pb-2 pr-4 font-normal">errors</th>
                <th className="pb-2 pr-4 font-normal">confidence</th>
                <th className="pb-2 font-normal">mode</th>
              </tr>
            </thead>
            <tbody>
              {data.questions.map((q) => (
                <Row key={q.questionId} q={q} onMode={onMode} busy={busy} />
              ))}
            </tbody>
          </table>
        </>
      )}
    </div>
  );
}
