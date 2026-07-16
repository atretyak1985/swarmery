// Shared project action controls — the detach / archive / restore buttons and
// their confirm dialogs — used by both the Projects list rows and the project
// detail header so the two surfaces behave identically. Also exports the plugin
// state badge and the detach-availability rule.

import { useState } from 'react';
import type { Project } from '../api/types';
import { archiveProject, patchProject, restoreProject } from '../api';
import { ConfirmDialog } from './ui';
import { DetachModal } from './DetachModal';

/** Why the Detach action is unavailable for a project, or null when allowed. */
export function detachBlockReason(project: Project): string | null {
  const p = project.plugin;
  if (p === null || !p.managed) return 'plugin is not enabled for this project';
  if (!p.underOnboardRoot) {
    return 'project is outside SWARMERY_ONBOARD_ROOTS — detach is fenced to the allow-list';
  }
  return null;
}

/** managed / not-enabled / telemetry-only pill from the project's plugin state. */
export function PluginBadge({ project }: { project: Project }): JSX.Element {
  const p = project.plugin;
  if (p === null) {
    return (
      <span className="rounded-full border border-line px-2 py-0.5 font-mono text-[10px] whitespace-nowrap text-ink-faint">
        telemetry-only
      </span>
    );
  }
  if (!p.managed) {
    return (
      <span className="rounded-full border border-line px-2 py-0.5 font-mono text-[10px] whitespace-nowrap text-ink-dim">
        not enabled
      </span>
    );
  }
  return (
    <span className="rounded-full border border-green/40 bg-green/10 px-2 py-0.5 font-mono text-[10px] whitespace-nowrap text-green">
      managed
    </span>
  );
}

/** Inline tag editor — comma-separated input persisted via PATCH {tags}. */
function TagEditor({
  project,
  onChanged,
  onClose,
}: {
  project: Project;
  onChanged: () => void;
  onClose: () => void;
}): JSX.Element {
  const [value, setValue] = useState(project.tags.join(', '));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const save = (): void => {
    const tags = value
      .split(',')
      .map((t) => t.trim().toLowerCase())
      .filter((t) => t !== '');
    setBusy(true);
    setError(null);
    patchProject(project.id, { tags })
      .then(() => {
        onChanged();
        onClose();
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setBusy(false));
  };

  return (
    <div className="absolute top-full right-0 z-20 mt-1.5 w-[260px] rounded-[11px] border border-line-strong bg-field p-3 shadow-[0_16px_34px_rgba(0,0,0,0.5)]">
      <div className="font-mono text-[10px] tracking-[0.1em] text-ink-faint uppercase">
        tags · comma-separated
      </div>
      <input
        type="text"
        value={value}
        onChange={(e) => setValue(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter') save();
          if (e.key === 'Escape') onClose();
        }}
        placeholder="billing, infra"
        aria-label={`tags for ${project.slug}`}
        autoFocus
        className="mt-2 w-full rounded-[9px] border border-line-strong bg-surface px-2.5 py-[6px] font-mono text-[11.5px] text-ink transition-colors outline-none placeholder:text-ink-faint focus:border-ink-dim"
      />
      {error !== null && <div className="mt-1.5 font-mono text-[10px] text-red">{error}</div>}
      <div className="mt-2.5 flex justify-end gap-2">
        <button
          type="button"
          onClick={onClose}
          className="rounded-lg border border-line bg-surface px-2.5 py-1 font-mono text-[10.5px] text-ink-2 transition-colors hover:bg-surface2"
        >
          cancel
        </button>
        <button
          type="button"
          onClick={save}
          disabled={busy}
          className="rounded-lg border border-brand/40 bg-brand/10 px-2.5 py-1 font-mono text-[10.5px] text-brand transition-colors hover:bg-brand/20 disabled:opacity-50"
        >
          save
        </button>
      </div>
    </div>
  );
}

export function ProjectActions({
  project,
  onChanged,
}: {
  project: Project;
  /** Called after a successful archive / restore / detach so the caller reloads. */
  onChanged: () => void;
}): JSX.Element {
  const [confirm, setConfirm] = useState<'archive' | 'restore' | null>(null);
  const [showDetach, setShowDetach] = useState(false);
  const [showTags, setShowTags] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const blocked = detachBlockReason(project);

  async function run(fn: () => Promise<void>): Promise<void> {
    setBusy(true);
    setError(null);
    try {
      await fn();
      onChanged();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
      setConfirm(null);
    }
  }

  return (
    <div className="relative flex items-center gap-2">
      {error !== null && <span className="font-mono text-[10px] text-red">{error}</span>}

      {project.archived ? (
        <button
          type="button"
          onClick={() => setConfirm('restore')}
          className="rounded-lg border border-line bg-surface px-2.5 py-1 font-mono text-[10.5px] text-ink-2 transition-colors hover:bg-surface2"
        >
          restore
        </button>
      ) : (
        <>
          <button
            type="button"
            onClick={() => setShowTags((v) => !v)}
            aria-expanded={showTags}
            title="edit project tags"
            className="rounded-lg border border-line bg-surface px-2.5 py-1 font-mono text-[10.5px] text-ink-2 transition-colors hover:bg-surface2"
          >
            tags
          </button>
          <button
            type="button"
            onClick={() => setShowDetach(true)}
            disabled={blocked !== null}
            title={blocked ?? 'remove swarmery from .claude/settings.json'}
            className="rounded-lg border border-line bg-surface px-2.5 py-1 font-mono text-[10.5px] text-ink-2 transition-colors hover:bg-surface2 disabled:cursor-not-allowed disabled:opacity-40"
          >
            detach
          </button>
          <button
            type="button"
            onClick={() => setConfirm('archive')}
            title="hide from the projects list (reversible)"
            className="rounded-lg border border-line bg-surface px-2.5 py-1 font-mono text-[10.5px] text-ink-2 transition-colors hover:bg-surface2"
          >
            archive
          </button>
        </>
      )}

      {showTags && (
        <TagEditor
          project={project}
          onChanged={onChanged}
          onClose={() => setShowTags(false)}
        />
      )}

      <ConfirmDialog
        open={confirm === 'archive'}
        title="Archive project"
        confirmLabel="archive"
        busy={busy}
        onCancel={() => setConfirm(null)}
        onConfirm={() => void run(() => archiveProject(project.id))}
      >
        Hide <span className="font-mono text-ink">{project.slug}</span> from the projects list.
        Nothing is deleted — its sessions and transcripts are kept, and you can restore it from
        “show archived”.
      </ConfirmDialog>

      <ConfirmDialog
        open={confirm === 'restore'}
        title="Restore project"
        confirmLabel="restore"
        busy={busy}
        onCancel={() => setConfirm(null)}
        onConfirm={() => void run(() => restoreProject(project.id))}
      >
        Bring <span className="font-mono text-ink">{project.slug}</span> back into the default
        projects list.
      </ConfirmDialog>

      {showDetach && (
        <DetachModal
          project={project}
          onClose={() => setShowDetach(false)}
          onDetached={() => {
            setShowDetach(false);
            onChanged();
          }}
        />
      )}
    </div>
  );
}
