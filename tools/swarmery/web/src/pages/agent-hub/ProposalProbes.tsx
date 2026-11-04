// The probe table on a proposal card (harness² phases 4 and 6): run the agent's
// promptfoo suite — or, opt-in, the frozen bench as whole sessions — on the base
// agent and on the proposal, then show one row per case or task: the
// deterministic result on each side and, where they agree, the blind judge's
// pick. Advisory: a probe never approves, applies or rejects.
import type { MessageDescriptor } from '@lingui/core';
import { msg } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { useCallback, useEffect, useState } from 'react';
import { fetchProposalProbes, startProposalProbe } from '../../api';
import type {
  AgentChangeProposal,
  ProbeChoice,
  ProbeJob,
  ProbeSource,
  ProposalProbe,
  ProposalProbesResp,
} from '../../api/types';

/** How often the card re-reads while a probe job is running. */
export const PROBE_POLL_MS = 5000;

const CHOICE_LABEL: Record<ProbeChoice, MessageDescriptor> = {
  base: msg`base`,
  variant: msg`proposal`,
  tie: msg`tie`,
  skipped: msg`not judged`,
  capped: msg`capped`,
  no_suite: msg`no suite`,
};

const CHOICE_TONE: Record<ProbeChoice, string> = {
  base: 'text-amber',
  variant: 'text-green',
  tie: 'text-ink-dim',
  skipped: 'text-ink-faint',
  capped: 'text-amber',
  no_suite: 'text-ink-faint',
};

const SOURCE_TITLE: Record<ProbeSource, MessageDescriptor> = {
  promptfoo: msg`eval suite`,
  bench: msg`frozen bench`,
};

const SOURCES: readonly ProbeSource[] = ['promptfoo', 'bench'];

/** A proposal can be probed while it is still a decision to make. */
export function canProbe(p: Pick<AgentChangeProposal, 'status'>): boolean {
  return p.status === 'proposed' || p.status === 'approved';
}

function PassCell({ pass }: { pass: boolean | null }): JSX.Element {
  const { t } = useLingui();
  if (pass === null) return <span className="text-ink-faint">·</span>;
  return pass ? (
    <span className="text-green" aria-label={t`pass`}>
      ✓
    </span>
  ) : (
    <span className="text-red" aria-label={t`fail`}>
      ✗
    </span>
  );
}

function usd(n: number): string {
  return `$${n.toFixed(2)}`;
}

/**
 * The latest job's rows (the API sends newest job first), of one source when
 * given.
 */
export function latestRun(probes: readonly ProposalProbe[], source?: ProbeSource): ProposalProbe[] {
  const mine = source === undefined ? probes : probes.filter((r) => r.source === source);
  const first = mine[0];
  if (first === undefined) return [];
  return mine.filter((r) => r.job_id === first.job_id);
}

/** One source's latest run as a table, with its job error and total. */
function ProbeTable({
  source,
  probes,
  jobs,
}: {
  source: ProbeSource;
  probes: readonly ProposalProbe[];
  jobs: readonly ProbeJob[];
}): JSX.Element | null {
  const { i18n, t } = useLingui();
  const rows = latestRun(probes, source);
  const shownJob = rows[0]?.job_id;
  const failed = jobs.find(
    (j) => j.source === source && j.status === 'failed' && (shownJob === undefined || j.id === shownJob),
  );
  if (rows.length === 0 && failed?.error === undefined) return null;
  const earlier = new Set(probes.filter((r) => r.source === source).map((r) => r.job_id)).size - (rows.length > 0 ? 1 : 0);
  const total = rows.reduce((sum, r) => sum + r.usd, 0);

  return (
    <div data-testid={`probe-table-${source}`} className="mt-1.5">
      <div className="font-mono text-[10px] text-ink-faint">{i18n._(SOURCE_TITLE[source])}</div>
      {failed?.error !== undefined && (
        <p data-testid="probe-job-error" className="mt-1 font-mono text-[10.5px] text-red">
          {failed.error}
        </p>
      )}
      {rows.length > 0 && (
        <table className="mt-0.5 w-full font-mono text-[10.5px]">
          <thead>
            <tr className="text-left text-ink-faint">
              <th className="py-0.5 pr-2 font-normal">{source === 'bench' ? <Trans>task</Trans> : <Trans>case</Trans>}</th>
              <th className="px-1 font-normal">
                <Trans>base</Trans>
              </th>
              <th className="px-1 font-normal">
                <Trans>proposal</Trans>
              </th>
              <th className="px-1 font-normal" data-tip={t`The blind judge's pick where both sides agree on the hard checks`}>
                <Trans>blind pick</Trans>
              </th>
              <th className="pl-1 text-right font-normal">
                <Trans>cost</Trans>
              </th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.id} className="border-t border-line align-top" data-testid="probe-row">
                <td className="py-0.5 pr-2 text-ink-dim" data-tip={r.reason === '' ? undefined : r.reason}>
                  {r.task_id === '' ? '—' : r.task_id}
                </td>
                <td className="px-1">
                  <PassCell pass={r.base_pass} />
                </td>
                <td className="px-1">
                  <PassCell pass={r.variant_pass} />
                </td>
                <td className={`px-1 ${CHOICE_TONE[r.blind_choice]}`} data-testid="probe-choice">
                  {i18n._(CHOICE_LABEL[r.blind_choice])}
                </td>
                <td className="pl-1 text-right text-ink-faint">{usd(r.usd)}</td>
              </tr>
            ))}
          </tbody>
          <tfoot>
            <tr className="border-t border-line text-ink-faint">
              <td className="py-0.5 pr-2" colSpan={4}>
                {earlier > 0 ? <Trans>total · earlier runs not shown: {earlier}</Trans> : <Trans>total</Trans>}
              </td>
              <td className="pl-1 text-right" data-testid={`probe-total-${source}`}>
                {usd(total)}
              </td>
            </tr>
          </tfoot>
        </table>
      )}
    </div>
  );
}

export function ProposalProbes({ p }: { p: AgentChangeProposal }): JSX.Element | null {
  const { t } = useLingui();
  const [data, setData] = useState<ProposalProbesResp | null>(null);
  const [starting, setStarting] = useState<ProbeSource | null>(null);
  const [err, setErr] = useState<string | null>(null);

  const load = useCallback((): void => {
    fetchProposalProbes(p.id)
      .then(setData)
      .catch(() => setData(null));
  }, [p.id]);
  useEffect(load, [load]);

  const runningJob = data?.jobs.find((j) => j.status === 'running');
  const running = runningJob !== undefined;
  useEffect(() => {
    if (!running) return undefined;
    const timer = window.setInterval(load, PROBE_POLL_MS);
    return () => window.clearInterval(timer);
  }, [running, load]);

  const start = (source: ProbeSource): void => {
    setStarting(source);
    setErr(null);
    startProposalProbe(p.id, source)
      .then(load)
      .catch((e: unknown) => setErr(e instanceof Error ? e.message : String(e)))
      .finally(() => setStarting(null));
  };

  const probes = data?.probes ?? [];
  const jobs = data?.jobs ?? [];
  const cap = usd(data?.max_usd ?? 0);
  const benchCap = usd(data?.bench_max_usd ?? 0);
  const ran = (source: ProbeSource): boolean => probes.some((r) => r.source === source);
  if (!canProbe(p) && probes.length === 0) return null;

  const busy = starting !== null || running;
  const buttonClass =
    'rounded-[7px] border border-line-strong px-2 py-[3px] font-mono text-[10.5px] text-ink-dim transition-colors hover:border-brand/40 hover:text-brand disabled:opacity-50';

  return (
    <section data-testid="proposal-probes" aria-label={t`proposal probe`} className="mt-2 rounded-[7px] border border-line p-2">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-mono text-[10px] text-ink-faint">
          <Trans>Probe: the agent's eval suite on the current file vs this proposal</Trans>
        </span>
        {canProbe(p) && (
          <span className="ml-auto flex flex-wrap items-center gap-2">
            <button
              type="button"
              disabled={busy}
              onClick={() => start('promptfoo')}
              data-tip={t`Runs every suite case twice through claude -p and stops at the cost cap of ${cap}. It never approves, applies or rejects.`}
              className={buttonClass}
            >
              {runningJob?.source === 'promptfoo' ? t`probing…` : ran('promptfoo') ? t`Probe again` : t`Probe`}
            </button>
            <button
              type="button"
              disabled={busy}
              onClick={() => start('bench')}
              data-tip={t`Runs each frozen-bench task tagged for this agent as a whole session, once as the current agent and once as the proposal, and stops between tasks at the bench cap of ${benchCap}. It never approves, applies or rejects.`}
              className={buttonClass}
            >
              {runningJob?.source === 'bench' ? t`running bench…` : ran('bench') ? t`Run bench again` : t`Run bench`}
            </button>
            <span data-testid="probe-bench-cap" className="font-mono text-[10px] text-ink-faint">
              <Trans>bench cap {benchCap}</Trans>
            </span>
          </span>
        )}
      </div>

      <p data-testid="probe-sample-warning" className="mt-1 font-mono text-[10px] leading-relaxed text-amber">
        <Trans>
          One sample per case: treat a single flip as noise and a difference that repeats across runs as signal.
        </Trans>
      </p>

      {err !== null && <p className="mt-1 font-mono text-[10.5px] text-red">{err}</p>}

      {SOURCES.map((source) => (
        <ProbeTable key={source} source={source} probes={probes} jobs={jobs} />
      ))}
    </section>
  );
}
