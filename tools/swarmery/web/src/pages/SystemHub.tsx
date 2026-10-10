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

import type { MessageDescriptor } from '@lingui/core';
import { msg } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
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
import { ScopeChip } from '../components/ScopeChip';
import { useProjectScope, useScope } from '../lib/scope';
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

/** The category as a plural noun in running text ("filter skills…"). */
const CATEGORY_NOUN: Record<HubCategory, MessageDescriptor> = {
  skills: msg`skills`,
  commands: msg`commands`,
  templates: msg`templates`,
  hooks: msg`hooks`,
  insights: msg`insights`,
};

/** One item of the category ("select a skill…"); insights has no items. */
const CATEGORY_SINGULAR: Record<HubCategory, MessageDescriptor> = {
  skills: msg`skill`,
  commands: msg`command`,
  templates: msg`template`,
  hooks: msg`hook`,
  insights: msg`hook`,
};

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

/** Where the row is DEFINED, for the shell's origin-scope chips. Skills,
 * commands and hooks carry `scope` straight from the registry (plugin-shipped
 * items are scanned as global); templates predate that field, so their
 * plugin/project `source` — the same distinction — stands in for it. */
function rowScope(r: RosterRow): 'global' | 'project' {
  return r.kind === 'templates' ? (r.item.source === 'project' ? 'project' : 'global') : r.item.scope;
}

/* ----- roster cards (one per kind) ----- */

function SkillCard({ item }: { item: SystemItem }): JSX.Element {
  const { t } = useLingui();
  const used30d = String(item.tasks30d);
  const lastUsed = item.lastUsed !== null ? fmtAgo(item.lastUsed) : null;
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
        <span className="whitespace-nowrap">
          <Trans>used 30d {used30d}</Trans>
        </span>
        <span className="whitespace-nowrap">{lastUsed !== null ? t`last ${lastUsed}` : t`never used`}</span>
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
            // i18n-ignore — a resolution value the server sends
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
            <span className="rounded-full border border-amber/45 px-2 py-px font-mono text-[10px] text-amber">
              <Trans>▲ no timeout</Trans>
            </span>
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

/** Props are optional so the standalone /system-hub and /p/:slug/system-hub
 * mounts are unchanged. The tabbed System shell (pages/SystemShell.tsx) mounts
 * this EMBEDDED: `embedded` suppresses the heading + the standalone role nav
 * (the shell's outer tab bar owns Toolkit/Hooks/Insights), `forceCategory` pins
 * the active category from the outer tab, `routeBase` keeps internal navigation
 * on the /system tree, and `scopeSlug` is supplied by the shell. */
export function SystemHub({
  embedded = false,
  forceCategory,
  routeBase: routeBaseProp,
  scopeSlug: scopeSlugProp,
  projectScoped = false,
  originScope = null,
}: {
  embedded?: boolean;
  forceCategory?: HubCategory;
  routeBase?: string;
  scopeSlug?: string | null;
  /** Origin-scope filter owned by the caller (SystemShell's chips above the tab
   * bar): null = all scopes, otherwise keep only rows defined in that scope.
   * Applied CLIENT-SIDE — the roster is already the effective catalog for the
   * project scope, and these chips only slice what arrived. */
  originScope?: 'global' | 'project' | null;
  /** PROJECT mode (/p/:slug/system/…): the roster is the project's EFFECTIVE
   * catalog — its own items PLUS the global ones PLUS the built-ins of the packs
   * it enables. The narrowing is the server's (?project= / ?projectId=), so this
   * flag drives only the empty-state copy. Fleet mode is unchanged. */
  projectScoped?: boolean;
} = {}): JSX.Element {
  const { i18n, t } = useLingui();
  const params = useParams();
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const { scope } = useScope();

  // Workspace mount (/p/:slug/system-hub) carries the slug; fleet mode uses the
  // global scope switcher. Either scopes the rollups + template resolution. When
  // embedded the shell passes both explicitly (it owns the route base).
  // The scope REFERENCE (pretty slug from the URL or the global scope chip)…
  const scopeRef = scopeSlugProp !== undefined ? scopeSlugProp : (params.slug ?? scope);
  // …resolved to the numeric project id before it reaches the API. The
  // /api/system/* template endpoints match the DB path slug or the id only, so
  // the pretty slug 500s there (see useProjectScope). Embedded, the shell has
  // already resolved it — the resolver is idempotent on a numeric id.
  // `scopePending` gates every fetch below: the unscoped catalog is the whole
  // fleet, and firing it while the project list resolves lets it land last and
  // overwrite the scoped one.
  const { id: scopeSlug, pending: scopePending } = useProjectScope(scopeRef);
  const routeBase =
    routeBaseProp ?? (params.slug !== undefined ? `/p/${params.slug}/system-hub` : '/system-hub');

  // Toolkit sub-kind (skills/commands/templates) when embedded: the outer shell
  // pins the ROLE via forceCategory ('skills' for the Toolkit tab, or 'hooks' /
  // 'insights'); the Toolkit sub-pills then flip this LOCAL state instead of the
  // URL, so the shell keeps a flat /system/toolkit route (no nested sub-path).
  const [subCat, setSubCat] = useState<HubCategory>('skills');
  // Category: standalone → route param; embedded → forceCategory, except when
  // forceCategory is the Toolkit role, where the active sub-kind (subCat) wins.
  const category = embedded
    ? forceCategory === 'skills'
      ? subCat
      : (forceCategory ?? 'skills')
    : parseCategory(params.category);
  // :id is the selected item (numeric for skills/commands/hooks, a name for
  // templates). Insights has no selection. Standalone reads it from the route;
  // embedded keeps it in LOCAL state (the shell route has no :id segment).
  const [embSel, setEmbSel] = useState<string | null>(null);
  const selectedKey = embedded ? embSel : (params.id ?? null);
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
    if (scopePending) return;
    setRosterError(null);
    // No client-side narrowing: a scopeSlug makes every endpoint above serve the
    // project's EFFECTIVE catalog already, and re-filtering here would drop the
    // pack/global items the project genuinely resolves.
    fetchRoster(category, scopeSlug ?? null)
      .then(setRoster)
      .catch((e: unknown) => setRosterError(String(e)));
  }, [category, scopeSlug, scopePending, refreshKey]);
  useEffect(loadRoster, [loadRoster]);

  const loadSummary = useCallback((): void => {
    if (scopePending) return;
    fetchSystemHubSummary(scopeSlug ?? undefined)
      .then(setSummary)
      .catch(() => setSummary(null));
  }, [scopeSlug, scopePending]);
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

  const visibleRoster = useMemo(
    () => (roster === null || originScope === null ? roster : roster.filter((r) => rowScope(r) === originScope)),
    [roster, originScope],
  );

  const goCategory = (next: HubCategory): void => {
    if (embedded) {
      // Toolkit sub-pill switch: local state, and drop any selection so the new
      // sub-kind opens on its roster (the previous item belongs to another kind).
      setSubCat(next);
      setEmbSel(null);
      return;
    }
    navigate(next === 'skills' ? routeBase : `${routeBase}/${next}`);
  };
  const onSelect = useCallback(
    (key: string | null): void => {
      if (embedded) {
        setEmbSel(key);
        return;
      }
      navigate(key === null ? `${routeBase}/${category}` : `${routeBase}/${category}/${encodeURIComponent(key)}`);
    },
    [embedded, navigate, routeBase, category],
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
          { id: 'overview', label: t`Overview` },
          { id: 'docs', label: t`Docs` },
          { id: 'usage', label: t`Usage` },
          { id: 'definition', label: t`Definition` },
        ];
      case 'commands':
        return [
          { id: 'overview', label: t`Overview` },
          { id: 'docs', label: t`Docs` },
          { id: 'content', label: t`Content` },
        ];
      case 'templates':
        return [{ id: 'overview', label: t`Content` }];
      case 'hooks':
        return [{ id: 'overview', label: t`Config` }];
      case 'insights':
        return [];
    }
  }, [category, t]);

  // The ROLE nav: Toolkit (with three sub-kinds) · Hooks · Insights, each with
  // a count badge from the summary. When embedded the outer shell owns the
  // role row, so RoleNav renders ONLY the Toolkit sub-pills (skills/commands/
  // templates) — and nothing at all for Hooks/Insights.
  const roleNav = (
    <RoleNav category={category} summary={summary} onCategory={goCategory} embedded={embedded} />
  );

  // Insights is a full-width inbox, NOT a split-pane roster.
  if (category === 'insights') {
    return (
      <div
        className={
          embedded
            ? 'flex h-full min-h-0 flex-col'
            : 'flex h-full flex-col px-4 pt-6 pb-6 desk:px-10 desk:pt-[34px] desk:pb-[34px]'
        }
      >
        {!embedded && (
          <h1 className="mb-4 font-display text-[30px] leading-tight font-medium tracking-[-0.01em]">
            <Trans>System Hub</Trans>
          </h1>
        )}
        {!embedded && roleNav}
        <div className="mt-4 min-h-0 flex-1 overflow-y-auto [-webkit-overflow-scrolling:touch]">
          <InsightsTab refreshKey={refreshKey} projectNames={projectNames} projectId={scopeSlug} />
        </div>
      </div>
    );
  }

  const activeTab = tabs.some((item) => item.id === tab) ? tab : (tabs[0]?.id ?? 'overview');
  const noun = i18n._(CATEGORY_NOUN[category]);
  const singularNoun = i18n._(CATEGORY_SINGULAR[category]);

  return (
    <div className="flex h-full min-h-0 flex-col">
      {/* Role nav sits above the shell; the shell owns the split-pane below.
          Standalone: full Toolkit/Hooks/Insights row + Toolkit sub-pills, with
          the page's own top padding. Embedded: RoleNav emits only the Toolkit
          sub-pills (Toolkit tab) or nothing (Hooks), and the outer shell owns
          the chrome — so we add a light top margin only when it renders. */}
      {!embedded ? (
        <div className="px-4 pt-6 desk:px-10 desk:pt-[34px]">{roleNav}</div>
      ) : (
        category === 'skills' || category === 'commands' || category === 'templates'
      ) ? (
        <div className="px-4 pt-4 desk:px-10">{roleNav}</div>
      ) : null}
      <div className="min-h-0 flex-1">
        {/* Standalone /system-hub has no filter row of its own, so the scope
            chip goes in HubShell's topBar slot — directly under the h1, the same
            position it holds on every other page. Embedded, SystemShell owns the
            chip and hands this hub its scopeSlug. */}
        <HubShell<RosterRow>
          {...(embedded ? {} : { title: t`System Hub`, topBar: <ScopeChip /> })}
          roster={visibleRoster}
          rosterError={rosterError}
          onRosterRetry={loadRoster}
          rowKey={rowKey}
          rowMatches={rowMatches}
          renderRow={(r) => renderRow(r)}
          selectedKey={selectedKey}
          onSelect={onSelect}
          searchPlaceholder={t`filter ${noun}…`}
          rosterEmptyLabel={
            // An active origin filter is the likely cause of an empty roster, so
            // name it instead of claiming the machine has none.
            originScope !== null
              ? t`no ${originScope}-scope ${noun} here`
              : projectScoped
                ? t`No ${noun} resolve for this project — enable a pack in Settings, or add one under .claude/.`
                : t`no ${noun} on this machine`
          }
          tabs={tabs}
          activeTab={activeTab}
          onTab={onTab}
          detailPlaceholder={
            <Empty>
              <Trans>select a {singularNoun} to see its profile</Trans>
            </Empty>
          }
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

/* ----- role nav (Toolkit · Hooks · Insights + Toolkit sub-tabs) ----- */

function RoleNav({
  category,
  summary,
  onCategory,
  embedded = false,
}: {
  category: HubCategory;
  summary: SystemHubSummary | null;
  onCategory: (c: HubCategory) => void;
  /** Embedded under the tabbed System shell: the shell owns the Toolkit/Hooks/
   * Insights role row, so render ONLY the Toolkit sub-pills (and nothing when
   * not in Toolkit). */
  embedded?: boolean;
}): JSX.Element | null {
  const { i18n, t } = useLingui();
  const inToolkit = (TOOLKIT as string[]).includes(category);
  // exactOptionalPropertyTypes: omit `badge` entirely when the summary is absent.
  const badge = (n: number | undefined): { badge?: number } => (n !== undefined ? { badge: n } : {});
  const toolkitBadge =
    summary === null ? undefined : summary.skills + summary.commands + summary.templates;
  const roles: { key: HubCategory; label: string; badge?: number; active: boolean }[] = [
    { key: 'skills', label: t`Toolkit`, ...badge(toolkitBadge), active: inToolkit },
    { key: 'hooks', label: t`Hooks`, ...badge(summary?.hooks), active: category === 'hooks' },
    { key: 'insights', label: t`Insights`, ...badge(summary?.insights), active: category === 'insights' },
  ];
  const toolkitPills = inToolkit ? (
    <div className="flex gap-1.5">
      {TOOLKIT.map((k) => {
        const b = summary === null ? undefined : k === 'skills' ? summary.skills : k === 'commands' ? summary.commands : summary.templates;
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
            {i18n._(CATEGORY_NOUN[k])}
            {b !== undefined ? ` ${String(b)}` : ''}
          </button>
        );
      })}
    </div>
  ) : null;

  // Embedded: only the Toolkit sub-pills (the outer shell owns the role row).
  if (embedded) return toolkitPills;

  return (
    <div className="space-y-2.5">
      <div className="flex gap-1 border-b border-line" role="tablist" aria-label={t`System Hub sections`}>
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
      {toolkitPills}
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
  const invalid = (
    <Empty>
      {category === 'skills' ? (
        <Trans>invalid skill</Trans>
      ) : category === 'commands' ? (
        <Trans>invalid command</Trans>
      ) : (
        <Trans>invalid hook</Trans>
      )}
    </Empty>
  );
  switch (category) {
    case 'skills':
      if (Number.isNaN(id)) return invalid;
      return (
        <SkillProfile
          id={id}
          tab={tab === 'docs' || tab === 'usage' || tab === 'definition' ? tab : 'overview'}
          projectId={scopeSlug}
          projectNames={projectNames}
          defRefresh={defRefresh}
          onDefinitionMutated={onDefinitionMutated}
        />
      );
    case 'commands':
      if (Number.isNaN(id)) return invalid;
      return (
        <CommandProfile
          id={id}
          tab={tab === 'docs' || tab === 'content' ? tab : 'overview'}
          projectId={scopeSlug}
        />
      );
    case 'hooks':
      if (Number.isNaN(id)) return invalid;
      return <HookProfile id={id} />;
    case 'templates':
      return <TemplateProfile name={selectedKey} projectId={scopeSlug} onCopied={onTemplateCopied} />;
    case 'insights':
      return <Empty>—</Empty>;
  }
}
