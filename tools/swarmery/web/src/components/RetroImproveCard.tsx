// The page-level improver on /retro: one button that reads the whole window,
// and the human gate between the model's opinion and any work.
//
// The card IS the state machine of a retro_analyses row (migration 0061), and
// every state has visible text, because the complaint this feature answers was
// "I see some report and that's it":
//
//   idle      → what the button will do, before it is pressed
//   running   → it is generating, and for how long
//   failed    → the row's error VERBATIM, plus retry
//   proposed  → the analysis, its citation count, accept / dismiss
//               (deliberately NO plan button: that is what the gate means)
//   accepted  → pick a project, then create the plan
//   planned   → a link to the planning session
//
// A planning conflict is a state too, not an exception: the 409 carries the
// active session, so the card links to it rather than printing a status code.

import { Plural, Trans, useLingui } from '@lingui/react/macro';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import {
  decideRetroAnalysis,
  fetchRetroAnalysis,
  planFromRetroAnalysis,
  RetroPlanConflictError,
  startRetroAnalysis,
  type AnalyticsRange,
} from '../api';
import type { RetroAnalysis } from '../api/types';
import { Markdown } from '../lib/markdown';
import { useScope } from '../lib/scope';
import { Explain } from './Explain';

/** Poll cadence while a run is in flight — the same rhythm PlanningMode uses. */
const POLL_MS = 4_000;

/** A conflict the card renders as a state, with a link instead of a code. */
interface Conflict {
  message: string;
  projectSlug: string;
}

function elapsed(fromISO: string, nowMs: number): string {
  const started = Date.parse(fromISO);
  if (Number.isNaN(started)) return '';
  const sec = Math.max(0, Math.round((nowMs - started) / 1000));
  if (sec < 60) return `${String(sec)}s`;
  return `${String(Math.floor(sec / 60))}m ${String(sec % 60)}s`;
}

export function RetroImproveCard({ range }: { range: AnalyticsRange }): JSX.Element {
  const { t } = useLingui();
  const { scope, projects, scopeProject } = useScope();
  const [analysis, setAnalysis] = useState<RetroAnalysis | null>(null);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  const [conflict, setConflict] = useState<Conflict | null>(null);
  const [nowMs, setNowMs] = useState(() => Date.now());
  // Pre-filled from the current scope, but always the operator's choice: the
  // changes land in the agent system's own repository, which is usually NOT
  // the project whose sessions produced the evidence.
  const [target, setTarget] = useState<number | null>(null);
  const touchedTarget = useRef(false);

  const load = useCallback((): void => {
    fetchRetroAnalysis(scope ?? undefined)
      .then((r) => setAnalysis(r.analysis))
      .catch(() => setAnalysis(null)); // endpoint unavailable → idle card
  }, [scope]);
  useEffect(load, [load]);

  const running = analysis?.status === 'running';

  // Poll only while something is actually in flight.
  useEffect(() => {
    if (!running) return undefined;
    const t = window.setInterval(load, POLL_MS);
    return () => {
      window.clearInterval(t);
    };
  }, [running, load]);

  // Elapsed timer, same gate as the poll.
  useEffect(() => {
    if (!running) return undefined;
    const t = window.setInterval(() => {
      setNowMs(Date.now());
    }, 1_000);
    return () => {
      window.clearInterval(t);
    };
  }, [running]);

  // Follow the scope until the operator overrides it.
  useEffect(() => {
    if (touchedTarget.current) return;
    setTarget(scopeProject?.id ?? null);
  }, [scopeProject]);

  const run = useCallback(
    (fn: () => Promise<unknown>): void => {
      setBusy(true);
      setFailure(null);
      setConflict(null);
      fn()
        .then(() => {
          load();
        })
        .catch((e: unknown) => {
          if (e instanceof RetroPlanConflictError) {
            setConflict({ message: e.message, projectSlug: e.projectSlug });
            return;
          }
          setFailure(e instanceof Error ? e.message : String(e));
        })
        .finally(() => {
          setBusy(false);
        });
    },
    [load],
  );

  const onStart = useCallback(() => {
    run(() => startRetroAnalysis(range));
  }, [run, range]);

  const onDecide = useCallback(
    (status: 'accepted' | 'dismissed') => {
      if (analysis === null) return;
      run(() => decideRetroAnalysis(analysis.id, status));
    },
    [run, analysis],
  );

  const onPlan = useCallback(() => {
    if (analysis === null || target === null) return;
    run(() => planFromRetroAnalysis(analysis.id, target));
  }, [run, analysis, target]);

  const plannedSlug = useMemo(
    () => projects.find((p) => p.id === target)?.slug ?? scopeProject?.slug ?? '',
    [projects, target, scopeProject],
  );

  const status = analysis?.status ?? 'idle';
  const runningFor = analysis !== null ? elapsed(analysis.createdAt, nowMs) : '';

  return (
    <section className="mt-[18px] rounded-[14px] border border-line bg-surface px-4 py-3.5">
      <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
        <span className="font-mono text-[10px] uppercase tracking-[0.14em] text-ink-faint">
          <Trans>Improve the system</Trans>
        </span>
        <Explain id="retro-improve" />
        {analysis !== null && (
          <span className="font-mono text-[10px] text-ink-faint">
            {analysis.windowFrom} → {analysis.windowTo}
            {analysis.scope === '' ? t` · whole fleet` : ` · ${analysis.scope}`}
          </span>
        )}
      </div>

      {status === 'idle' || status === 'dismissed' ? (
        <div className="mt-2 flex flex-wrap items-center gap-3">
          <button
            type="button"
            disabled={busy}
            onClick={onStart}
            className="rounded-[7px] border border-line-strong px-2.5 py-[4px] font-mono text-[10.5px] text-ink-dim transition-colors hover:border-brand/40 hover:text-brand disabled:opacity-50"
          >
            {busy ? t`starting…` : t`Improve`}
          </button>
          <p className="text-[11.5px] leading-relaxed text-ink-dim">
            <Trans>
              Reads this whole window, has an agent write what hurts and what to change — every
              claim citing the evidence it came from. Nothing is written anywhere until you accept
              it; only then can it become a plan.
            </Trans>
          </p>
        </div>
      ) : null}

      {status === 'running' && analysis !== null && (
        <div className="mt-2 flex items-center gap-3">
          <button
            type="button"
            disabled
            className="rounded-[7px] border border-line px-2.5 py-[4px] font-mono text-[10.5px] text-ink-faint opacity-50"
          >
            <Trans>analysing…</Trans>
          </button>
          <span className="font-mono text-[11px] text-ink-dim">
            <Trans>the improver is reading the report · {runningFor}</Trans>
          </span>
        </div>
      )}

      {status === 'failed' && analysis !== null && (
        <div className="mt-2 flex flex-col gap-2">
          {/* The row's own error, verbatim. Never "something went wrong": the
              reason is usually actionable (a refused citation, a stderr tail). */}
          <pre className="overflow-x-auto rounded-[8px] border border-red/30 bg-red/5 px-2.5 py-2 font-mono text-[11px] whitespace-pre-wrap text-red">
            {analysis.error === '' ? t`the analysis failed without a recorded reason` : analysis.error}
          </pre>
          <div>
            <button
              type="button"
              disabled={busy}
              onClick={onStart}
              className="rounded-[7px] border border-line-strong px-2.5 py-[4px] font-mono text-[10.5px] text-ink-dim transition-colors hover:border-brand/40 hover:text-brand disabled:opacity-50"
            >
              {busy ? t`starting…` : t`Try again`}
            </button>
          </div>
        </div>
      )}

      {(status === 'proposed' || status === 'accepted' || status === 'planned') &&
        analysis !== null && (
          <>
            <div className="mt-2 font-mono text-[10px] text-ink-faint">
              <Plural
                value={analysis.citations}
                one="# evidence citation"
                few="# evidence citations"
                many="# evidence citations"
                other="# evidence citations"
              />
            </div>
            <div className="mt-2 max-w-[90ch] text-[12.5px] leading-relaxed">
              <Markdown text={analysis.markdown} />
            </div>
          </>
        )}

      {status === 'proposed' && (
        // Accept or dismiss ONLY. The plan button appears one state later —
        // that separation is the gate, not a UI nicety.
        <div className="mt-3 flex flex-wrap items-center gap-2">
          <button
            type="button"
            disabled={busy}
            onClick={() => {
              onDecide('accepted');
            }}
            className="rounded-[7px] border border-line-strong px-2.5 py-[4px] font-mono text-[10.5px] text-ink-dim transition-colors hover:border-green/40 hover:text-green disabled:opacity-50"
          >
            <Trans>Accept</Trans>
          </button>
          <button
            type="button"
            disabled={busy}
            onClick={() => {
              onDecide('dismissed');
            }}
            className="rounded-[7px] border border-line px-2.5 py-[4px] font-mono text-[10.5px] text-ink-faint transition-colors hover:border-red/40 hover:text-red disabled:opacity-50"
          >
            <Trans>Dismiss</Trans>
          </button>
        </div>
      )}

      {status === 'accepted' && (
        <div className="mt-3 flex flex-wrap items-center gap-2">
          <label className="font-mono text-[10.5px] text-ink-faint" htmlFor="retro-plan-project">
            <Trans>plan in</Trans>
          </label>
          <select
            id="retro-plan-project"
            value={target === null ? '' : String(target)}
            onChange={(e) => {
              touchedTarget.current = true;
              setTarget(e.target.value === '' ? null : Number(e.target.value));
            }}
            className="rounded-[7px] border border-line bg-surface px-2 py-[3px] font-mono text-[10.5px] text-ink-dim"
          >
            <option value="">{t`choose a project…`}</option>
            {projects.map((p) => (
              <option key={p.id} value={String(p.id)}>
                {p.name ?? p.slug}
              </option>
            ))}
          </select>
          <button
            type="button"
            disabled={busy || target === null}
            onClick={onPlan}
            title={target === null ? t`choose a project to plan in` : undefined}
            className="rounded-[7px] border border-line-strong px-2.5 py-[4px] font-mono text-[10.5px] text-ink-dim transition-colors hover:border-brand/40 hover:text-brand disabled:opacity-50"
          >
            {busy ? t`starting…` : t`Create a plan`}
          </button>
          {target === null && (
            <span className="font-mono text-[10.5px] text-ink-faint">
              <Trans>
                choose a project — the changes land in the agent system’s repository, not
                necessarily the one that produced the evidence
              </Trans>
            </span>
          )}
        </div>
      )}

      {status === 'planned' && analysis !== null && (
        <div className="mt-3">
          <Link
            to={`/p/${plannedSlug}/planning`}
            className="font-mono text-[11px] text-brand hover:underline"
          >
            <Trans>open the planning session →</Trans>
          </Link>
        </div>
      )}

      {conflict !== null && (
        // SC-7 in the UI: a sentence and a link, never a bare 409.
        <div className="mt-3 rounded-[8px] border border-amber/40 bg-amber/5 px-2.5 py-2 text-[11.5px] text-ink-dim">
          {conflict.message}.{' '}
          {conflict.projectSlug !== '' && (
            <Link
              to={`/p/${conflict.projectSlug}/planning`}
              className="font-mono text-[11px] text-brand hover:underline"
            >
              <Trans>open the active session →</Trans>
            </Link>
          )}
        </div>
      )}

      {failure !== null && (
        <div className="mt-3 font-mono text-[10.5px] text-red">{failure}</div>
      )}
    </section>
  );
}
