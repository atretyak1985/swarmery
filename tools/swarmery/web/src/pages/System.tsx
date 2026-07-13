// System screen (phase 4 — Stage 1 read-only registry): what is installed on
// this machine. Header summary from /api/system/summary with clickable lint
// severity badges (→ list filter), tabs Agents | Skills | Hooks | Commands |
// Templates deep-linked via ?tab= (mobile: the strip scrolls horizontally),
// scope/project filters pushed to the API, agents/skills detail panel with
// version history + diff. Live: WS system_item_updated → refetch the open
// tab + summary (payload carries ids only — clients refetch, ws-protocol.md).

import { useCallback, useEffect, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import type { LintSeverity, SystemItem, SystemSummary, WSMessage } from '../api/types';
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
import { FiltersRow, LINT_TONES, LintDot, OriginBadge, ScopeBadge, useSystemList } from './system/shared';
import { SystemItemPanel } from './system/ItemDetail';
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
    <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
      <h1 className="font-display text-[20px] leading-tight font-bold">System</h1>
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
                    active ? 'border-current bg-surface2' : 'border-line hover:border-current'
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
  selected,
  selectable,
  onSelect,
}: {
  item: SystemItem;
  selected: boolean;
  selectable: boolean;
  onSelect: () => void;
}): JSX.Element {
  const body = (
    <>
      <div className="flex flex-wrap items-center gap-2">
        <LintDot severity={item.lintMax} />
        <span className={`text-[13px] font-semibold ${selected ? 'text-brand' : 'text-ink'}`}>
          {item.name}
        </span>
        {item.model !== null && (
          <span className="font-mono text-[10.5px] text-ink-dim">{item.model}</span>
        )}
        <span className="ml-auto flex items-center gap-1.5">
          <ScopeBadge scope={item.scope} projectSlug={item.projectSlug} />
          <OriginBadge origin={item.origin} pluginName={item.pluginName} />
        </span>
      </div>
      {item.description !== null && (
        <div className="mt-0.5 truncate text-[12px] text-ink-dim">{item.description}</div>
      )}
      <div className="mt-1 flex flex-wrap items-center gap-x-3 font-mono text-[10.5px] text-ink-dim/80">
        <span className="whitespace-nowrap">tasks 30d {String(item.tasks30d)}</span>
        <span className="whitespace-nowrap">
          {item.lastUsed !== null ? `used ${fmtAgo(item.lastUsed)}` : 'never used'}
        </span>
        <span className="min-w-0 truncate">{item.path}</span>
      </div>
    </>
  );
  const deadClass = item.dead ? 'opacity-45' : '';
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
        selected ? 'bg-surface2' : 'hover:bg-surface2/50'
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

function ItemsTab({
  kind,
  selectable,
  scope,
  project,
  lint,
  selectedId,
  refreshKey,
  onScope,
  onProject,
  onSelect,
}: {
  kind: SystemItemsKind | 'commands';
  /** Agents/skills open the detail panel; commands are list-only (no detail DTO). */
  selectable: boolean;
  scope: 'global' | 'project' | null;
  project: string | null;
  lint: LintSeverity | null;
  selectedId: number | null;
  refreshKey: number;
  onScope: (scope: 'global' | 'project' | null) => void;
  onProject: (slug: string | null) => void;
  onSelect: (id: number | null) => void;
}): JSX.Element {
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
  const { rows, error, projectOptions, retry } = useSystemList(fetcher, scope, project, refreshKey);

  if (error !== null) return <ErrorBox message={error} onRetry={retry} />;

  const filtered = rows === null ? null : lint !== null ? rows.filter((r) => r.lintMax === lint) : rows;
  const detailOpen = selectable && selectedId !== null;

  const list = (
    <div>
      <FiltersRow
        scope={scope}
        project={project}
        projectOptions={projectOptions}
        onScope={onScope}
        onProject={onProject}
      />
      {filtered === null && <Loading label={`${kind}…`} />}
      {filtered !== null && filtered.length === 0 && (
        <Empty>
          {rows !== null && rows.length > 0
            ? `no ${kind} match the current filter`
            : ITEM_EMPTY[kind]}
        </Empty>
      )}
      {filtered !== null && filtered.length > 0 && (
        <div className="mt-3 overflow-hidden rounded-xl border border-line bg-surface">
          {filtered.map((item) => (
            <ItemRow
              key={item.id}
              item={item}
              selectable={selectable}
              selected={selectedId === item.id}
              onSelect={() => onSelect(selectedId === item.id ? null : item.id)}
            />
          ))}
        </div>
      )}
    </div>
  );

  if (!detailOpen || kind === 'commands') return list;
  return (
    <div className="wide:grid wide:grid-cols-[minmax(300px,380px)_minmax(0,1fr)] wide:items-start wide:gap-6">
      {list}
      <div className="mt-4 min-w-0 wide:mt-3">
        <SystemItemPanel
          kind={kind}
          id={selectedId}
          refreshKey={refreshKey}
          onClose={() => onSelect(null)}
        />
      </div>
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
  const itemParam = searchParams.get('item');
  const selectedId = itemParam !== null && /^\d+$/.test(itemParam) ? Number(itemParam) : null;

  const [summary, setSummary] = useState<SystemSummary | null>(null);
  const [summaryError, setSummaryError] = useState<string | null>(null);
  const [refreshKey, setRefreshKey] = useState(0);

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
  const onSelect = (id: number | null): void => {
    patchParams({ item: id === null ? null : String(id) });
  };

  const listProps = {
    scope,
    project,
    lint,
    refreshKey,
    onScope,
    onProject,
  };

  return (
    <div>
      <SummaryHeader summary={summary} lint={lint} onLint={onLint} />
      {summaryError !== null && <ErrorBox message={summaryError} onRetry={loadSummary} />}

      <div
        className="mt-4 flex gap-0.5 overflow-x-auto border-b border-line [-webkit-overflow-scrolling:touch]"
        role="tablist"
      >
        {TABS.map((t) => (
          <button
            key={t}
            type="button"
            role="tab"
            aria-selected={tab === t}
            onClick={() => setTab(t)}
            className={`-mb-px shrink-0 border-b-2 px-3.5 py-2 text-[12.5px] font-medium whitespace-nowrap transition-colors ${
              tab === t ? 'border-brand text-brand' : 'border-transparent text-ink-dim hover:text-ink'
            }`}
          >
            {TAB_LABELS[t]}
          </button>
        ))}
      </div>

      <div role="tabpanel">
        {tab === 'agents' && (
          <ItemsTab kind="agents" selectable selectedId={selectedId} onSelect={onSelect} {...listProps} />
        )}
        {tab === 'skills' && (
          <ItemsTab kind="skills" selectable selectedId={selectedId} onSelect={onSelect} {...listProps} />
        )}
        {tab === 'hooks' && <HooksTab {...listProps} />}
        {tab === 'commands' && (
          <ItemsTab
            kind="commands"
            selectable={false}
            selectedId={null}
            onSelect={onSelect}
            {...listProps}
          />
        )}
        {tab === 'templates' && <TemplatesTab refreshKey={refreshKey} />}
      </div>
    </div>
  );
}
