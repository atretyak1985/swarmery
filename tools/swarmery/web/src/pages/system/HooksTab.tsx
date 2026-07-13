// Hooks tab (System screen): settings-file hook entries grouped by event,
// list order = registration order (seq). Commands arrive already redacted by
// the backend. The hook_no_timeout lint (step-04) surfaces as an amber "no
// timeout" flag. Stage 2 (step-12) adds the write surface: enable/disable
// toggle behind a confirmation modal (the command stops executing in Claude
// Code) and inline command/timeout editing — both PUT/POST with the row
// contentHash as base_hash (step-10 contract); managed=swarmery entries stay
// read-only (installer-owned), and a stale hash is resolved ONLY by refetch.

import { useCallback, useState } from 'react';
import type { LintSeverity, SystemHook } from '../../api/types';
import {
  fetchSystemHooks,
  toggleSystemHook,
  updateSystemHook,
  SystemWriteError,
  type SystemListFilters,
} from '../../api/system';
import { ConfirmDialog, Empty, ErrorBox, GroupHeader, Loading } from '../../components/ui';
import { FiltersRow, ScopeBadge, useSystemList } from './shared';

/** Claude Code hook lifecycle order — unknown events sort last, by name. */
const EVENT_ORDER = [
  'SessionStart',
  'UserPromptSubmit',
  'PreToolUse',
  'PostToolUse',
  'Notification',
  'PermissionRequest',
  'Stop',
  'SubagentStop',
  'PreCompact',
  'SessionEnd',
];

function eventRank(event: string): number {
  const idx = EVENT_ORDER.indexOf(event);
  return idx === -1 ? EVENT_ORDER.length : idx;
}

function groupByEvent(hooks: SystemHook[]): [string, SystemHook[]][] {
  const groups = new Map<string, SystemHook[]>();
  for (const hook of hooks) {
    const list = groups.get(hook.event);
    if (list !== undefined) list.push(hook);
    else groups.set(hook.event, [hook]);
  }
  for (const list of groups.values()) {
    list.sort((a, b) => (a.scope === b.scope ? a.seq - b.seq : a.scope === 'global' ? -1 : 1));
  }
  return [...groups.entries()].sort(
    ([a], [b]) => eventRank(a) - eventRank(b) || a.localeCompare(b),
  );
}

function HookRow({
  hook,
  onMutated,
  onReadonly,
}: {
  hook: SystemHook;
  /** A write landed or the row went stale — refetch the list. */
  onMutated: () => void;
  /** A write hit the global readonly kill-switch — page-level banner. */
  onReadonly: () => void;
}): JSX.Element {
  const [confirmToggle, setConfirmToggle] = useState(false);
  const [editing, setEditing] = useState(false);
  const [cmdDraft, setCmdDraft] = useState('');
  const [timeoutDraft, setTimeoutDraft] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const managed = hook.managed === 'swarmery';
  // step-10 contract: every hook write carries the row contentHash as
  // base_hash; while the GET handler does not serve it, writes stay disabled.
  const writable = !managed && hook.contentHash !== undefined;

  const fail = (e: unknown): void => {
    if (e instanceof SystemWriteError) {
      if (e.forbidden === 'readonly') onReadonly();
      if (e.conflict !== null) {
        // Stale base_hash: NO retry with the old hash, ever — refetch brings
        // the fresh row (and hash); the user re-applies the change on top.
        setError('the entry changed outside the dashboard — the list was refreshed, re-apply your change');
        onMutated();
        return;
      }
      setError(e.message);
      return;
    }
    setError(String(e));
  };

  const doToggle = (): void => {
    setBusy(true);
    setError(null);
    toggleSystemHook(hook.id, !hook.enabled, hook.contentHash ?? '')
      .then(() => {
        setConfirmToggle(false);
        onMutated();
      })
      .catch((e: unknown) => {
        setConfirmToggle(false);
        fail(e);
      })
      .finally(() => setBusy(false));
  };

  const openEdit = (): void => {
    setCmdDraft(hook.command);
    setTimeoutDraft(hook.timeout === null ? '' : String(hook.timeout));
    setError(null);
    setEditing(true);
  };

  const timeoutValue = timeoutDraft.trim() === '' ? null : Number(timeoutDraft.trim());
  const timeoutInvalid =
    timeoutValue !== null && (!Number.isInteger(timeoutValue) || timeoutValue <= 0);

  const saveEdit = (): void => {
    if (timeoutInvalid || cmdDraft.trim() === '') return;
    setBusy(true);
    setError(null);
    updateSystemHook(hook.id, cmdDraft, timeoutValue, hook.contentHash ?? '')
      .then(() => {
        setEditing(false);
        onMutated();
      })
      .catch((e: unknown) => {
        // On a 409 the draft is stale by definition — close so it cannot be
        // re-PUT against the old hash; other failures keep the form open.
        if (e instanceof SystemWriteError && e.conflict !== null) setEditing(false);
        fail(e);
      })
      .finally(() => setBusy(false));
  };

  return (
    <div className="border-b border-line-soft px-3.5 py-2.5 last:border-b-0">
      <div className={hook.enabled || editing ? '' : 'opacity-45'}>
        <div className="flex flex-wrap items-center gap-1.5">
          <button
            type="button"
            role="switch"
            aria-checked={hook.enabled}
            aria-label={`${hook.enabled ? 'disable' : 'enable'} hook ${hook.event} ${hook.matcher ?? '*'}`}
            disabled={!writable || busy}
            title={
              managed
                ? 'swarmery installer hook — manage it via `swarmery hooks`'
                : writable
                  ? hook.enabled
                    ? 'disable this hook'
                    : 'enable this hook'
                  : 'hook writes need the row content_hash — not served by the daemon yet'
            }
            onClick={() => setConfirmToggle(true)}
            className={`relative h-[18px] w-[34px] shrink-0 rounded-full border transition-colors ${
              hook.enabled ? 'border-green/50 bg-green/25' : 'border-line bg-surface2'
            } ${writable ? '' : 'cursor-not-allowed opacity-40'}`}
          >
            <span
              className={`absolute top-[2px] h-[12px] w-[12px] rounded-full transition-all ${
                hook.enabled ? 'left-[18px] bg-green' : 'left-[3px] bg-ink-dim'
              }`}
            />
          </button>
          <span className="font-mono text-[11.5px] text-ink">{hook.matcher ?? '*'}</span>
          <span className="ml-auto flex flex-wrap items-center gap-1.5">
            <ScopeBadge scope={hook.scope} projectSlug={hook.projectSlug} />
            {managed && (
              <span
                className="rounded-full border border-brand/40 px-2 py-px font-mono text-[10px] whitespace-nowrap text-brand"
                title="installer-owned entry — managed by swarmery"
              >
                managed · swarmery
              </span>
            )}
            {hook.timeout === null && (
              <span
                className="rounded-full border border-amber/45 px-2 py-px font-mono text-[10px] whitespace-nowrap text-amber"
                title="lint hook_no_timeout: no timeout set — a hung command blocks the session"
              >
                ▲ no timeout
              </span>
            )}
            {!hook.enabled && (
              <span className="rounded-full border border-line px-2 py-px font-mono text-[10px] whitespace-nowrap text-ink-dim">
                disabled
              </span>
            )}
            {writable && !editing && (
              <button
                type="button"
                onClick={openEdit}
                className="rounded-full border border-line px-2 py-px font-mono text-[10px] whitespace-nowrap text-ink-dim transition-colors hover:border-brand/50 hover:text-brand"
              >
                edit
              </button>
            )}
          </span>
        </div>
        {editing ? (
          <div className="mt-1 rounded-lg border border-line bg-bg px-2.5 py-2">
            {cmdDraft.includes('•••') && (
              <div className="mb-1.5 font-mono text-[10.5px] text-amber">
                ▲ the command contains redacted values (•••) — saving writes the text exactly as
                shown
              </div>
            )}
            <textarea
              value={cmdDraft}
              onChange={(e) => setCmdDraft(e.target.value)}
              rows={2}
              spellCheck={false}
              aria-label="hook command"
              className="w-full resize-y rounded-md border border-line bg-surface px-2.5 py-1.5 font-mono text-[11px] text-ink-2 focus:border-brand focus:outline-none"
            />
            <div className="mt-1.5 flex flex-wrap items-center gap-2">
              <label
                className="font-mono text-[10.5px] text-ink-dim"
                htmlFor={`hk-timeout-${String(hook.id)}`}
              >
                timeout (s)
              </label>
              <input
                id={`hk-timeout-${String(hook.id)}`}
                value={timeoutDraft}
                onChange={(e) => setTimeoutDraft(e.target.value)}
                placeholder="none"
                inputMode="numeric"
                className={`w-[72px] rounded-md border bg-surface px-2 py-1 font-mono text-[11px] text-ink-2 focus:outline-none ${
                  timeoutInvalid ? 'border-red/50' : 'border-line focus:border-brand'
                }`}
              />
              <button
                type="button"
                onClick={saveEdit}
                disabled={busy || timeoutInvalid || cmdDraft.trim() === ''}
                className="rounded-lg border border-green/40 bg-green/10 px-3 py-1 font-mono text-[11px] font-semibold text-green transition-colors enabled:hover:bg-green/20 disabled:cursor-not-allowed disabled:opacity-40"
              >
                {busy ? 'saving…' : 'save'}
              </button>
              <button
                type="button"
                onClick={() => setEditing(false)}
                disabled={busy}
                className="rounded-lg border border-line bg-surface px-3 py-1 font-mono text-[11px] text-ink-2 transition-colors hover:bg-surface2 disabled:opacity-50"
              >
                cancel
              </button>
            </div>
          </div>
        ) : (
          <div className="mt-1 overflow-x-auto rounded-md bg-bg px-2.5 py-1.5 font-mono text-[11px] whitespace-pre text-ink-2">
            {hook.command}
          </div>
        )}
        <div className="mt-1 flex flex-wrap items-center gap-x-3 font-mono text-[10.5px] text-ink-dim">
          <span className="break-all">{hook.sourceFile}</span>
          {hook.timeout !== null && (
            <span className="whitespace-nowrap">timeout {String(hook.timeout)}s</span>
          )}
          {hook.statusMessage !== null && (
            <span className="whitespace-nowrap">“{hook.statusMessage}”</span>
          )}
        </div>
      </div>
      {error !== null && (
        <div className="mt-1.5 font-mono text-[11px] text-red" role="alert">
          {error}
        </div>
      )}
      <ConfirmDialog
        open={confirmToggle}
        title={hook.enabled ? 'Disable hook' : 'Enable hook'}
        confirmLabel={hook.enabled ? 'disable' : 'enable'}
        danger={hook.enabled}
        busy={busy}
        onConfirm={doToggle}
        onCancel={() => setConfirmToggle(false)}
      >
        {hook.enabled ? (
          <>
            The command will <b>stop executing in Claude Code</b> — {hook.event} no longer
            triggers it. The entry stays in the settings file (parked under{' '}
            <span className="font-mono">_swarmery_disabled_hooks</span>) and can be re-enabled
            here.
          </>
        ) : (
          <>The command will run in Claude Code again on every {hook.event} event.</>
        )}
      </ConfirmDialog>
    </div>
  );
}

export function HooksTab({
  scope,
  project,
  lint,
  refreshKey,
  onScope,
  onProject,
  onReadonly,
}: {
  scope: 'global' | 'project' | null;
  project: string | null;
  /** Summary-badge lint filter: only hook_no_timeout (warn) applies to hooks. */
  lint: LintSeverity | null;
  refreshKey: number;
  onScope: (scope: 'global' | 'project' | null) => void;
  onProject: (slug: string | null) => void;
  /** A write hit the global readonly kill-switch — page-level banner. */
  onReadonly: () => void;
}): JSX.Element {
  const fetcher = useCallback(
    (filters: SystemListFilters) => fetchSystemHooks(filters),
    [],
  );
  const { rows, error, projectOptions, retry } = useSystemList(fetcher, scope, project, refreshKey);

  if (error !== null) return <ErrorBox message={error} onRetry={retry} />;

  const filtered =
    rows === null ? null : lint === 'warn' ? rows.filter((h) => h.timeout === null) : lint !== null ? [] : rows;

  return (
    <div>
      <FiltersRow
        scope={scope}
        project={project}
        projectOptions={projectOptions}
        onScope={onScope}
        onProject={onProject}
      />
      {filtered === null && <Loading label="hooks…" />}
      {filtered !== null && filtered.length === 0 && (
        <Empty>
          {rows !== null && rows.length > 0
            ? 'no hooks match the current filter'
            : 'no hooks registered — no hooks blocks in ~/.claude/settings.json or any scanned project settings on this machine'}
        </Empty>
      )}
      {filtered !== null &&
        groupByEvent(filtered).map(([event, entries]) => (
          <div key={event}>
            <GroupHeader>
              {event} · {String(entries.length)}
            </GroupHeader>
            <div className="overflow-hidden rounded-xl border border-line bg-surface">
              {entries.map((hook) => (
                <HookRow key={hook.id} hook={hook} onMutated={retry} onReadonly={onReadonly} />
              ))}
            </div>
          </div>
        ))}
    </div>
  );
}
