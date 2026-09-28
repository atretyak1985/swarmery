// Header action: bootstrap a new consumer project from the dashboard. Opens a
// hairline modal (same overlay language as ConfirmDialog) with slug (→
// AGENT_PROJECT), absolute path, workspace root (→ AGENT_WORKSPACE_ROOT) and
// packs. Defaults come from GET /api/projects/onboard/config and show as
// placeholders — leaving a field blank submits the default (slug derives from
// the path's basename; workspace root falls back to the server value). The
// endpoint is fenced to an allow-list server-side; when disabled the form shows
// how to enable it instead of failing on submit.

import { useEffect, useState } from 'react';
import { fetchOnboardConfig, onboardProject } from '../api';
import type { OnboardConfig, OnboardResponse } from '../api/types';
import { ConfirmDialog } from './ui';
import { useDiscardGuard } from './useDiscardGuard';

const PACKS = ['web-pack', 'iot-pack', 'uav-pack', 'infra-pack', 'lsp-pack'] as const;

const SLUG_RE = /^[a-z0-9-]+$/;

/** Kebab-ify a path's last segment into a candidate slug (AGENT_PROJECT). */
function deriveSlug(path: string): string {
  const base = path.replace(/\/+$/, '').split('/').pop() ?? '';
  return base
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '');
}

export function NewProjectButton(): JSX.Element {
  const [open, setOpen] = useState(false);

  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        className="rounded-lg border border-line bg-surface px-2.5 py-1 font-mono text-[11px] font-semibold text-ink-2 transition-colors hover:bg-surface2"
      >
        + new project
      </button>
      {open && <NewProjectModal onClose={() => setOpen(false)} />}
    </>
  );
}

function NewProjectModal({ onClose }: { onClose: () => void }): JSX.Element {
  const [cfg, setCfg] = useState<OnboardConfig | null>(null);
  const [slug, setSlug] = useState('');
  const [path, setPath] = useState('');
  const [workspaceRoot, setWorkspaceRoot] = useState('');
  const [packs, setPacks] = useState<ReadonlySet<string>>(new Set());
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState<OnboardResponse | null>(null);

  useEffect(() => {
    fetchOnboardConfig()
      .then(setCfg)
      .catch(() => setCfg({ enabled: false, workspaceRoot: '', roots: [] }));
  }, []);

  // slug defaults to the path's basename when left blank.
  const effectiveSlug = slug.trim() !== '' ? slug.trim() : deriveSlug(path);
  const slugValid = effectiveSlug !== '' && SLUG_RE.test(effectiveSlug);
  const enabled = cfg?.enabled ?? false;
  const canSubmit = enabled && slugValid && path.trim() !== '' && !busy;

  // Dirty = any field differs from the empty form it opens with. After a
  // successful onboard there is nothing left to lose.
  const dirty =
    done === null && (slug.trim() !== '' || path.trim() !== '' || workspaceRoot.trim() !== '' || packs.size > 0);
  const discard = useDiscardGuard(dirty, onClose, { disabled: busy });
  const { requestClose } = discard;

  useEffect(() => {
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') requestClose();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [requestClose]);

  function togglePack(p: string): void {
    setPacks((prev) => {
      const next = new Set(prev);
      if (next.has(p)) next.delete(p);
      else next.add(p);
      return next;
    });
  }

  async function submit(): Promise<void> {
    setBusy(true);
    setError(null);
    try {
      const res = await onboardProject(effectiveSlug, path.trim(), [...packs], workspaceRoot.trim());
      setDone(res);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-bg/70 p-4"
      role="dialog"
      aria-modal="true"
      aria-label="New project"
      onClick={requestClose}
    >
      <div
        className="w-full max-w-md rounded-xl border border-line bg-surface px-4 py-4"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="font-display text-[14px] font-bold text-ink">Onboard a project</div>

        {done === null ? (
          <>
            <div className="mt-1 text-[12px] leading-relaxed text-ink-dim">
              Writes <span className="font-mono">.claude/</span> config and carves the workspace
              namespace. Idempotent — existing config is never overwritten.
            </div>

            {cfg !== null && !enabled && (
              <div className="mt-3 rounded-lg border border-amber/30 bg-amber/5 px-2.5 py-2 font-mono text-[11px] leading-relaxed text-amber">
                onboarding is disabled — restart the daemon with an allow-list:
                <br />
                <span className="text-ink-2">SWARMERY_ONBOARD_ROOTS=&quot;$HOME/projects&quot;</span>
              </div>
            )}

            <label className="mt-3.5 block font-mono text-[10.5px] tracking-[0.12em] text-ink-dim uppercase">
              slug <span className="text-ink-faint normal-case">→ AGENT_PROJECT</span>
            </label>
            <input
              value={slug}
              onChange={(e) => setSlug(e.target.value)}
              placeholder={deriveSlug(path) !== '' ? deriveSlug(path) : 'my-project'}
              autoFocus
              className="mt-1 w-full rounded-lg border border-line bg-bg px-2.5 py-1.5 font-mono text-[12.5px] text-ink outline-none focus:border-line-strong"
            />
            {slug.trim() === '' && effectiveSlug !== '' && (
              <div className="mt-1 font-mono text-[10.5px] text-ink-faint">
                defaults to <span className="text-ink-dim">{effectiveSlug}</span> (from path)
              </div>
            )}
            {effectiveSlug !== '' && !slugValid && (
              <div className="mt-1 font-mono text-[10.5px] text-red">kebab-case only ([a-z0-9-])</div>
            )}

            <label className="mt-3 block font-mono text-[10.5px] tracking-[0.12em] text-ink-dim uppercase">
              absolute path
            </label>
            <input
              value={path}
              onChange={(e) => setPath(e.target.value)}
              placeholder="/absolute/path/to/project"
              className="mt-1 w-full rounded-lg border border-line bg-bg px-2.5 py-1.5 font-mono text-[12.5px] text-ink outline-none focus:border-line-strong"
            />

            <label className="mt-3 block font-mono text-[10.5px] tracking-[0.12em] text-ink-dim uppercase">
              workspace root <span className="text-ink-faint normal-case">→ AGENT_WORKSPACE_ROOT</span>
            </label>
            <input
              value={workspaceRoot}
              onChange={(e) => setWorkspaceRoot(e.target.value)}
              placeholder="/absolute/path/to/workspace"
              className="mt-1 w-full rounded-lg border border-line bg-bg px-2.5 py-1.5 font-mono text-[12.5px] text-ink outline-none focus:border-line-strong"
            />
            <div className="mt-1 font-mono text-[10.5px] text-ink-faint">
              blank → the daemon&apos;s default workspace root
            </div>

            <label className="mt-3 block font-mono text-[10.5px] tracking-[0.12em] text-ink-dim uppercase">
              packs (core is always on)
            </label>
            <div className="mt-1.5 flex flex-wrap gap-1.5">
              {PACKS.map((p) => {
                const on = packs.has(p);
                return (
                  <button
                    key={p}
                    type="button"
                    onClick={() => togglePack(p)}
                    className={`rounded-full border px-2.5 py-0.5 font-mono text-[11px] transition-colors ${
                      on
                        ? 'border-brand/50 bg-brand/10 text-brand'
                        : 'border-line text-ink-dim hover:bg-surface2'
                    }`}
                  >
                    {p}
                  </button>
                );
              })}
            </div>

            {error !== null && (
              <div className="mt-3 rounded-lg border border-red/25 bg-red/5 px-2.5 py-2 font-mono text-[11px] text-red">
                {error}
              </div>
            )}

            <div className="mt-4 flex justify-end gap-2">
              <button
                type="button"
                onClick={requestClose}
                disabled={busy}
                className="rounded-lg border border-line bg-surface px-3.5 py-1.5 font-mono text-[11.5px] text-ink-2 transition-colors hover:bg-surface2 disabled:opacity-50"
              >
                cancel
              </button>
              <button
                type="button"
                onClick={() => void submit()}
                disabled={!canSubmit}
                className="rounded-lg border border-green/40 bg-green/10 px-3.5 py-1.5 font-mono text-[11.5px] font-semibold text-green transition-colors hover:bg-green/20 disabled:opacity-50"
              >
                {busy ? '…' : 'onboard'}
              </button>
            </div>
          </>
        ) : (
          <>
            <div className="mt-2 space-y-1">
              {done.steps.map((s, i) => (
                <div key={i} className="font-mono text-[11.5px] text-ink-2">
                  {s}
                </div>
              ))}
            </div>
            <div className="mt-3 text-[12px] leading-relaxed text-ink-dim">
              Next: open a fresh Claude Code session in{' '}
              <span className="font-mono text-ink-2">{done.path}</span> and accept the{' '}
              <span className="font-mono">swarmery</span> trust prompt. It appears here once its
              first session runs.
            </div>
            <div className="mt-4 flex justify-end">
              <button
                type="button"
                onClick={onClose}
                className="rounded-lg border border-line bg-surface px-3.5 py-1.5 font-mono text-[11.5px] text-ink-2 transition-colors hover:bg-surface2"
              >
                done
              </button>
            </div>
          </>
        )}
      </div>

      <ConfirmDialog
        {...discard.confirmProps}
        title="Discard new project?"
        confirmLabel="discard"
        danger
      >
        The slug, path, workspace root, and pack selections you&apos;ve entered will be lost.
      </ConfirmDialog>
    </div>
  );
}
