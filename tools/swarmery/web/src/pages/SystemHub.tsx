// System Hub (fusion phase 18): the catalog-wide extension of the Agent Hub
// pattern (phase 17), grouped by ROLE. Built on the SAME reusable HubShell —
// Toolkit (Skills · Commands · Templates) and Hooks each mount it with their own
// roster source + tabs; Insights is a full-width action inbox (the existing
// promotion/drift/lint views re-homed under a count badge). Nothing is forked:
// the split-pane, the search, the roster list, the tab bar all come from
// HubShell; the Skill Definition tab embeds the existing versioned System editor.
//
// Routing: /system-hub/:category(/:id) (fleet) + /p/:slug/system-hub/… (project,
// rollups + template resolution scoped to :slug). category ∈
// skills|commands|templates|hooks|insights; :id is the selected item.

import { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate, useParams, useSearchParams } from 'react-router-dom';
import type {
  Project,
  SystemCommand,
  SystemHook,
  SystemHubSummary,
  SystemItem,
  SystemTemplate,
  WSMessage,
} from '../api/types';
import { fetchProjects } from '../api';
import { fetchSystemCommands, fetchSystemHooks, fetchSystemItems } from '../api/system';
import { fetchSystemHubSummary, fetchSystemTemplates } from '../api/systemHub';
import { fmtAgo } from '../lib/format';
import { useScope } from '../lib/scope';
import { useLiveUpdates } from '../lib/ws';
import { Empty } from '../components/ui';
import { HubShell, type HubTab } from './agent-hub/HubShell';
import { LintDot, OriginBadge, ScopeBadge } from './system/shared';
import { InsightsTab } from './system/InsightsTab';
import { CommandProfile, HookProfile, SkillProfile, TemplateProfile } from './system-hub/Profiles';

/** The ROLE-grouped catalog sections. Toolkit fans out to three item kinds. */
type HubCategory = 'skills' | 'commands' | 'templates' | 'hooks' | 'insights';
const CATEGORIES: HubCategory[] = ['skills', 'commands', 'templates', 'hooks', 'insights'];

function parseCategory(v: string | undefined): HubCategory {
  return (CATEGORIES as string[]).includes(v ?? '') ? (v as HubCategory) : 'skills';
}

/** Toolkit sub-tabs (Skills/Commands/Templates are one ROLE, three kinds). */
const TOOLKIT: HubCategory[] = ['skills', 'commands', 'templates'];

/* A roster row is one of the four catalog item shapes, tagged by kind so the
 * renderer + the profile can discriminate. */
type RosterRow =
  | { kind: 'skills'; item: SystemItem }
  | { kind: 'commands'; item: SystemCommand }
  | { kind: 'templates'; item: SystemTemplate }
  | { kind: 'hooks'; item: SystemHook };

function rowKey(r: RosterRow): string {
  return r.kind === 'templates' ? r.item.name : String(r.item.id);
}

/* ----- roster cards (one per kind) ----- */

function SkillCard({ item }: { item: SystemItem }): JSX.Element {
  return (
    <>
      <div className="flex flex-wrap items-center gap-2">
        <LintDot severity={item.lintMax} />
        <span className="text-[13.5px] font-semibold text-ink">{item.name}</span>
        <span className="ml-auto flex items-center gap-1.5">
          <ScopeBadge scope={item.scope} projectSlug={item.projectSlug} />
          <OriginBadge origin={item.origin} pluginName={item.pluginName} />
        </span>
      </div>
      {item.description !== null && (
        <div className="mt-[3px] truncate text-[12.5px] text-ink-dim">{item.description}</div>
      )}
      <div className="mt-1 flex flex-wrap items-center gap-x-3 font-mono text-[10px] text-ink-faint">
        <span className="whitespace-nowrap">used 30d {String(item.tasks30d)}</span>
        <span className="whitespace-nowrap">
          {item.lastUsed !== null ? `last ${fmtAgo(item.lastUsed)}` : 'never used'}
        </span>
      </div>
    </>
  );
}

function CommandCard({ item }: { item: SystemCommand }): JSX.Element {
  return (
    <>
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-[13.5px] font-semibold text-ink">/{item.name}</span>
        <span className="ml-auto flex items-center gap-1.5">
          <ScopeBadge scope={item.scope} projectSlug={item.projectSlug} />
          <OriginBadge origin={item.origin} pluginName={item.pluginName} />
        </span>
      </div>
      {item.description !== null && (
        <div className="mt-[3px] truncate text-[12.5px] text-ink-dim">{item.description}</div>
      )}
    </>
  );
}

function TemplateCard({ item }: { item: SystemTemplate }): JSX.Element {
  return (
    <>
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-mono text-[13px] font-semibold text-ink">{item.name}</span>
        <span
          className={`ml-auto rounded-full border px-2 py-px font-mono text-[10px] whitespace-nowrap ${
            item.resolution === 'project override' ? 'border-brand/40 text-brand' : 'border-line-strong text-ink-dim'
          }`}
        >
          {item.resolution}
        </span>
      </div>
      <div className="mt-1 font-mono text-[10px] break-all text-ink-faint">{item.fileName}</div>
    </>
  );
}

function HookCard({ item }: { item: SystemHook }): JSX.Element {
  return (
    <>
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-mono text-[12.5px] font-semibold text-ink">{item.event}</span>
        <span className="font-mono text-[11px] text-ink-dim">{item.matcher ?? '*'}</span>
        <span className="ml-auto flex items-center gap-1.5">
          {item.timeout === null && (
            <span className="rounded-full border border-amber/45 px-2 py-px font-mono text-[10px] text-amber">▲ no timeout</span>
          )}
          <ScopeBadge scope={item.scope} projectSlug={item.projectSlug} />
        </span>
      </div>
      <div className="mt-1 truncate font-mono text-[10.5px] text-ink-dim">{item.command}</div>
    </>
  );
}

function renderRow(r: RosterRow): JSX.Element {
  switch (r.kind) {
    case 'skills':
      return <SkillCard item={r.item} />;
    case 'commands':
      return <CommandCard item={r.item} />;
    case 'templates':
      return <TemplateCard item={r.item} />;
    case 'hooks':
      return <HookCard item={r.item} />;
  }
}

function rowMatches(r: RosterRow, q: string): boolean {
  switch (r.kind) {
    case 'skills':
      return [r.item.name, r.item.description, r.item.path].some((v) => v != null && v.toLowerCase().includes(q));
    case 'commands':
      return [r.item.name, r.item.description].some((v) => v != null && v.toLowerCase().includes(q));
    case 'templates':
      return [r.item.name, r.item.resolution].some((v) => v.toLowerCase().includes(q));
    case 'hooks':
      return [r.item.event, r.item.matcher, r.item.command].some((v) => v != null && v.toLowerCase().includes(q));
  }
}

/* ----- category roster fetch ----- */

function fetchRoster(category: HubCategory, projectId: string | null): Promise<RosterRow[]> {
  // exactOptionalPropertyTypes: only set `project` when scoped (never undefined).
  const filters = projectId !== null ? { project: projectId } : {};
  const project = projectId ?? undefined;
  switch (category) {
    case 'skills':
      return fetchSystemItems('skills', filters).then((rows) => rows.map((item) => ({ kind: 'skills', item })));
    case 'commands':
      return fetchSystemCommands(filters).then((rows) => rows.map((item) => ({ kind: 'commands', item })));
    case 'templates':
      return fetchSystemTemplates(project).then((rows) => rows.map((item) => ({ kind: 'templates', item })));
    case 'hooks':
      return fetchSystemHooks(filters).then((rows) => rows.map((item) => ({ kind: 'hooks', item })));
    case 'insights':
      return Promise.resolve([]);
  }
}

/* ================= the page ================= */

export function SystemHub(): JSX.Element {
  const params = useParams();
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const { scope } = useScope();

  // Workspace mount (/p/:slug/system-hub) carries the slug; fleet mode uses the
  // global scope switcher. Either scopes the rollups + template resolution.
  const scopeSlug = params.slug ?? scope;
  const routeBase = params.slug !== undefined ? `/p/${params.slug}/system-hub` : '/system-hub';

  const category = parseCategory(params.category);
  // :id is the selected item (numeric for skills/commands/hooks, a name for
  // templates). Insights has no selection.
  const selectedKey = params.id ?? null;
  const tab = searchParams.get('tab') ?? '';

  const [roster, setRoster] = useState<RosterRow[] | null>(null);
  const [rosterError, setRosterError] = useState<string | null>(null);
  const [summary, setSummary] = useState<SystemHubSummary | null>(null);
  const [projects, setProjects] = useState<Project[]>([]);
  const [refreshKey, setRefreshKey] = useState(0);
  const [defRefresh, setDefRefresh] = useState(0);

  const loadRoster = useCallback((): void => {
    if (category === 'insights') {
      setRoster([]);
      return;
    }
    setRosterError(null);
    fetchRoster(category, scopeSlug ?? null)
      .then(setRoster)
      .catch((e: unknown) => setRosterError(String(e)));
  }, [category, scopeSlug, refreshKey]);
  useEffect(loadRoster, [loadRoster]);

  const loadSummary = useCallback((): void => {
    fetchSystemHubSummary(scopeSlug ?? undefined)
      .then(setSummary)
      .catch(() => setSummary(null));
  }, [scopeSlug]);
  useEffect(loadSummary, [loadSummary]);

  useEffect(() => {
    fetchProjects()
      .then(setProjects)
      .catch(() => setProjects([]));
  }, []);

  // Live: a registry edit (WS system_item_updated) refetches the roster,
  // summary, and bumps the embedded Definition editor — the same invalidation
  // the System page uses.
  const refresh = useCallback((): void => {
    setRefreshKey((k) => k + 1);
    loadSummary();
  }, [loadSummary]);
  const onMessage = useCallback(
    (msg: WSMessage): void => {
      if (msg.type === 'system_item_updated') {
        refresh();
        setDefRefresh((k) => k + 1);
      }
    },
    [refresh],
  );
  useLiveUpdates(onMessage, refresh);

  const projectNames = useMemo(
    () => Object.fromEntries(projects.map((p) => [p.slug, p.name ?? p.slug])),
    [projects],
  );

  const goCategory = (next: HubCategory): void => {
    navigate(next === 'skills' ? routeBase : `${routeBase}/${next}`);
  };
  const onSelect = useCallback(
    (key: string | null): void => {
      navigate(key === null ? `${routeBase}/${category}` : `${routeBase}/${category}/${encodeURIComponent(key)}`);
    },
    [navigate, routeBase, category],
  );
  const onTab = useCallback(
    (id: string): void => {
      setSearchParams(
        (prev) => {
          const next = new URLSearchParams(prev);
          if (id === '' || id === 'overview') next.delete('tab');
          else next.set('tab', id);
          return next;
        },
        { replace: true },
      );
    },
    [setSearchParams],
  );

  // Tabs per selected item kind (empty when nothing selected / insights).
  const tabs: HubTab[] = useMemo(() => {
    switch (category) {
      case 'skills':
        return [
          { id: 'overview', label: 'Overview' },
          { id: 'usage', label: 'Usage' },
          { id: 'definition', label: 'Definition' },
        ];
      case 'commands':
        return [
          { id: 'overview', label: 'Overview' },
          { id: 'content', label: 'Content' },
        ];
      case 'templates':
        return [{ id: 'overview', label: 'Content' }];
      case 'hooks':
        return [{ id: 'overview', label: 'Config' }];
      case 'insights':
        return [];
    }
  }, [category]);

  // The ROLE nav: Toolkit (with three sub-kinds) · Hooks · Insights, each with
  // a count badge from the summary.
  const roleNav = (
    <RoleNav category={category} summary={summary} onCategory={goCategory} />
  );

  // Insights is a full-width inbox, NOT a split-pane roster.
  if (category === 'insights') {
    return (
      <div className="flex h-full flex-col px-4 pt-6 pb-6 desk:px-10 desk:pt-[34px] desk:pb-[34px]">
        <h1 className="mb-4 font-display text-[30px] leading-tight font-medium tracking-[-0.01em]">
          System Hub
        </h1>
        {roleNav}
        <div className="mt-4 min-h-0 flex-1 overflow-y-auto [-webkit-overflow-scrolling:touch]">
          <InsightsTab refreshKey={refreshKey} projectNames={projectNames} />
        </div>
      </div>
    );
  }

  const activeTab = tabs.some((t) => t.id === tab) ? tab : (tabs[0]?.id ?? 'overview');

  return (
    <div className="flex h-full min-h-0 flex-col">
      {/* Role nav sits above the shell; the shell owns the split-pane below. */}
      <div className="px-4 pt-6 desk:px-10 desk:pt-[34px]">{roleNav}</div>
      <div className="min-h-0 flex-1">
        <HubShell<RosterRow>
          title="System Hub"
          roster={roster}
          rosterError={rosterError}
          onRosterRetry={loadRoster}
          rowKey={rowKey}
          rowMatches={rowMatches}
          renderRow={(r) => renderRow(r)}
          selectedKey={selectedKey}
          onSelect={onSelect}
          searchPlaceholder={`filter ${category}…`}
          rosterEmptyLabel={`no ${category} on this machine`}
          tabs={tabs}
          activeTab={activeTab}
          onTab={onTab}
          detailPlaceholder={<Empty>select a {singular(category)} to see its profile</Empty>}
        >
          {selectedKey !== null && (
            <ProfileFor
              category={category}
              selectedKey={selectedKey}
              tab={activeTab}
              scopeSlug={scopeSlug}
              projectNames={projectNames}
              defRefresh={defRefresh}
              onDefinitionMutated={() => {
                setDefRefresh((k) => k + 1);
                refresh();
              }}
              onTemplateCopied={refresh}
            />
          )}
        </HubShell>
      </div>
    </div>
  );
}

function singular(c: HubCategory): string {
  return c === 'skills' ? 'skill' : c === 'commands' ? 'command' : c === 'templates' ? 'template' : 'hook';
}

/* ----- role nav (Toolkit · Hooks · Insights + Toolkit sub-tabs) ----- */

function RoleNav({
  category,
  summary,
  onCategory,
}: {
  category: HubCategory;
  summary: SystemHubSummary | null;
  onCategory: (c: HubCategory) => void;
}): JSX.Element {
  const inToolkit = (TOOLKIT as string[]).includes(category);
  // exactOptionalPropertyTypes: omit `badge` entirely when the summary is absent.
  const badge = (n: number | undefined): { badge?: number } => (n !== undefined ? { badge: n } : {});
  const toolkitBadge =
    summary === null ? undefined : summary.skills + summary.commands + summary.templates;
  const roles: { key: HubCategory; label: string; badge?: number; active: boolean }[] = [
    { key: 'skills', label: 'Toolkit', ...badge(toolkitBadge), active: inToolkit },
    { key: 'hooks', label: 'Hooks', ...badge(summary?.hooks), active: category === 'hooks' },
    { key: 'insights', label: 'Insights', ...badge(summary?.insights), active: category === 'insights' },
  ];
  return (
    <div className="space-y-2.5">
      <div className="flex gap-1 border-b border-line" role="tablist" aria-label="System Hub sections">
        {roles.map((r) => (
          <button
            key={r.key}
            type="button"
            role="tab"
            aria-selected={r.active}
            onClick={() => onCategory(r.key)}
            className={`-mb-px flex items-center gap-1.5 border-b-2 px-3.5 py-[7px] text-[12.5px] font-medium whitespace-nowrap transition-colors ${
              r.active ? 'border-brand text-brand' : 'border-transparent text-ink-dim hover:text-ink'
            }`}
          >
            {r.label}
            {r.badge !== undefined && r.badge > 0 && (
              <span className="inline-flex h-[16px] min-w-[16px] items-center justify-center rounded-full bg-line-strong px-1 font-mono text-[9.5px] font-bold text-ink-dim">
                {r.badge}
              </span>
            )}
          </button>
        ))}
      </div>
      {inToolkit && (
        <div className="flex gap-1.5">
          {TOOLKIT.map((k) => {
            const badge = summary === null ? undefined : k === 'skills' ? summary.skills : k === 'commands' ? summary.commands : summary.templates;
            const active = category === k;
            return (
              <button
                key={k}
                type="button"
                onClick={() => onCategory(k)}
                aria-pressed={active}
                className={`rounded-full border px-2.5 py-[3px] font-mono text-[11px] whitespace-nowrap transition-colors ${
                  active ? 'border-brand/50 bg-surface2 text-brand' : 'border-line text-ink-dim hover:border-line-strong hover:text-ink'
                }`}
              >
                {k}
                {badge !== undefined ? ` ${String(badge)}` : ''}
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}

/* ----- profile dispatch ----- */

function ProfileFor({
  category,
  selectedKey,
  tab,
  scopeSlug,
  projectNames,
  defRefresh,
  onDefinitionMutated,
  onTemplateCopied,
}: {
  category: HubCategory;
  selectedKey: string;
  tab: string;
  scopeSlug: string | null;
  projectNames: Record<string, string>;
  defRefresh: number;
  onDefinitionMutated: () => void;
  onTemplateCopied: () => void;
}): JSX.Element {
  const id = /^\d+$/.test(selectedKey) ? Number(selectedKey) : NaN;
  switch (category) {
    case 'skills':
      if (Number.isNaN(id)) return <Empty>invalid skill</Empty>;
      return (
        <SkillProfile
          id={id}
          tab={tab === 'usage' || tab === 'definition' ? tab : 'overview'}
          projectId={scopeSlug}
          projectNames={projectNames}
          defRefresh={defRefresh}
          onDefinitionMutated={onDefinitionMutated}
        />
      );
    case 'commands':
      if (Number.isNaN(id)) return <Empty>invalid command</Empty>;
      return <CommandProfile id={id} tab={tab === 'content' ? 'content' : 'overview'} projectId={scopeSlug} />;
    case 'hooks':
      if (Number.isNaN(id)) return <Empty>invalid hook</Empty>;
      return <HookProfile id={id} />;
    case 'templates':
      return <TemplateProfile name={selectedKey} projectId={scopeSlug} onCopied={onTemplateCopied} />;
    case 'insights':
      return <Empty>—</Empty>;
  }
}
