// Attach confirmation modal — the mirror of DetachModal. On open it calls the
// server DRY RUN to preview exactly what would be restored (merged settings
// entries, project.json from its .bak, statusline, hooks), then "Confirm
// attach" performs the real write and shows the applied steps. Warning lines
// ("! …") flag foreign values the merge refused to overwrite.

import { Trans, useLingui } from '@lingui/react/macro';
import { useEffect, useState } from 'react';
import type { AttachResponse, Project } from '../api/types';
import { attachProject } from '../api';

type Phase =
  | { kind: 'loading' }
  | { kind: 'plan'; plan: AttachResponse }
  | { kind: 'applying'; plan: AttachResponse }
  | { kind: 'done'; result: AttachResponse }
  | { kind: 'error'; message: string };

export function AttachModal({
  project,
  onClose,
  onAttached,
}: {
  project: Project;
  onClose: () => void;
  onAttached: () => void;
}): JSX.Element {
  const { t } = useLingui();
  const [phase, setPhase] = useState<Phase>({ kind: 'loading' });
  const projectName = project.name ?? project.slug;

  // Dry run on mount → the restore plan.
  useEffect(() => {
    let alive = true;
    setPhase({ kind: 'loading' });
    attachProject(project.id, true)
      .then((plan) => alive && setPhase({ kind: 'plan', plan }))
      .catch((e: unknown) =>
        alive && setPhase({ kind: 'error', message: e instanceof Error ? e.message : String(e) }),
      );
    return () => {
      alive = false;
    };
  }, [project.id]);

  async function apply(plan: AttachResponse): Promise<void> {
    setPhase({ kind: 'applying', plan });
    try {
      const result = await attachProject(project.id, false);
      setPhase({ kind: 'done', result });
    } catch (e) {
      setPhase({ kind: 'error', message: e instanceof Error ? e.message : String(e) });
    }
  }

  const nothingToDo =
    (phase.kind === 'plan' || phase.kind === 'applying') && !phase.plan.attached;
  const busy = phase.kind === 'applying';

  const backupPath = phase.kind === 'done' ? phase.result.backup : undefined;
  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-bg/70 p-4"
      role="dialog"
      aria-modal="true"
      aria-label={t`Attach project`}
      onClick={busy ? undefined : onClose}
    >
      <div
        className="w-full max-w-md rounded-xl border border-line bg-surface px-4 py-4"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="font-display text-[14px] font-bold text-ink">
          <Trans>
            Attach <span className="font-mono">{projectName}</span>
          </Trans>
        </div>
        <div className="mt-1 text-[12px] leading-relaxed text-ink-dim">
          <Trans>
            Merges the swarmery entries back into <span className="font-mono">.claude/settings.json</span>{' '}
            (your other settings are never overwritten), restores{' '}
            <span className="font-mono">project.json</span> from its backup, and reinstalls the hooks.
            Plugins install on the next fresh Claude Code session in the project.
          </Trans>
        </div>

        {phase.kind === 'loading' && (
          <div className="mt-3 font-mono text-[11.5px] text-ink-dim">
            <Trans>computing plan…</Trans>
          </div>
        )}

        {(phase.kind === 'plan' || phase.kind === 'applying') && (
          <StepList
            title={nothingToDo ? t`nothing to restore` : t`will restore`}
            steps={phase.plan.steps}
          />
        )}

        {phase.kind === 'done' && (
          <>
            <StepList title={t`restored`} steps={phase.result.steps} />
            {backupPath !== undefined && (
              <div className="mt-2 font-mono text-[10.5px] text-ink-faint">
                <Trans>backup: {backupPath}</Trans>
              </div>
            )}
          </>
        )}

        {phase.kind === 'error' && (
          <div className="mt-3 rounded-lg border border-red/25 bg-red/5 px-2.5 py-2 font-mono text-[11px] text-red">
            {phase.message}
          </div>
        )}

        <div className="mt-4 flex justify-end gap-2">
          {phase.kind === 'done' || phase.kind === 'error' ? (
            <button
              type="button"
              onClick={phase.kind === 'done' ? onAttached : onClose}
              className="rounded-lg border border-line bg-surface px-3.5 py-1.5 font-mono text-[11.5px] text-ink-2 transition-colors hover:bg-surface2"
            >
              {phase.kind === 'done' ? t`done` : t`close`}
            </button>
          ) : (
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
                  if (phase.kind === 'plan') void apply(phase.plan);
                }}
                disabled={phase.kind !== 'plan' || nothingToDo}
                className="rounded-lg border border-green/40 bg-green/10 px-3.5 py-1.5 font-mono text-[11.5px] font-semibold text-green transition-colors hover:bg-green/20 disabled:opacity-50"
              >
                {busy ? '…' : t`confirm attach`}
              </button>
            </>
          )}
        </div>
      </div>
    </div>
  );
}

function StepList({ title, steps }: { title: string; steps: string[] }): JSX.Element {
  return (
    <div className="mt-3">
      <div className="font-mono text-[10px] tracking-[0.12em] text-ink-faint uppercase">{title}</div>
      <div className="mt-1.5 space-y-0.5">
        {steps.map((s, i) => (
          <div
            key={i}
            className={`font-mono text-[11.5px] ${s.startsWith('! ') ? 'text-amber' : 'text-ink-2'}`}
          >
            {s}
          </div>
        ))}
      </div>
    </div>
  );
}
