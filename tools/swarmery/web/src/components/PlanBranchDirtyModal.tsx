// The plan-level branch-dirty affordance: what a "Run plan" that refused to
// start actually means, and the two real ways out of it.
//
// A plan run commits to the deterministic branch swarm/plan-<taskId>. Every
// teardown keeps that branch, so a plan whose previous run produced commits
// cannot simply reuse the name — reclaiming it would `git branch -D` work
// nothing else holds. The daemon refuses (409) and names the branch, the commit
// count and the base the count was measured against.
//
// Why a sibling of RunOutcomeModal rather than a prop on it: that modal is a
// PHASE diagnosis, built around GET /phases/{id}/diagnosis. A plan has no
// diagnosis endpoint, so reusing it would mean making its only data source
// optional and every section conditional. This modal has one input — the
// structured 409 — and no fetch on open.
//
// Two things it deliberately does NOT do:
//   • run git. "Keep the work" prints the exact commands and stops. A merge is a
//     decision with a working tree behind it; a browser button that guesses the
//     target branch and merges into it is how you lose an afternoon.
//   • claim the count is safe to ignore. `base` is shown because the SAME branch
//     is "2 commits ahead" of dev and "0 ahead" of a branch that already carries
//     them, and only the user knows which reading applies.

import { plural } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { useEffect, useState } from 'react';
import { deletePlanRunBranch } from '../api';

/** The structured 409 from POST /api/epics/{taskId}/run, already parsed. */
export type PlanBranchDirty = {
  branch: string;
  /** Commits on `branch` that `base` does not have. 0 ⇒ the daemon could not count. */
  commitsAhead: number;
  /** The branch the count was measured against; empty when it could not be named. */
  base: string;
  /** The daemon's own sentence, kept verbatim as the fallback headline. */
  message: string;
};

function CommandRow({ cmd }: { cmd: string }): JSX.Element {
  const { t } = useLingui();
  const [copied, setCopied] = useState(false);
  return (
    <div className="flex items-start gap-2 rounded-lg border border-line bg-bg/40 px-2.5 py-2">
      <code className="min-w-0 flex-1 font-mono text-[10.5px] break-all text-ink-2">{cmd}</code>
      <button
        type="button"
        aria-label={t`copy: ${cmd}`}
        onClick={() => {
          // navigator.clipboard is undefined on non-secure origins (plain-HTTP
          // LAN) — optional-chain to a no-op instead of throwing; the command
          // stays visible and selectable either way.
          void navigator.clipboard
            ?.writeText(cmd)
            .then(() => {
              setCopied(true);
              setTimeout(() => setCopied(false), 1500);
            })
            .catch(() => {});
        }}
        className="shrink-0 rounded border border-line-strong px-2 py-0.5 font-mono text-[10px] text-ink-dim transition-colors hover:text-ink"
      >
        {copied ? t`copied` : t`copy`}
      </button>
    </div>
  );
}

export function PlanBranchDirtyModal({
  taskId,
  dirty,
  onClose,
  onRetry,
}: {
  taskId: number;
  dirty: PlanBranchDirty;
  onClose: () => void;
  /** Fires the caller's own plan-run handler (Plans.tsx owns the busy state). */
  onRetry: () => void;
}): JSX.Element {
  const { t } = useLingui();
  const [busy, setBusy] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [actionErr, setActionErr] = useState<string | null>(null);
  const [discarded, setDiscarded] = useState(false);

  // Esc backs out one level at a time: an ARMED discard disarms, otherwise the
  // modal closes. Closing on the armed step would leave the user unsure whether
  // the branch survived.
  useEffect(() => {
    const onKey = (e: KeyboardEvent): void => {
      if (e.key !== 'Escape' || busy) return;
      if (confirming) {
        setConfirming(false);
        return;
      }
      onClose();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [busy, confirming, onClose]);

  const commits = dirty.commitsAhead;
  const branch = dirty.branch;
  const commitsText = plural(commits, {
    one: '# commit',
    few: '# commits',
    many: '# commits',
    other: '# commits',
  });
  // No base ⇒ say so rather than print a range against nothing. `git log <branch>`
  // still shows the branch's history, which is the honest degraded answer.
  const base = dirty.base === '' ? null : dirty.base;
  const logCmd =
    base === null
      ? `git log --oneline ${dirty.branch}` // i18n-ignore — a shell command
      : `git log --oneline ${base}..${dirty.branch}`; // i18n-ignore — a shell command
  const mergeCmd = `git merge --no-ff ${dirty.branch}`; // i18n-ignore — a shell command

  function discard(): void {
    setBusy(true);
    setConfirming(false);
    setActionErr(null);
    deletePlanRunBranch(taskId)
      .then(() => setDiscarded(true))
      .catch((e: unknown) => setActionErr(e instanceof Error ? e.message : String(e)))
      .finally(() => setBusy(false));
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-bg/70 p-4"
      role="dialog"
      aria-modal="true"
      aria-label={t`Run branch holds unmerged commits`}
      onClick={busy ? undefined : onClose}
    >
      <div
        className="flex max-h-[85vh] w-full max-w-lg flex-col rounded-xl border border-line bg-surface px-4 py-4"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="min-w-0">
          <div className="font-mono text-[10px] tracking-[0.16em] text-amber uppercase">
            <Trans>run not started</Trans>
          </div>
          <div className="font-display mt-0.5 text-[14px] font-bold text-ink">
            <Trans>The run branch still holds work</Trans>
          </div>
        </div>

        <div className="mt-3 min-h-0 flex-1 space-y-4 overflow-y-auto">
          {/* The one sentence: which branch, how many commits, ahead of what. */}
          <p className="text-[12px] leading-relaxed text-ink-2">
            {base !== null ? (
              <Trans>
                <code className="font-mono text-[11.5px] text-ink">{branch}</code> has{' '}
                <strong className="text-ink">{commitsText}</strong> that{' '}
                <code className="font-mono text-[11.5px] text-ink">{base}</code> does not have. Starting a
                new run would reuse that branch name, so the daemon refused rather than destroy them.
              </Trans>
            ) : (
              <Trans>
                <code className="font-mono text-[11.5px] text-ink">{branch}</code> has{' '}
                <strong className="text-ink">{commitsText}</strong> that could not be measured against a
                base branch. Starting a new run would reuse that branch name, so the daemon refused
                rather than destroy them.
              </Trans>
            )}
          </p>

          {discarded ? (
            <div className="rounded-lg border border-green/25 bg-green/5 px-2.5 py-2 font-mono text-[11px] text-green">
              <Trans>{branch} deleted — Run plan again to start a fresh run.</Trans>
            </div>
          ) : (
            <>
              <div>
                <div className="mb-1.5 font-mono text-[10px] tracking-[0.16em] text-ink-faint uppercase">
                  <Trans>keep the work</Trans>
                </div>
                <p className="mb-2 text-[11.5px] leading-relaxed text-ink-dim">
                  {base !== null ? (
                    <Trans>
                      Review the commits, then merge them into {base} in your own checkout. Once
                      merged, the branch is empty and the next run reclaims and deletes it
                      automatically — nothing else to clean up.
                    </Trans>
                  ) : (
                    <Trans>
                      Review the commits, then merge them in your own checkout. Once merged, the
                      branch is empty and the next run reclaims and deletes it automatically —
                      nothing else to clean up.
                    </Trans>
                  )}
                </p>
                <div className="space-y-1.5">
                  <CommandRow cmd={logCmd} />
                  <CommandRow cmd={mergeCmd} />
                </div>
              </div>

              <div>
                <div className="mb-1.5 font-mono text-[10px] tracking-[0.16em] text-ink-faint uppercase">
                  <Trans>or discard it</Trans>
                </div>
                <p className="text-[11.5px] leading-relaxed text-ink-dim">
                  <Trans>
                    If the run produced nothing worth keeping, delete the branch. This runs{' '}
                    <code className="font-mono text-[10.5px]">git branch -D</code>: the {commitsText}{' '}
                    go, and nothing else holds them.
                  </Trans>
                </p>
              </div>
            </>
          )}

          {actionErr !== null && (
            <div
              role="alert"
              className="rounded-lg border border-red/25 bg-red/5 px-2.5 py-2 font-mono text-[11px] text-red"
            >
              {actionErr}
            </div>
          )}
        </div>

        <div className="mt-4 flex flex-wrap items-center justify-end gap-2">
          {/* Two-step confirm, in-modal (never window.confirm — a browser modal
              blocks the extension-driven flows this repo uses). The armed step
              names the branch and the exact number of commits it destroys. */}
          {!discarded &&
            (confirming ? (
              <span className="flex flex-wrap items-center justify-end gap-2">
                <span className="font-mono text-[10.5px] text-ink-dim">
                  <Trans>
                    Delete {branch}? {commitsText} will be lost.
                  </Trans>
                </span>
                <button
                  type="button"
                  disabled={busy}
                  onClick={discard}
                  className="rounded-lg border border-red/40 bg-red/10 px-3.5 py-1.5 font-mono text-[11.5px] font-semibold text-red transition-colors hover:bg-red/20 disabled:cursor-not-allowed disabled:opacity-50"
                >
                  {busy ? t`deleting…` : t`Delete permanently`}
                </button>
                <button
                  type="button"
                  disabled={busy}
                  onClick={() => setConfirming(false)}
                  className="font-mono text-[11.5px] text-ink-dim transition-colors hover:text-ink disabled:opacity-50"
                >
                  <Trans>Cancel</Trans>
                </button>
              </span>
            ) : (
              <button
                type="button"
                disabled={busy}
                data-tip={t`delete the run branch and the commits on it`}
                onClick={() => setConfirming(true)}
                className="rounded-lg border border-red/40 bg-red/5 px-3.5 py-1.5 font-mono text-[11.5px] text-red transition-colors hover:bg-red/10 disabled:cursor-not-allowed disabled:opacity-50"
              >
                <Trans>Discard branch</Trans>
              </button>
            ))}
          {discarded && (
            <button
              type="button"
              disabled={busy}
              data-tip={t`start the plan run again`}
              onClick={() => {
                onRetry();
                onClose();
              }}
              className="rounded-lg border border-brand/40 bg-brand/10 px-3.5 py-1.5 font-mono text-[11.5px] font-semibold text-brand transition-colors hover:bg-brand/20 disabled:cursor-not-allowed disabled:opacity-50"
            >
              <Trans>Run plan again</Trans>
            </button>
          )}
          <button
            type="button"
            onClick={onClose}
            disabled={busy}
            className="rounded-lg border border-line bg-surface px-3.5 py-1.5 font-mono text-[11.5px] text-ink-2 transition-colors hover:bg-surface2 disabled:opacity-50"
          >
            <Trans>Close</Trans>
          </button>
        </div>
      </div>
    </div>
  );
}
