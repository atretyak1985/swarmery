// The whole-plan run: the panel that asks how to run it, and the line that reports
// a run in flight or a failed one. The button that opens the panel lives in the
// plan header, as one of the states of its main action.
//
// The modes offered are what the run-plan skill cannot derive from the phase DAG.

import type { MessageDescriptor } from '@lingui/core';
import { msg } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { fetchSystemItems } from '../../api/system';
import type { PlanRun, PlanRunMode } from '../../api/types';
import { fmtElapsed } from '../../lib/format';
import { useSessionHref } from '../../lib/sessionHref';

/** The execution modes offered when starting a whole-plan run. The run-plan
 * skill triages its own ROUTE (sequential / parallel group / workflow) from the
 * phase DAG — the UI must not duplicate that, since it needs the graph to be
 * meaningful. What the skill cannot derive is the call an interactive session
 * always asks about: dispatch per-phase executors, or do the work inline. */
const PLAN_RUN_MODES: { id: PlanRunMode; label: MessageDescriptor; hint: MessageDescriptor }[] = [
  { id: 'auto', label: msg`auto`, hint: msg`let the run-plan skill triage the route from the phase DAG` },
  {
    id: 'subagents',
    label: msg`subagent-driven`,
    hint: msg`dispatch an implementer + a fresh reviewer per phase`,
  },
  { id: 'inline', label: msg`inline`, hint: msg`the controller implements the phases itself, no dispatch` },
];

/** A run in flight (elapsed, its session, Cancel) or a failed one; nothing otherwise. */
export function PlanRunLive({
  run,
  now,
  busy,
  onCancel,
}: {
  run: PlanRun;
  now: number;
  busy: boolean;
  onCancel: () => void;
}): JSX.Element | null {
  const { t } = useLingui();
  const sessionHref = useSessionHref();
  if (run.runState === 'failed') {
    return (
      <div className="mt-3 font-mono text-[11px] leading-[normal] text-red" data-tip={run.runError ?? t`plan run failed`}>
        <Trans>Run failed</Trans>
      </div>
    );
  }
  if (run.runState !== 'running') return null;
  const agent = run.agent;
  const mode = run.mode;
  const agentPart = agent !== null ? t` — agent ${agent}` : '';
  return (
    <div className="mt-3 flex items-center gap-2 font-mono text-[11px] leading-[normal] text-ink-dim">
      <span data-tip={t`the whole plan is running${agentPart} (mode: ${mode})`}>
        <Trans>Running plan</Trans>
        {run.runStartedAt !== null ? ` · ${fmtElapsed(run.runStartedAt, now)}` : ''}
      </span>
      {run.runSessionUuid !== null && (
        <Link
          to={sessionHref(run.runSessionUuid)}
          className="underline-offset-2 transition-colors hover:text-brand hover:underline"
        >
          <Trans>session</Trans>
        </Link>
      )}
      <button
        type="button"
        disabled={busy}
        onClick={onCancel}
        className="text-red transition-colors hover:underline disabled:cursor-not-allowed disabled:opacity-50"
      >
        <Trans>Cancel</Trans>
      </button>
    </div>
  );
}

/** The inline "how should this run?" panel — agent + execution mode. Inline
 * rather than modal, matching the rest of this page. It takes the full row so
 * it reads as a step, not as a dropdown hanging off the button. */
export function PlanRunDialog({
  initialAgent,
  initialMode,
  onRun,
  onCancel,
}: {
  initialAgent: string;
  initialMode: PlanRunMode;
  onRun: (agent: string, mode: PlanRunMode) => void;
  onCancel: () => void;
}): JSX.Element {
  const { t, i18n } = useLingui();
  const [agent, setAgent] = useState(initialAgent);
  const [mode, setMode] = useState<PlanRunMode>(initialMode);
  const [agents, setAgents] = useState<string[] | null>(null);

  // The picker is the reason this is a panel and not a bare button: the agent
  // list is only worth fetching once someone actually opens it.
  useEffect(() => {
    let alive = true;
    fetchSystemItems('agents')
      .then((items) => {
        if (!alive) return;
        const names = [...new Set(items.map((i) => i.name))].sort((a, b) => a.localeCompare(b));
        setAgents(names);
      })
      .catch(() => {
        if (alive) setAgents([]); // the free-text field still works
      });
    return () => {
      alive = false;
    };
  }, []);

  const modeDef = PLAN_RUN_MODES.find((m) => m.id === mode);
  const modeHint = modeDef !== undefined ? i18n._(modeDef.hint) : '';

  return (
    <div className="mt-3 w-full rounded-lg border border-line bg-surface/60 px-3 py-2.5">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <label className="flex items-center gap-1.5">
          <span className="font-mono text-[10px] uppercase tracking-wider text-ink-faint">
            <Trans>agent</Trans>
          </span>
          <input
            list="plan-run-agents"
            value={agent}
            onChange={(e) => setAgent(e.target.value)}
            placeholder={agents === null ? t`loading…` : t`tech-lead (default)`}
            className="w-[190px] rounded-md border border-line bg-field px-2 py-1 font-mono text-[11px] text-ink outline-none focus:border-ink-dim"
          />
          <datalist id="plan-run-agents">
            {(agents ?? []).map((a) => (
              <option key={a} value={a} />
            ))}
          </datalist>
        </label>

        <div className="flex items-center gap-1.5" role="radiogroup" aria-label={t`execution mode`}>
          <span className="font-mono text-[10px] uppercase tracking-wider text-ink-faint">
            <Trans>mode</Trans>
          </span>
          {PLAN_RUN_MODES.map((m) => (
            <button
              key={m.id}
              type="button"
              role="radio"
              aria-checked={mode === m.id}
              data-tip={i18n._(m.hint)}
              onClick={() => setMode(m.id)}
              className={`rounded-md border px-2 py-1 font-mono text-[10.5px] transition-colors ${
                mode === m.id
                  ? 'border-brand/50 bg-brand/10 text-brand'
                  : 'border-line text-ink-dim hover:border-line-strong hover:text-ink'
              }`}
            >
              {i18n._(m.label)}
            </button>
          ))}
        </div>

        <div className="ml-auto flex items-center gap-1.5">
          <button
            type="button"
            onClick={onCancel}
            className="rounded-md border border-line px-2 py-1 font-mono text-[10.5px] text-ink-dim transition-colors hover:text-ink"
          >
            <Trans>cancel</Trans>
          </button>
          <button
            type="button"
            onClick={() => onRun(agent.trim(), mode)}
            className="rounded-md border border-brand/50 bg-brand/10 px-2.5 py-1 font-mono text-[10.5px] text-brand transition-colors hover:bg-brand/20"
          >
            <Trans>▶ Run</Trans>
          </button>
        </div>
      </div>
      <p className="mt-2 font-mono text-[10px] leading-relaxed text-ink-faint">
        {modeHint} ·{' '}
        <Trans>
          the run executes core&apos;s <code className="text-ink-dim">run-plan</code> skill headlessly in an
          isolated worktree: it commits per phase but never pushes, and stops with PLAN BLOCKED if a phase needs a
          human.
        </Trans>
      </p>
    </div>
  );
}
