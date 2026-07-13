// Hooks tab (System screen, read-only): settings-file hook entries grouped
// by event, list order = registration order (seq). Commands arrive already
// redacted by the backend. The hook_no_timeout lint (step-04) surfaces as an
// amber "no timeout" flag on entries whose settings JSON has no timeout.

import { useCallback } from 'react';
import type { LintSeverity, SystemHook } from '../../api/types';
import { fetchSystemHooks, type SystemListFilters } from '../../api/system';
import { Empty, ErrorBox, GroupHeader, Loading } from '../../components/ui';
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

function HookRow({ hook }: { hook: SystemHook }): JSX.Element {
  return (
    <div className={`border-b border-line-soft px-3.5 py-2.5 last:border-b-0 ${hook.enabled ? '' : 'opacity-45'}`}>
      <div className="flex flex-wrap items-center gap-1.5">
        <span className="font-mono text-[11.5px] text-ink">{hook.matcher ?? '*'}</span>
        <span className="ml-auto flex flex-wrap items-center gap-1.5">
          <ScopeBadge scope={hook.scope} projectSlug={hook.projectSlug} />
          {hook.managed === 'swarmery' && (
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
        </span>
      </div>
      <div className="mt-1 overflow-x-auto rounded-md bg-bg px-2.5 py-1.5 font-mono text-[11px] whitespace-pre text-ink-2">
        {hook.command}
      </div>
      <div className="mt-1 flex flex-wrap items-center gap-x-3 font-mono text-[10.5px] text-ink-dim">
        <span className="break-all">{hook.sourceFile}</span>
        {hook.timeout !== null && <span className="whitespace-nowrap">timeout {String(hook.timeout)}s</span>}
        {hook.statusMessage !== null && (
          <span className="whitespace-nowrap">“{hook.statusMessage}”</span>
        )}
      </div>
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
}: {
  scope: 'global' | 'project' | null;
  project: string | null;
  /** Summary-badge lint filter: only hook_no_timeout (warn) applies to hooks. */
  lint: LintSeverity | null;
  refreshKey: number;
  onScope: (scope: 'global' | 'project' | null) => void;
  onProject: (slug: string | null) => void;
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
                <HookRow key={hook.id} hook={hook} />
              ))}
            </div>
          </div>
        ))}
    </div>
  );
}
