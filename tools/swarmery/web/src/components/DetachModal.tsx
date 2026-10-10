// Detach confirmation modal (same overlay language as NewProjectModal /
// ConfirmDialog). On open it calls the server DRY RUN to preview the exact set
// of swarmery-owned entries that would be removed from the project's
// .claude/settings.json, so the destructive write is never a surprise. The
// "Confirm detach" button then performs the real write; on success it shows the
// applied steps and the backup path.

import { Trans, useLingui } from '@lingui/react/macro';
import { useEffect, useState } from 'react';
import type { DetachResponse, Project } from '../api/types';
import { detachProject } from '../api';

type Phase =
  | { kind: 'loading' }
  | { kind: 'plan'; plan: DetachResponse }
  | { kind: 'applying'; plan: DetachResponse }
  | { kind: 'done'; result: DetachResponse }
  | { kind: 'error'; message: string };

export function DetachModal({
  project,
  onClose,
  onDetached,
}: {
  project: Project;
  onClose: () => void;
  onDetached: () => void;
}): JSX.Element {
  const { t } = useLingui();
  const [phase, setPhase] = useState<Phase>({ kind: 'loading' });
  const [full, setFull] = useState(true);

  // Dry run on mount (and on full-mode toggle) → the removal plan.
  useEffect(() => {
    let alive = true;
    setPhase({ kind: 'loading' });
    detachProject(project.id, true, full)
      .then((plan) => alive && setPhase({ kind: 'plan', plan }))
      .catch((e: unknown) =>
        alive && setPhase({ kind: 'error', message: e instanceof Error ? e.message : String(e) }),
      );
    return () => {
      alive = false;
    };
  }, [project.id, full]);

  async function apply(plan: DetachResponse): Promise<void> {
    setPhase({ kind: 'applying', plan });
    try {
      const result = await detachProject(project.id, false, full);
      setPhase({ kind: 'done', result });
    } catch (e) {
      setPhase({ kind: 'error', message: e instanceof Error ? e.message : String(e) });
    }
  }

  const nothingToDo =
    (phase.kind === 'plan' || phase.kind === 'applying') && !phase.plan.detached;
  const busy = phase.kind === 'applying';
  const projectName = project.name ?? project.slug;
  const backupPath = phase.kind === 'done' ? phase.result.backup : undefined;

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-bg/70 p-4"
      role="dialog"
      aria-modal="true"
      aria-label={t`Detach project`}
      onClick={busy ? undefined : onClose}
    >
      <div
        className="w-full max-w-md rounded-xl border border-line bg-surface px-4 py-4"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="font-display text-[14px] font-bold text-ink">
          <Trans>
            Detach <span className="font-mono">{projectName}</span>
          </Trans>
        </div>
        <div className="mt-1 text-[12px] leading-relaxed text-ink-dim">
          <Trans>
            Removes the swarmery-owned entries from{' '}
            <span className="font-mono">.claude/settings.json</span>. Your other settings are left
            untouched and the original is backed up to{' '}
            <span className="font-mono">settings.json.bak</span>.
          </Trans>
        </div>

        <label className="mt-2.5 flex cursor-pointer items-start gap-2 text-[12px] text-ink-2">
          <input
            type="checkbox"
            checked={full}
            disabled={busy}
            onChange={(e) => setFull(e.target.checked)}
            className="mt-0.5 accent-brand"
          />
          <span>
            <Trans>
              full offboard — also remove <span className="font-mono">project.json</span> and the
              statusline scripts (local agents/skills are never touched)
            </Trans>
          </span>
        </label>

        {phase.kind === 'loading' && (
          <div className="mt-3 font-mono text-[11.5px] text-ink-dim">
            <Trans>computing plan…</Trans>
          </div>
        )}

        {(phase.kind === 'plan' || phase.kind === 'applying') && (
          <StepList
            title={nothingToDo ? t`nothing to remove` : t`will remove`}
            steps={phase.plan.steps}
          />
        )}

        {phase.kind === 'done' && (
          <>
            <StepList title={t`removed`} steps={phase.result.steps} />
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
              onClick={phase.kind === 'done' ? onDetached : onClose}
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
                className="rounded-lg border border-red/40 bg-red/10 px-3.5 py-1.5 font-mono text-[11.5px] font-semibold text-red transition-colors hover:bg-red/20 disabled:opacity-50"
              >
                {busy ? '…' : t`confirm detach`}
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
          <div key={i} className="font-mono text-[11.5px] text-ink-2">
            {s}
          </div>
        ))}
      </div>
    </div>
  );
}
