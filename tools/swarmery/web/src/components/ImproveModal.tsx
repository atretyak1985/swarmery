// Improve-agent preview modal — mirrors AttachModal's structure. On open it
// fetches the READ-ONLY evidence bundle the rewriter would feed the model
// (GET /api/retro/agents/{agent}/evidence), shows a scorecard summary plus the
// raw bundle behind a collapsible, then "Generate proposal" fires the real
// (minutes-long) generation (POST …/improve) and, on success, closes the modal
// and refreshes the proposals rail.
//
// Built-in agents (no editable definition file) resolve to in_registry:false —
// the modal then only offers Close (the Improve button is normally hidden for
// them upstream, so this is a defensive path).

import { Trans, useLingui } from '@lingui/react/macro';
import { useEffect, useState } from 'react';
import type { AgentEvidence, RetroAgentRow } from '../api/types';
import { fetchAgentEvidence, improveAgent } from '../api';

type Phase =
  | { kind: 'loading' }
  | { kind: 'ready'; evidence: AgentEvidence }
  | { kind: 'generating'; evidence: AgentEvidence }
  | { kind: 'error'; message: string };

/** behavior/harness/infra split of the scorecard's raw error-class tally. */
function errSplit(byClass: Record<string, number> | undefined): {
  behavior: number;
  harness: number;
  infra: number;
} {
  return {
    behavior: byClass?.['behavior_fixable'] ?? 0,
    harness: byClass?.['harness_recoverable'] ?? 0,
    infra: byClass?.['infra_noise'] ?? 0,
  };
}

export function ImproveModal({
  row,
  onClose,
  onGenerated,
}: {
  row: RetroAgentRow;
  onClose: () => void;
  onGenerated: () => void;
}): JSX.Element {
  const { t } = useLingui();
  const [phase, setPhase] = useState<Phase>({ kind: 'loading' });
  const agent = row.agent;

  // Fetch the evidence preview on mount.
  useEffect(() => {
    let alive = true;
    setPhase({ kind: 'loading' });
    fetchAgentEvidence(agent)
      .then((evidence) => alive && setPhase({ kind: 'ready', evidence }))
      .catch(
        (e: unknown) =>
          alive && setPhase({ kind: 'error', message: e instanceof Error ? e.message : String(e) }),
      );
    return () => {
      alive = false;
    };
  }, [agent]);

  // Esc closes, except while a generation is in flight.
  const busy = phase.kind === 'generating';
  useEffect(() => {
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape' && !busy) onClose();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [busy, onClose]);

  async function generate(evidence: AgentEvidence): Promise<void> {
    setPhase({ kind: 'generating', evidence });
    try {
      await improveAgent(agent);
      onGenerated();
      onClose();
    } catch (e) {
      setPhase({ kind: 'error', message: e instanceof Error ? e.message : String(e) });
    }
  }

  const evidence = phase.kind === 'ready' || phase.kind === 'generating' ? phase.evidence : null;
  const inRegistry = evidence?.in_registry ?? false;
  const { behavior, harness, infra } = errSplit(row.errors_by_class);

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-bg/70 p-4"
      role="dialog"
      aria-modal="true"
      aria-label={t`Improve ${agent}`}
      onClick={busy ? undefined : onClose}
    >
      <div
        className="flex max-h-[85vh] w-full max-w-lg flex-col rounded-xl border border-line bg-surface px-4 py-4"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="font-display text-[14px] font-bold text-ink">
          <Trans>
            Improve <span className="font-mono">{agent}</span>
          </Trans>
        </div>

        {phase.kind === 'loading' && (
          <div className="mt-3 font-mono text-[11.5px] text-ink-dim">
            <Trans>loading evidence…</Trans>
          </div>
        )}

        {phase.kind === 'error' && (
          <div className="mt-3 rounded-lg border border-red/25 bg-red/5 px-2.5 py-2 font-mono text-[11px] text-red">
            {phase.message}
          </div>
        )}

        {evidence !== null && !inRegistry && (
          <div className="mt-3 font-mono text-[11.5px] text-ink-dim">
            <Trans>Built-in agent — no editable definition file to improve.</Trans>
          </div>
        )}

        {evidence !== null && inRegistry && (
          <div className="mt-3 min-h-0 flex-1 overflow-y-auto">
            {/* Summary block — no extra call, derived from the scorecard row + path. */}
            <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 font-mono text-[11.5px]">
              <dt className="text-ink-faint">
                <Trans>file</Trans>
              </dt>
              <dd className="truncate text-ink-2" data-tip-mono data-tip={evidence.agent_path}>
                {evidence.agent_path ?? '—'}
              </dd>
              <dt className="text-ink-faint">
                <Trans>runs</Trans>
              </dt>
              <dd className="text-ink-2">{row.runs}</dd>
              <dt className="text-ink-faint">
                <Trans>error rate</Trans>
              </dt>
              <dd className="text-ink-2">{(row.error_rate * 100).toFixed(1)}%</dd>
              <dt className="text-ink-faint">
                <Trans>errors</Trans>
              </dt>
              <dd className="text-ink-2">
                <Trans>
                  behavior {behavior} · harness {harness} · infra {infra}
                </Trans>
              </dd>
            </dl>

            {/* Collapsible raw evidence sent to the model. */}
            <details className="mt-3">
              <summary className="cursor-pointer font-mono text-[11px] text-ink-dim select-none hover:text-ink-2">
                <Trans>Show full evidence sent to the model</Trans>
              </summary>
              <pre className="mt-2 max-h-64 overflow-auto rounded-lg border border-line bg-bg/40 px-2.5 py-2 font-mono text-[10.5px] leading-relaxed whitespace-pre-wrap text-ink-2">
                {evidence.bundle ?? ''}
              </pre>
            </details>

            <p className="mt-3 font-mono text-[10.5px] leading-relaxed text-ink-faint">
              <Trans>
                This runs the model on the evidence above and creates a diff proposal you approve
                before any PR.
              </Trans>
            </p>
          </div>
        )}

        <div className="mt-4 flex justify-end gap-2">
          {inRegistry ? (
            <>
              <button
                type="button"
                onClick={onClose}
                disabled={busy}
                className="rounded-lg border border-line bg-surface px-3.5 py-1.5 font-mono text-[11.5px] text-ink-2 transition-colors hover:bg-surface2 disabled:opacity-50"
              >
                <Trans>cancel</Trans>
              </button>
              <button
                type="button"
                onClick={() => {
                  if (evidence !== null) void generate(evidence);
                }}
                disabled={evidence === null || busy}
                className="rounded-lg border border-green/40 bg-green/10 px-3.5 py-1.5 font-mono text-[11.5px] font-semibold text-green transition-colors hover:bg-green/20 disabled:opacity-50"
              >
                {busy ? t`generating…` : t`Generate proposal`}
              </button>
            </>
          ) : (
            <button
              type="button"
              onClick={onClose}
              className="rounded-lg border border-line bg-surface px-3.5 py-1.5 font-mono text-[11.5px] text-ink-2 transition-colors hover:bg-surface2"
            >
              <Trans>close</Trans>
            </button>
          )}
        </div>
      </div>
    </div>
  );
}
