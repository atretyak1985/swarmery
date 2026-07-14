// System screen (phase 4 — Stage 1 read-only registry): what is installed on
// this machine. Header summary from /api/system/summary with clickable lint
// severity badges (→ list filter), tabs Agents | Skills | Hooks | Commands |
// Templates deep-linked via ?tab= (mobile: the strip scrolls horizontally),
// scope/project filters pushed to the API, agents/skills detail panel with
// version history + diff. Live: WS system_item_updated → refetch the open
// tab + summary (payload carries ids only — clients refetch, ws-protocol.md).

import { useCallback, useEffect, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import type { LintSeverity, Project, SystemItem, SystemSummary, WSMessage } from '../api/types';
import { fetchProjects } from '../api';
import {
  fetchSystemCommands,
  fetchSystemItems,
  fetchSystemSummary,
  type SystemItemsKind,
  type SystemListFilters,
} from '../api/system';
import { fmtAgo } from '../lib/format';
import { useLiveUpdates } from '../lib/ws';
import { Empty, ErrorBox, Loading } from '../components/ui';
import {
  FiltersRow,
  LINT_TONES,
  LintDot,
  OriginBadge,
  ScopeBadge,
  parseSort,
  sortItems,
  useSystemList,
  type SystemSort,
} from './system/shared';
import { SystemItemPanel } from './system/ItemDetail';
import { CreateAgentForm } from './system/CreateAgentForm';
import { HooksTab } from './system/HooksTab';
import { TemplatesTab } from './system/TemplatesTab';

type SystemTab = 'agents' | 'skills' | 'hooks' | 'commands' | 'templates';

const TABS: SystemTab[] = ['agents', 'skills', 'hooks', 'commands', 'templates'];
const TAB_LABELS: Record<SystemTab, string> = {
  agents: 'Agents',
  skills: 'Skills',
  hooks: 'Hooks',
  commands: 'Commands',
  templates: 'Templates',
};

/** ?tab= deep-links (shareable); anything else falls back to Agents. */
function parseTab(value: string | null): SystemTab {
  return (TABS as string[]).includes(value ?? '') ? (value as SystemTab) : 'agents';
}

function parseScope(value: string | null): 'global' | 'project' | null {
  return value === 'global' || value === 'project' ? value : null;
}

function parseLint(value: string | null): LintSeverity | null {
  return value === 'error' || value === 'warn' || value === 'info' ? value : null;
}

const DEAD_TOOLTIP = 'dead: 0 telemetry mentions in the last 30 days (advisory)';

/* ----- header summary + lint severity badges ----- */

function SummaryHeader({
  summary,
  lint,
  onLint,
}: {
  summary: SystemSummary | null;
  lint: LintSeverity | null;
  onLint: (severity: LintSeverity | null) => void;
}): JSX.Element {
  return (
    <div className="flex flex-wrap items-baseline gap-x-[14px] gap-y-[10px]">
      <h1 className="font-display text-[30px] leading-tight font-medium tracking-[-0.01em]">
        System
      </h1>
      {summary !== null && (
        <>
          <span className="font-mono text-[11px] text-ink-dim">
            {String(summary.agents)} agents · {String(summary.skills)} skills ·{' '}
            {String(summary.hooks)} hooks · {String(summary.commands)} commands ·{' '}
            {String(summary.overlays)} overlays
          </span>
          <span className="flex items-center gap-1.5">
            {(['error', 'warn', 'info'] as const).map((severity) => {
              const count = summary.lint[severity];
              if (count === 0) return null;
              const active = lint === severity;
              return (
                <button
                  key={severity}
                  type="button"
                  aria-pressed={active}
                  title={`filter lists by worst active lint finding: ${severity}`}
                  onClick={() => onLint(active ? null : severity)}
                  className={`rounded-full border px-2 py-px font-mono text-[10px] whitespace-nowrap transition-colors ${LINT_TONES[severity]} ${
                    active ? 'border-current bg-surface2' : 'border-current/50 hover:border-current'
                  }`}
                >
                  {severity === 'info' ? '●' : '▲'} {String(count)} {severity}
                </button>
              );
            })}
          </span>
        </>
      )}
    </div>
  );
}

/* ----- agents/skills list + detail (commands reuse the row shell) ----- */

function ItemRow({
  item,
  projectName,
  selected,
  selectable,
  onSelect,
}: {
  item: SystemItem;
  projectName?: string | null;
  selected: boolean;
  selectable: boolean;
  onSelect: () => void;
}): JSX.Element {
  const body = (
    <>
      <div className="flex flex-wrap items-center gap-2">
        <LintDot severity={item.lintMax} />
        <span className={`text-[13.5px] font-semibold ${selected ? 'text-brand' : 'text-ink'}`}>
          {item.name}
        </span>
        {item.model !== null && (
          <span className="font-mono text-[10px] text-ink-faint">{item.model}</span>
        )}
        <span className="ml-auto flex items-center gap-1.5">
          <ScopeBadge scope={item.scope} projectSlug={item.projectSlug} projectName={projectName} />
          <OriginBadge origin={item.origin} pluginName={item.pluginName} />
        </span>
      </div>
      {item.description !== null && (
        <div className="mt-[3px] truncate text-[12.5px] text-ink-dim">{item.description}</div>
      )}
      <div className="mt-1 flex flex-wrap items-center gap-x-3 font-mono text-[10px] text-ink-faint">
        <span className="whitespace-nowrap">tasks 30d {String(item.tasks30d)}</span>
        <span className="whitespace-nowrap">
          {item.lastUsed !== null ? `used ${fmtAgo(item.lastUsed)}` : 'never used'}
        </span>
        <span className="min-w-0 truncate">{item.path}</span>
      </div>
    </>
  );
  const deadClass = item.dead ? 'opacity-70' : '';
  if (!selectable) {
    return (
      <div
        className={`border-b border-line-soft px-3.5 py-2.5 last:border-b-0 ${deadClass}`}
        {...(item.dead ? { title: DEAD_TOOLTIP } : {})}
      >
        {body}
      </div>
    );
  }
  return (
    <button
      type="button"
      onClick={onSelect}
      aria-current={selected ? 'true' : undefined}
      title={item.dead ? DEAD_TOOLTIP : `open ${item.name}`}
      className={`block w-full border-b border-line-soft px-3.5 py-2.5 text-left transition-colors last:border-b-0 ${
        selected ? 'bg-surface2' : 'hover:bg-surface'
      } ${deadClass}`}
    >
      {body}
    </button>
  );
}

const ITEM_EMPTY: Record<'agents' | 'skills' | 'commands', string> = {
  agents:
    'no agents on this machine — ~/.claude/agents/ and the scanned projects’ .claude/agents/ are absent or empty',
  skills:
    'no skills on this machine — ~/.claude/skills/ and the scanned projects’ .claude/skills/ are absent or empty',
  commands:
    'no commands on this machine — ~/.claude/commands/ and the scanned projects’ .claude/commands/ are absent or empty',
};

const SEARCH_DEBOUNCE_MS = 150;

function ItemsTab({
  kind,
  selectable,
  scope,
  project,
  projects,
  lint,
  sort,
  selectedId,
  refreshKey,
  onScope,
  onProject,
  onSort,
  onSelect,
  onMutated,
  onDeleted,
  onReadonly,
}: {
  kind: SystemItemsKind | 'commands';
  /** Agents/skills open the detail panel; commands are list-only (no detail DTO). */
  selectable: boolean;
  scope: 'global' | 'project' | null;
  project: string | null;
  projects: Project[];
  lint: LintSeverity | null;
  sort: SystemSort;
  selectedId: number | null;
  refreshKey: number;
  onScope: (scope: 'global' | 'project' | null) => void;
  onProject: (slug: string | null) => void;
  onSort: (sort: SystemSort) => void;
  onSelect: (id: number | null) => void;
  /** A write landed in the panel/form — refetch list + summary + detail. */
  onMutated: () => void;
  /** Soft delete landed — close the panel and refetch. */
  onDeleted: () => void;
  /** A write hit the global readonly kill-switch — page-level banner. */
  onReadonly: () => void;
}): JSX.Element {
  // "+ new agent" (step-12) — the form collapses back into the button.
  const [creating, setCreating] = useState(false);
  const [search, setSearch] = useState('');
  const [query, setQuery] = useState('');
  useEffect(() => {
    const t = setTimeout(() => setQuery(search.trim().toLowerCase()), SEARCH_DEBOUNCE_MS);
    return () => clearTimeout(t);
  }, [search]);

  const fetcher = useCallback(
    (filters: SystemListFilters): Promise<SystemItem[]> =>
      kind === 'commands'
        ? // Commands carry no model/lint/usage columns — pad to the shared row shape.
          fetchSystemCommands(filters).then((rows) =>
            rows.map((c) => ({
              ...c,
              model: null,
              lintMax: null,
              dead: false,
              lastUsed: null,
              tasks30d: 0,
            })),
          )
        : fetchSystemItems(kind, filters),
    [kind],
  );
  const { rows, error, retry } = useSystemList(fetcher, scope, project, refreshKey);

  if (error !== null) return <ErrorBox message={error} onRetry={retry} />;

  const lintFiltered = rows === null ? null : lint !== null ? rows.filter((r) => r.lintMax === lint) : rows;
  const searched =
    lintFiltered === null
      ? null
      : query === ''
        ? lintFiltered
        : lintFiltered.filter((r) =>
            [r.name, r.description, r.path].some(
              (v) => v != null && v.toLowerCase().includes(query),
            ),
          );
  const filtered = searched === null ? null : sortItems(searched, sort);
  const detailOpen = selectable && selectedId !== null;

  // name lookup map for ScopeBadge — avoids passing projects[] into every row
  const projectNames = Object.fromEntries(
    projects.map((p) => [p.slug, p.name ?? p.slug]),
  );

  const listContent = (
    <>
      {filtered === null && <Loading label={`${kind}…`} />}
      {filtered !== null && filtered.length === 0 && (
        <Empty>
          {rows !== null && rows.length > 0
            ? `no ${kind} match the current filter`
            : ITEM_EMPTY[kind]}
        </Empty>
      )}
      {filtered !== null && filtered.length > 0 && (
        <div className="overflow-hidden">
          {filtered.map((item) => (
            <ItemRow
              key={item.id}
              item={item}
              projectName={item.projectSlug !== null ? (projectNames[item.projectSlug] ?? item.projectSlug) : null}
              selectable={selectable}
              selected={selectedId === item.id}
              onSelect={() => onSelect(selectedId === item.id ? null : item.id)}
            />
          ))}
        </div>
      )}
    </>
  );

  return (
    <div className="flex h-full flex-col">
      {/* filters + new agent — never scroll */}
      <div className="shrink-0 pt-0 pb-3">
        <FiltersRow
          scope={scope}
          project={project}
          projects={projects}
          search={search}
          onSearch={setSearch}
          onScope={onScope}
          onProject={onProject}
          sort={sort}
          onSort={onSort}
        />
        {kind === 'agents' && (
          <div className="mt-[14px]">
            {creating ? (
              <CreateAgentForm
                onCancel={() => setCreating(false)}
                onCreated={(id) => {
                  setCreating(false);
                  onMutated();
                  onSelect(id);
                }}
                onReadonly={onReadonly}
              />
            ) : (
              <button
                type="button"
                onClick={() => setCreating(true)}
                className="rounded-lg border border-line-strong bg-field px-3 py-1.5 text-[12px] font-semibold text-ink-2 transition-colors hover:bg-surface2"
              >
                + new agent
              </button>
            )}
          </div>
        )}
      </div>

      {/* scrollable area — list only (no detail) or 2-col grid with independent scrolls */}
      {!detailOpen || kind === 'commands' ? (
        <div className="min-h-0 flex-1 overflow-y-auto rounded-xl border border-line [-webkit-overflow-scrolling:touch]">
          {listContent}
        </div>
      ) : (
        <div className="min-h-0 flex-1 wide:grid wide:grid-cols-[minmax(320px,400px)_minmax(0,1fr)] wide:gap-6 wide:overflow-hidden">
          {/* Border framed on the scroll container too, so list + detail align
              top & bottom and the rows scroll inside a fixed perimeter. */}
          <div className="overflow-y-auto rounded-xl border border-line [-webkit-overflow-scrolling:touch]">
            {listContent}
          </div>
          {/* Border is a fixed frame on the scroll container — the panel
              scrolls INSIDE it, so the perimeter stays visible while scrolling. */}
          <div className="overflow-y-auto rounded-xl border border-line bg-surface px-[18px] py-4 [-webkit-overflow-scrolling:touch]">
            <SystemItemPanel
              kind={kind}
              id={selectedId}
              refreshKey={refreshKey}
              projectNames={projectNames}
              onClose={() => onSelect(null)}
              onMutated={onMutated}
              onDeleted={onDeleted}
              onReadonly={onReadonly}
            />
          </div>
        </div>
      )}
    </div>
  );
}

/* ----- the page ----- */

export function System(): JSX.Element {
  const [searchParams, setSearchParams] = useSearchParams();
  const tab = parseTab(searchParams.get('tab'));
  const scope = parseScope(searchParams.get('scope'));
  const project = searchParams.get('project');
  const lint = parseLint(searchParams.get('lint'));
  const sort = parseSort(searchParams.get('sort'));
  const itemParam = searchParams.get('item');
  const selectedId = itemParam !== null && /^\d+$/.test(itemParam) ? Number(itemParam) : null;

  const [projects, setProjects] = useState<Project[]>([]);
  const [summary, setSummary] = useState<SystemSummary | null>(null);
  const [summaryError, setSummaryError] = useState<string | null>(null);
  const [refreshKey, setRefreshKey] = useState(0);
  // Step-12: any write that hits the SWARMERY_SYSTEM_READONLY kill-switch
  // (403 readonly) raises this page-level banner; it stays for the session.
  const [readonly, setReadonly] = useState(false);

  const patchParams = useCallback(
    (patch: Record<string, string | null>): void => {
      setSearchParams(
        (prev) => {
          const next = new URLSearchParams(prev);
          for (const [key, value] of Object.entries(patch)) {
            if (value === null) next.delete(key);
            else next.set(key, value);
          }
          return next;
        },
        { replace: true },
      );
    },
    [setSearchParams],
  );

  useEffect(() => {
    fetchProjects()
      .then(setProjects)
      .catch(() => setProjects([]));
  }, []);

  const loadSummary = useCallback((): void => {
    fetchSystemSummary()
      .then((s) => {
        setSummary(s);
        setSummaryError(null);
      })
      .catch((e: unknown) => setSummaryError(String(e)));
  }, []);
  useEffect(loadSummary, [loadSummary]);

  // Live updates: the payload is a cache-invalidation hint (kind + itemId) —
  // refetch the open tab's list, the open detail, and the summary counters.
  const refresh = useCallback((): void => {
    setRefreshKey((k) => k + 1);
    loadSummary();
  }, [loadSummary]);
  const onMessage = useCallback(
    (msg: WSMessage): void => {
      if (msg.type === 'system_item_updated') refresh();
    },
    [refresh],
  );
  useLiveUpdates(onMessage, refresh);

  const setTab = (next: SystemTab): void => {
    // Keep scope/project/lint across tabs (they mean the same thing); the
    // item selection belongs to one list.
    patchParams({ tab: next === 'agents' ? null : next, item: null });
  };
  const onScope = (next: 'global' | 'project' | null): void => {
    patchParams({ scope: next, item: null });
  };
  const onProject = (slug: string | null): void => {
    patchParams({ project: slug, item: null });
  };
  const onLint = (severity: LintSeverity | null): void => {
    patchParams({ lint: severity, item: null });
  };
  // Sort is a view preference — it never changes which row is selected, so it
  // keeps ?item=. 'name' is the default and stays out of the URL.
  const onSort = (next: SystemSort): void => {
    patchParams({ sort: next === 'name' ? null : next });
  };
  const onSelect = (id: number | null): void => {
    patchParams({ item: id === null ? null : String(id) });
  };
  const onReadonly = useCallback((): void => setReadonly(true), []);
  const onDeleted = useCallback((): void => {
    patchParams({ item: null });
    refresh();
  }, [patchParams, refresh]);

  const listProps = {
    scope,
    project,
    lint,
    refreshKey,
    onScope,
    onProject,
  };

  return (
    <div className="flex h-full flex-col px-4 pt-6 pb-6 desk:px-10 desk:pt-[34px] desk:pb-[34px]">
      {/* sticky header: title + summary + lint badges + readonly banner + tabs */}
      <div className="shrink-0">
        <SummaryHeader summary={summary} lint={lint} onLint={onLint} />
        {summaryError !== null && <ErrorBox message={summaryError} onRetry={loadSummary} />}
        {readonly && (
          <div
            className="mt-3 rounded-lg border border-amber/40 bg-amber/10 px-3.5 py-2.5 font-mono text-[12px] text-amber"
            role="alert"
          >
            ▲ System is in readonly mode — the daemon rejected a write
            (SWARMERY_SYSTEM_READONLY). Edits, toggles, rollbacks and deletes stay off until the
            kill-switch is lifted.
          </div>
        )}
        <div
          className="mt-[18px] flex gap-1 overflow-x-auto border-b border-line [-webkit-overflow-scrolling:touch]"
          role="tablist"
        >
          {TABS.map((t) => (
            <button
              key={t}
              type="button"
              role="tab"
              aria-selected={tab === t}
              onClick={() => setTab(t)}
              className={`-mb-px shrink-0 border-b-2 px-3.5 py-[7px] text-[12.5px] font-medium whitespace-nowrap transition-colors ${
                tab === t ? 'border-brand text-brand' : 'border-transparent text-ink-dim hover:text-ink'
              }`}
            >
              {TAB_LABELS[t]}
            </button>
          ))}
        </div>
      </div>

      {/* content area — each tab manages its own internal scroll */}
      <div className="min-h-0 flex-1 overflow-hidden" role="tabpanel">
        {tab === 'agents' && (
          <ItemsTab
            kind="agents"
            selectable
            projects={projects}
            selectedId={selectedId}
            sort={sort}
            onSort={onSort}
            onSelect={onSelect}
            onMutated={refresh}
            onDeleted={onDeleted}
            onReadonly={onReadonly}
            {...listProps}
          />
        )}
        {tab === 'skills' && (
          <ItemsTab
            kind="skills"
            selectable
            projects={projects}
            selectedId={selectedId}
            sort={sort}
            onSort={onSort}
            onSelect={onSelect}
            onMutated={refresh}
            onDeleted={onDeleted}
            onReadonly={onReadonly}
            {...listProps}
          />
        )}
        {tab === 'hooks' && <HooksTab projects={projects} onReadonly={onReadonly} {...listProps} />}
        {tab === 'commands' && (
          <ItemsTab
            kind="commands"
            selectable={false}
            projects={projects}
            selectedId={null}
            sort={sort}
            onSort={onSort}
            onSelect={onSelect}
            onMutated={refresh}
            onDeleted={onDeleted}
            onReadonly={onReadonly}
            {...listProps}
          />
        )}
        {tab === 'templates' && (
          <div className="h-full overflow-y-auto pb-4 pt-3 [-webkit-overflow-scrolling:touch]">
            <TemplatesTab refreshKey={refreshKey} />
          </div>
        )}
      </div>
    </div>
  );
}
