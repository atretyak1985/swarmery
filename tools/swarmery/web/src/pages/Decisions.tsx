import { useCallback, useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import {
  fetchDecisions,
  fetchLabelQueue,
  postGroundTruth,
  putDecisionMode,
  type DecideMode,
  type DecisionsResponse,
  type QuestionStats,
  type QueueItem,
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

/** One queued decision: the model's answer, a one-click confirm, and a picker
 *  for the right answer when the model was wrong. */
function QueueRow({
  item,
  onLabel,
}: {
  item: QueueItem;
  onLabel: (item: QueueItem, value: string) => void;
}): JSX.Element {
  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-1 py-1.5">
      <span className="min-w-44 text-ink-dim">{LABELS[item.questionId] ?? item.questionId}</span>
      <span className="font-mono text-ink">{item.answer}</span>
      {item.confidence !== null && (
        <span className="font-mono text-[10px] text-ink-dim">{pct(item.confidence)}</span>
      )}
      <button
        type="button"
        onClick={() => onLabel(item, item.answer)}
        className="rounded border border-line px-1.5 py-px font-mono text-[10px] text-ink-dim hover:border-green hover:text-green"
      >
        ✓ correct
      </button>
      <label className="inline-flex items-center gap-1 font-mono text-[10px] text-ink-dim">
        <span>or it was</span>
        <select
          aria-label={`correct answer for ${item.questionId}`}
          defaultValue=""
          onChange={(e) => {
            if (e.target.value !== '') onLabel(item, e.target.value);
          }}
          className="rounded border border-line bg-surface px-1 py-px text-ink"
        >
          <option value="">choose…</option>
          {item.options
            .filter((o) => o !== item.answer)
            .map((o) => (
              <option key={o} value={o}>
                {o}
              </option>
            ))}
        </select>
      </label>
    </li>
  );
}

/** The labelling queue: answered decisions with no ground truth yet, grouped by
 *  the session they judge. The decisions table stores an input hash, never the
 *  input, so the operator judges from the session itself (the link). Every
 *  label feeds the agreement column above. */
function LabelQueue({ onLabelled }: { onLabelled: () => void }): JSX.Element {
  const [items, setItems] = useState<QueueItem[] | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    fetchLabelQueue()
      .then(setItems)
      .catch((e: unknown) => setErr(String(e)));
  }, []);

  const groups = useMemo(() => {
    const byKey = new Map<string, QueueItem[]>();
    for (const it of items ?? []) {
      const key = it.sessionUuid !== '' ? it.sessionUuid : it.subject;
      byKey.set(key, [...(byKey.get(key) ?? []), it]);
    }
    return [...byKey.entries()];
  }, [items]);

  const onLabel = useCallback(
    (item: QueueItem, value: string) => {
      setErr(null);
      postGroundTruth(item.id, value)
        .then(() => {
          setItems((cur) => (cur ?? []).filter((i) => i.id !== item.id));
          onLabelled();
        })
        .catch((e: unknown) => setErr(String(e)));
    },
    [onLabelled],
  );

  return (
    <section className="mt-8">
      <h2 className="text-[13px] text-ink">Label queue</h2>
      <p className="mt-1 max-w-2xl text-[12px] text-ink-dim">
        What the model answered, waiting for what actually happened. Open the session, then confirm
        the answer or pick the right one. Agreement above counts only labelled rows.
      </p>
      {err !== null && (
        <div role="alert" className="mt-2 text-[12px] text-red">
          {err}
        </div>
      )}
      {items === null && err === null && <div className="mt-3 text-ink-dim">loading…</div>}
      {items !== null && items.length === 0 && (
        <div className="mt-3 text-[12px] text-ink-dim">Nothing to label.</div>
      )}
      <div className="mt-3 space-y-3 text-[12px]">
        {groups.map(([key, group]) => {
          const first = group[0];
          if (first === undefined) return null;
          return (
            <div key={key} className="rounded-lg border border-line bg-surface/40 px-3 py-2">
              <div className="flex items-baseline gap-2">
                {first.sessionUuid !== '' ? (
                  <Link to={`/sessions/${first.sessionUuid}`} className="truncate text-ink hover:text-brand">
                    {first.sessionTitle !== '' ? first.sessionTitle : first.sessionUuid}
                  </Link>
                ) : (
                  <span className="truncate font-mono text-ink">{first.subject}</span>
                )}
                <span className="shrink-0 font-mono text-[10px] text-ink-faint">
                  {first.createdAt.slice(0, 16).replace('T', ' ')}
                </span>
              </div>
              <ul>
                {group.map((it) => (
                  <QueueRow key={it.id} item={it} onLabel={onLabel} />
                ))}
              </ul>
            </div>
          );
        })}
      </div>
    </section>
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

  const refresh = useCallback(() => {
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
          <LabelQueue onLabelled={refresh} />
        </>
      )}
    </div>
  );
}
