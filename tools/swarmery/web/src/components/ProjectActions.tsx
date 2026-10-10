// Shared project action controls — the detach / archive / restore buttons and
// their confirm dialogs — used by both the Projects list rows and the project
// detail header so the two surfaces behave identically. Also exports the plugin
// state badge and the detach-availability rule.

import type { MessageDescriptor } from '@lingui/core';
import { msg } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { useState } from 'react';
import type { Project } from '../api/types';
import { archiveProject, patchProject, restoreProject } from '../api';
import { ConfirmDialog } from './ui';
import { ExplainPair } from './Explain';
import { DetachModal } from './DetachModal';
import { AttachModal } from './AttachModal';

/** Why the Detach action is unavailable for a project, or null when allowed.
 * A message descriptor: the caller translates it where it renders. */
export function detachBlockReason(project: Project): MessageDescriptor | null {
  const p = project.plugin;
  if (p === null || !p.managed) return msg`plugin is not enabled for this project`;
  if (!p.underOnboardRoot) {
    return msg`project is outside SWARMERY_ONBOARD_ROOTS — detach is fenced to the allow-list`;
  }
  return null;
}

/**
 * Whether the Attach action applies: the project has a .claude/settings.json
 * (plugin state known) but swarmery is not enabled, and the path is inside the
 * onboarding allow-list the write endpoints are fenced to.
 */
export function canAttach(project: Project): boolean {
  const p = project.plugin;
  return p !== null && !p.managed && p.underOnboardRoot;
}

/** managed / not-enabled / telemetry-only pill from the project's plugin state. */
export function PluginBadge({ project }: { project: Project }): JSX.Element {
  const p = project.plugin;
  if (p === null) {
    return (
      <span className="rounded-full border border-line px-2 py-0.5 font-mono text-[10px] whitespace-nowrap text-ink-faint">
        <Trans>telemetry-only</Trans>
      </span>
    );
  }
  if (!p.managed) {
    return (
      <span className="rounded-full border border-line px-2 py-0.5 font-mono text-[10px] whitespace-nowrap text-ink-dim">
        <Trans>not enabled</Trans>
      </span>
    );
  }
  return (
    <span className="rounded-full border border-green/40 bg-green/10 px-2 py-0.5 font-mono text-[10px] whitespace-nowrap text-green">
      <Trans>managed</Trans>
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
  const { t } = useLingui();
  const [value, setValue] = useState(project.tags.join(', '));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const save = (): void => {
    const tags = value
      .split(',')
      .map((tag) => tag.trim().toLowerCase())
      .filter((tag) => tag !== '');
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

  const projectName = project.name ?? project.slug;

  return (
    <div className="absolute top-full right-0 z-20 mt-1.5 w-[260px] rounded-[11px] border border-line-strong bg-field p-3 shadow-[0_16px_34px_rgba(0,0,0,0.5)]">
      <div className="font-mono text-[10px] tracking-[0.1em] text-ink-faint uppercase">
        <Trans>tags · comma-separated</Trans>
      </div>
      <input
        type="text"
        value={value}
        onChange={(e) => setValue(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter') save();
          if (e.key === 'Escape') onClose();
        }}
        placeholder={t`billing, infra`}
        aria-label={t`tags for ${projectName}`}
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
          <Trans>cancel</Trans>
        </button>
        <button
          type="button"
          onClick={save}
          disabled={busy}
          className="rounded-lg border border-brand/40 bg-brand/10 px-2.5 py-1 font-mono text-[10.5px] text-brand transition-colors hover:bg-brand/20 disabled:opacity-50"
        >
          <Trans>save</Trans>
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
  const { t, i18n } = useLingui();
  const [confirm, setConfirm] = useState<'archive' | 'restore' | null>(null);
  const [showDetach, setShowDetach] = useState(false);
  const [showTags, setShowTags] = useState(false);
  const [showAttach, setShowAttach] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const blocked = detachBlockReason(project);
  const projectName = project.name ?? project.slug;

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
          <Trans>restore</Trans>
        </button>
      ) : (
        <>
          <button
            type="button"
            onClick={() => setShowTags((v) => !v)}
            aria-expanded={showTags}
            data-tip={t`edit project tags`}
            className="rounded-lg border border-line bg-surface px-2.5 py-1 font-mono text-[10.5px] text-ink-2 transition-colors hover:bg-surface2"
          >
            <Trans>tags</Trans>
          </button>
          {/* Attach and detach are one concept and one slot — exactly one of the
              two buttons renders — so the explainer is grouped with the pair
              rather than dropped into the row, where it would sit equidistant
              from `archive` and read as belonging to it. */}
          <ExplainPair id="attach-detach">
            {canAttach(project) ? (
              <button
                type="button"
                onClick={() => setShowAttach(true)}
                data-tip={t`re-enable swarmery in .claude/settings.json`}
                className="rounded-lg border border-green/40 bg-green/10 px-2.5 py-1 font-mono text-[10.5px] text-green transition-colors hover:bg-green/20"
              >
                <Trans>attach</Trans>
              </button>
            ) : (
              <button
                type="button"
                onClick={() => setShowDetach(true)}
                disabled={blocked !== null}
                data-tip={blocked !== null ? i18n._(blocked) : t`remove swarmery from .claude/settings.json`}
                className="rounded-lg border border-line bg-surface px-2.5 py-1 font-mono text-[10.5px] text-ink-2 transition-colors hover:bg-surface2 disabled:cursor-not-allowed disabled:opacity-40"
              >
                <Trans>detach</Trans>
              </button>
            )}
          </ExplainPair>
          <button
            type="button"
            onClick={() => setConfirm('archive')}
            data-tip={t`hide from the projects list (reversible)`}
            className="rounded-lg border border-line bg-surface px-2.5 py-1 font-mono text-[10.5px] text-ink-2 transition-colors hover:bg-surface2"
          >
            <Trans>archive</Trans>
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
        title={t`Archive project`}
        confirmLabel={t`archive`}
        busy={busy}
        onCancel={() => setConfirm(null)}
        onConfirm={() => void run(() => archiveProject(project.id))}
      >
        <Trans>
          Hide <span className="font-mono text-ink">{projectName}</span> from the projects list.
          Nothing is deleted — its sessions and transcripts are kept, and you can restore it from
          “show archived”.
        </Trans>
      </ConfirmDialog>

      <ConfirmDialog
        open={confirm === 'restore'}
        title={t`Restore project`}
        confirmLabel={t`restore`}
        busy={busy}
        onCancel={() => setConfirm(null)}
        onConfirm={() => void run(() => restoreProject(project.id))}
      >
        <Trans>
          Bring <span className="font-mono text-ink">{projectName}</span> back into the default
          projects list.
        </Trans>
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

      {showAttach && (
        <AttachModal
          project={project}
          onClose={() => setShowAttach(false)}
          onAttached={() => {
            setShowAttach(false);
            onChanged();
          }}
        />
      )}
    </div>
  );
}
