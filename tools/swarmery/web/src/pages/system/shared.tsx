// Shared primitives of the System screen (phase 4, Stage 1 read-only):
// scope/origin/lint badges, the scope+project filter row, and the list-fetch
// hook every tab uses. Visual language mirrors components/ui.tsx (hairline
// pill chips, mono micro-type); tooltips are native `title` attributes.

import { useEffect, useState } from 'react';
import type { LintSeverity } from '../../api/types';
import type { SystemListFilters } from '../../api/system';

/* ----- badges ----- */

export function ScopeBadge({
  scope,
  projectSlug,
}: {
  scope: 'global' | 'project';
  projectSlug: string | null;
}): JSX.Element {
  if (scope === 'project') {
    return (
      <span className="rounded-full border border-blue/40 px-2 py-px font-mono text-[10px] whitespace-nowrap text-blue">
        project{projectSlug !== null ? ` · ${projectSlug}` : ''}
      </span>
    );
  }
  return (
    <span className="rounded-full border border-line px-2 py-px font-mono text-[10px] whitespace-nowrap text-ink-dim">
      global
    </span>
  );
}

export function OriginBadge({
  origin,
  pluginName,
}: {
  origin: 'local' | 'plugin';
  pluginName: string | null;
}): JSX.Element {
  if (origin === 'plugin') {
    return (
      <span className="rounded-full border border-brand/40 px-2 py-px font-mono text-[10px] whitespace-nowrap text-brand">
        plugin{pluginName !== null ? ` · ${pluginName}` : ''}
      </span>
    );
  }
  return (
    <span className="rounded-full border border-line px-2 py-px font-mono text-[10px] whitespace-nowrap text-ink-dim">
      local
    </span>
  );
}

export const LINT_TONES: Record<LintSeverity, string> = {
  error: 'text-red',
  warn: 'text-amber',
  info: 'text-blue',
};

/** Max-severity lint marker of a list row; null (clean) renders nothing. */
export function LintDot({
  severity,
  message,
}: {
  severity: LintSeverity | null;
  message?: string;
}): JSX.Element | null {
  if (severity === null) return null;
  return (
    <span
      className={`shrink-0 font-mono text-[12px] leading-none ${LINT_TONES[severity]}`}
      title={message ?? `worst active lint finding: ${severity}`}
      aria-label={`lint ${severity}`}
    >
      {severity === 'info' ? '●' : '▲'}
    </span>
  );
}

/* ----- filter chips (Sessions FilterChip rhythm) ----- */

export function FilterChip({
  selected,
  onClick,
  children,
}: {
  selected: boolean;
  onClick: () => void;
  children: string;
}): JSX.Element {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={selected}
      className={`shrink-0 rounded-full border px-2.5 py-[3px] font-mono text-[10.5px] whitespace-nowrap transition-colors ${
        selected ? 'border-ink-dim bg-surface2 text-ink' : 'border-line text-ink-dim hover:text-ink'
      }`}
    >
      {children}
    </button>
  );
}

/** Scope (all/global/project) + project-slug chips — pushed to the API as
 * `?scope=&project=` (the step-05 handlers filter server-side). */
export function FiltersRow({
  scope,
  project,
  projectOptions,
  onScope,
  onProject,
}: {
  scope: 'global' | 'project' | null;
  project: string | null;
  projectOptions: string[];
  onScope: (scope: 'global' | 'project' | null) => void;
  onProject: (slug: string | null) => void;
}): JSX.Element {
  return (
    <div className="mt-3 flex flex-wrap items-center gap-1.5">
      <FilterChip selected={scope === null} onClick={() => onScope(null)}>
        all scopes
      </FilterChip>
      <FilterChip selected={scope === 'global'} onClick={() => onScope('global')}>
        global
      </FilterChip>
      <FilterChip selected={scope === 'project'} onClick={() => onScope('project')}>
        project
      </FilterChip>
      {projectOptions.length > 0 && (
        <>
          <span className="mx-1 h-4 w-px bg-line" aria-hidden="true" />
          {projectOptions.map((slug) => (
            <FilterChip
              key={slug}
              selected={project === slug}
              onClick={() => onProject(project === slug ? null : slug)}
            >
              {slug}
            </FilterChip>
          ))}
        </>
      )}
    </div>
  );
}

/* ----- list-fetch hook ----- */

interface SystemListState<T> {
  rows: T[] | null;
  error: string | null;
  /** Project slugs seen in the last UNfiltered response (chip options). */
  projectOptions: string[];
  retry: () => void;
}

/**
 * Fetches one /api/system list whenever filters or refreshKey change
 * (refreshKey bumps on WS system_item_updated → refetch of the open tab).
 */
export function useSystemList<T extends { projectSlug: string | null }>(
  fetcher: (filters: SystemListFilters) => Promise<T[]>,
  scope: 'global' | 'project' | null,
  project: string | null,
  refreshKey: number,
): SystemListState<T> {
  const [rows, setRows] = useState<T[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [projectOptions, setProjectOptions] = useState<string[]>([]);
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    let cancelled = false;
    const filters: SystemListFilters = {};
    if (scope !== null) filters.scope = scope;
    if (project !== null) filters.project = project;
    fetcher(filters)
      .then((list) => {
        if (cancelled) return;
        setRows(list);
        setError(null);
        if (project === null) {
          const slugs = [...new Set(list.map((r) => r.projectSlug).filter((s) => s !== null))];
          setProjectOptions(slugs.sort());
        }
      })
      .catch((e: unknown) => {
        if (!cancelled) setError(String(e));
      });
    return () => {
      cancelled = true;
    };
  }, [fetcher, scope, project, refreshKey, attempt]);

  return { rows, error, projectOptions, retry: () => setAttempt((a) => a + 1) };
}
