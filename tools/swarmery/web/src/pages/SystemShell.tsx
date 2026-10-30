// System (v4, handoff §3, §9): the single "System" destination in the frame every
// place shares. Its sections are the accordion groups of the list: Agents · Skills ·
// Commands · Templates · Hooks · Routines · Plugins · Mods · Insights. Skills,
// Commands and Templates were one tab with sub-pills; here each is a group.
//
// Routing: /system/:group(/:item) (fleet) and /p/:slug/system/:group(/:item)
// (workspace). The open group is the first path segment and the selected row the
// next one; `?tab=` stays the section of the detail. The old URLs all land: bare
// /system on Agents, the retired /system/toolkit(/…) on Skills (keeping the deeper
// path, so /system/skills/commands opens Commands), /agents(/:id) and
// /system-hub(/:category(/:id)) through the redirects in AgentHub and SystemHub.
//
// In PROJECT mode (a concrete :slug) the rosters are the project's EFFECTIVE system,
// its own components PLUS the global ones PLUS the built-ins of the packs it enables,
// never another project's locals. The narrowing is the server's; the origin filter
// under the list only slices what arrived.

import { Trans, useLingui } from '@lingui/react/macro';
import { useCallback, useEffect, useMemo, useState } from 'react';
import { Navigate, useLocation, useNavigate, useParams, useSearchParams } from 'react-router-dom';
import type { AgentRosterRow, Routine, SystemHubSummary, SystemSummary } from '../api/types';
import { fetchProjects, fetchRoutines } from '../api';
import { fetchAgentRoster } from '../api/agentHub';
import { fetchSystemSummary } from '../api/system';
import { fetchSystemHubSummary } from '../api/systemHub';
import { PageDetail, PagePlace, type PlaceGroup, type PlaceItem } from '../components/place/PageDetail';
import { ProjectPlugins } from '../components/ProjectPlugins';
import { ScopeChip } from '../components/ScopeChip';
import { Empty, Loading } from '../components/ui';
import { fmtAgo } from '../lib/format';
import { useProjectScope, useScope } from '../lib/scope';
import { useLiveUpdates } from '../lib/ws';
import { AgentDetail, agentItem } from './AgentHub';
import { RoutineDetail, Routines, describeCron } from './Routines';
import { CATALOG, type CatalogCategory, CatalogDetail, type RosterRow, fetchRoster, rosterItem, rowKey, rowScope } from './SystemHub';
import { InsightsTab } from './system/InsightsTab';
import { ModsTab } from './system/ModsTab';
import { FiltersRow } from './system/shared';
import { SkillsToExtendHint } from './system/SkillsToExtendHint';

/** Origin of a catalog row: where the component is DEFINED (the user's ~/.claude + the
 * plugin cache = global; a project's own .claude/ = project). */
type OriginScope = 'global' | 'project' | null;

const GROUPS = [
  'agents',
  'skills',
  'commands',
  'templates',
  'hooks',
  'routines',
  'plugins',
  'mods',
  'insights',
] as const;
type SystemGroup = (typeof GROUPS)[number];

/** Retired slugs → their new home (phase 8 renamed Toolkit to Skills). */
const RENAMED: Record<string, SystemGroup> = { toolkit: 'skills' };

/** Groups whose rows have an origin: the filter is hidden on the others. */
const ORIGIN_GROUPS: ReadonlySet<SystemGroup> = new Set(['agents', 'skills', 'commands', 'templates', 'hooks']);

const ALL_ROUTINES = 'all';

function isGroup(value: string): value is SystemGroup {
  return (GROUPS as readonly string[]).includes(value);
}

function isCatalog(value: SystemGroup): value is CatalogCategory {
  return (CATALOG as readonly string[]).includes(value);
}

/** `skills/commands/12` is how a deep toolkit link spells Commands, row 12. */
function parsePath(splat: string): { group: SystemGroup | null; item: string | null } {
  const [first = '', second, third] = splat.split('/');
  if (first === 'skills' && (second === 'commands' || second === 'templates')) {
    return { group: second, item: third ?? null };
  }
  return { group: isGroup(first) ? first : null, item: second === undefined || second === '' ? null : second };
}

/** A category is absent until its roster lands: "loading" must not read as "none". */
type Rosters = Partial<Record<CatalogCategory, RosterRow[]>>;

export function SystemShell(): JSX.Element {
  const { t } = useLingui();
  const params = useParams();
  const navigate = useNavigate();
  const location = useLocation();
  const [searchParams, setSearchParams] = useSearchParams();
  const { scope } = useScope();

  const slug = params.slug;
  const projectScoped = slug !== undefined;
  // The scope REFERENCE (URL slug or the global scope chip), resolved to the numeric
  // id every /api/system/* endpoint accepts. scopePending gates each fetch: the
  // unscoped rosters are the whole machine, and firing them while the project list
  // resolves lets them land last and show the fleet over a project page.
  const { id: scopeSlug, pending: scopePending } = useProjectScope(slug ?? scope);
  const base = projectScoped ? `/p/${slug}/system` : '/system';

  const [originScope, setOriginScope] = useState<OriginScope>(null);
  const [agents, setAgents] = useState<AgentRosterRow[] | null>(null);
  const [rosters, setRosters] = useState<Rosters>({});
  const [routines, setRoutines] = useState<Routine[]>([]);
  const [summary, setSummary] = useState<SystemHubSummary | null>(null);
  const [projectNames, setProjectNames] = useState<Record<string, string>>({});
  const [refreshKey, setRefreshKey] = useState(0);
  // Coverage is a separate fetch on purpose: the hub summary carries only the
  // counts, the docs block lives on /api/system/summary. "We went through every
  // item" has to be a number on the page, not a claim in a commit message.
  const [docs, setDocs] = useState<SystemSummary['docs'] | null>(null);

  const load = useCallback((): void => {
    if (scopePending) return;
    fetchAgentRoster(scopeSlug ?? undefined)
      .then((r) => setAgents(r.agents))
      .catch(() => setAgents([]));
    for (const category of CATALOG) {
      fetchRoster(category, scopeSlug)
        .then((rows) => setRosters((prev) => ({ ...prev, [category]: rows })))
        .catch(() => setRosters((prev) => ({ ...prev, [category]: [] })));
    }
    fetchRoutines(scopeSlug === null ? undefined : Number(scopeSlug))
      .then(setRoutines)
      .catch(() => setRoutines([]));
    fetchSystemHubSummary(scopeSlug ?? undefined)
      .then(setSummary)
      .catch(() => setSummary(null));
  }, [scopeSlug, scopePending]);
  useEffect(load, [load]);

  useEffect(() => {
    let cancelled = false;
    fetchSystemSummary()
      .then((s) => {
        if (!cancelled) setDocs(s.docs);
      })
      .catch(() => {
        if (!cancelled) setDocs(null);
      });
    fetchProjects()
      .then((ps) => {
        if (!cancelled) setProjectNames(Object.fromEntries(ps.map((p) => [p.slug, p.name ?? p.slug])));
      })
      .catch(() => {
        if (!cancelled) setProjectNames({});
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const changed = useCallback((): void => {
    setRefreshKey((k) => k + 1);
    load();
  }, [load]);
  useLiveUpdates((msg) => {
    if (msg.type === 'system_item_updated') changed();
  }, changed);

  const visibleAgents = useMemo(
    () => (agents ?? []).filter((a) => originScope === null || a.scope === originScope),
    [agents, originScope],
  );
  const visibleRoster = useCallback(
    (category: CatalogCategory): RosterRow[] =>
      (rosters[category] ?? []).filter((r) => originScope === null || rowScope(r) === originScope),
    [rosters, originScope],
  );

  const splat = params['*'] ?? '';
  const renamed = RENAMED[splat.split('/')[0] ?? ''];
  const { group, item } = parsePath(splat);

  // Retired /system/toolkit(/…) → the same place under Skills, keeping any deeper
  // selection and the query. `replace` so back doesn't loop.
  if (renamed !== undefined) {
    const rest = splat.split('/').slice(1).join('/');
    return (
      <Navigate to={{ pathname: `${base}/${renamed}${rest !== '' ? `/${rest}` : ''}`, search: location.search }} replace />
    );
  }
  if (group === null) return <Navigate to={`${base}/agents`} replace />;

  const onTab = (tab: string): void => {
    setSearchParams(
      (prev) => {
        const next = new URLSearchParams(prev);
        if (tab === 'overview') next.delete('tab');
        else next.set('tab', tab);
        return next;
      },
      { replace: true },
    );
  };

  const routineItem = (routine: Routine): PlaceItem => ({
    key: routine.id,
    glyph: '↻',
    glyphTone: routine.enabled ? 'faint' : 'amber',
    title: routine.name,
    summary: describeCron(routine.cronExpr),
    ...(routine.lastRunAt ? { age: fmtAgo(routine.lastRunAt) } : {}),
  });

  const groups: PlaceGroup[] = [
    {
      key: 'agents',
      label: t`Agents`,
      items: visibleAgents.map((agent) => {
        const runs = String(agent.runs30d);
        return agentItem(agent, projectNames, t`${runs} runs / 30 d`);
      }),
    },
    { key: 'skills', label: t`Skills`, items: visibleRoster('skills').map(rosterItem) },
    { key: 'commands', label: t`Commands`, items: visibleRoster('commands').map(rosterItem) },
    { key: 'templates', label: t`Templates`, items: visibleRoster('templates').map(rosterItem) },
    { key: 'hooks', label: t`Hooks`, items: visibleRoster('hooks').map(rosterItem) },
    {
      key: 'routines',
      label: t`Routines`,
      count: routines.length,
      items: [
        ...routines.map(routineItem),
        { key: ALL_ROUTINES, glyph: '↻', title: t`All routines`, summary: t`create · schedule · run history` },
      ],
    },
    {
      key: 'plugins',
      label: t`Plugins`,
      items: [{ key: 'list', glyph: '▦', title: t`Plugins`, summary: t`which packs a project enables` }],
    },
    {
      key: 'mods',
      label: t`Mods`,
      items: [{ key: 'audit', glyph: '◇', title: t`Mods audit`, summary: t`every mod on this machine` }],
    },
    {
      key: 'insights',
      label: t`Insights`,
      ...(summary !== null ? { count: summary.insights } : {}),
      items: [{ key: 'inbox', glyph: '◆', title: t`Insights`, summary: t`promotion · drift · dead components` }],
    },
  ];

  const open = groups.find((g) => g.key === group);
  const selected = item ?? open?.items[0]?.key ?? null;

  const emptyText = (): string => {
    if (originScope !== null) return t`no ${originScope}-scope entries here`;
    if (projectScoped) return t`Nothing resolves for this project — enable a pack in Settings, or add one under .claude/.`;
    return t`nothing on this machine`;
  };

  const detail = (): JSX.Element => {
    if (group === 'agents' || isCatalog(group)) {
      const loading = group === 'agents' ? agents === null : rosters[group] === undefined;
      if (loading && item === null) return <Loading label={t`roster…`} />;
      if (selected === null) return <Empty>{emptyText()}</Empty>;
    }
    if (group === 'agents') {
      if (selected === null || !/^\d+$/.test(selected)) return <Empty>{emptyText()}</Empty>;
      return (
        <AgentDetail
          key={selected}
          id={Number(selected)}
          tabParam={searchParams.get('tab')}
          onTab={onTab}
          scopeSlug={scopeSlug}
          projectNames={projectNames}
          healthHref={projectScoped ? `/p/${slug}/health?tab=agents` : '/health?tab=agents'}
          refreshKey={refreshKey}
          onChanged={changed}
        />
      );
    }
    if (isCatalog(group)) {
      return (
        <CatalogDetail
          key={`${group}:${String(selected)}`}
          category={group}
          selectedKey={selected ?? ''}
          row={rosters[group]?.find((r) => rowKey(r) === selected)}
          tabParam={searchParams.get('tab')}
          onTab={onTab}
          scopeSlug={scopeSlug}
          projectNames={projectNames}
          defRefresh={refreshKey}
          onChanged={changed}
        />
      );
    }
    if (group === 'routines') {
      const routine = routines.find((r) => r.id === selected);
      if (routine !== undefined) {
        return (
          <RoutineDetail
            key={routine.id}
            routine={routine}
            onChanged={load}
            onDeleted={() => {
              load();
              navigate(`${base}/routines`, { replace: true });
            }}
          />
        );
      }
      return (
        <PageDetail kind={t`system · routines`} title={t`All routines`}>
          <div className="mt-[18px]">
            <Routines embedded onChanged={load} />
          </div>
        </PageDetail>
      );
    }
    if (group === 'plugins') {
      // Plugins are enabled per project: the fleet has no single list to show.
      return (
        <PageDetail
          kind={t`system · plugins`}
          title={t`Plugins`}
          {...(projectScoped ? {} : { lead: t`Plugins are enabled per project — pick one in the switcher.` })}
        >
          {projectScoped && (
            <div className="mt-[18px]">
              {scopeSlug === null ? <Loading label={t`plugins…`} /> : <ProjectPlugins projectId={Number(scopeSlug)} />}
            </div>
          )}
        </PageDetail>
      );
    }
    if (group === 'mods') {
      // A machine-wide audit (the plugin cache + CLAUDE_CODE_PLUGIN_DIRS), so a
      // project scope lists the same set.
      return (
        <PageDetail kind={t`system · mods`} title={t`Mods audit`}>
          <div className="mt-[18px]">
            <ModsTab />
          </div>
        </PageDetail>
      );
    }
    return (
      <PageDetail kind={t`system · insights`} title={t`Insights`}>
        <div className="mt-[18px]">
          <InsightsTab refreshKey={refreshKey} projectNames={projectNames} projectId={scopeSlug} />
        </div>
      </PageDetail>
    );
  };

  const counts = {
    agents: String(agents?.length ?? 0),
    skills: String(rosters.skills?.length ?? 0),
    hooks: String(rosters.hooks?.length ?? 0),
    routines: String(routines.length),
  };
  const { agents: agentCount, skills: skillCount, hooks: hookCount, routines: routineCount } = counts;

  return (
    <PagePlace
      title={t`System`}
      sub={t`${agentCount} agents · ${skillCount} skills · ${hookCount} hooks · ${routineCount} routines`}
      searchHint={t`filter`}
      groups={groups}
      openGroup={group}
      selected={selected}
      onOpenGroup={(key) => navigate(`${base}/${key}`)}
      onSelect={(key, groupKey) =>
        navigate(`${base}/${groupKey}/${encodeURIComponent(key)}${location.search}`, { replace: true })
      }
      detail={detail()}
      after={
        <div className="mt-2 flex flex-col gap-2 border-t border-line px-[14px] py-3">
          <ScopeChip />
          {ORIGIN_GROUPS.has(group) && <FiltersRow inline scope={originScope} onScope={setOriginScope} />}
          {docs !== null && docs.total > 0 && (
            <span
              className="font-mono text-[10.5px] text-ink-faint"
              data-tip={t`usage-guide coverage: every agent, skill and command the marketplace ships must carry a '# How to use' block`}
            >
              <span className={docs.documented === docs.total ? 'text-green' : 'text-amber'}>
                {docs.documented}/{docs.total}
              </span>
              <span> <Trans>documented ·</Trans> </span>
              <span className={docs.reviewed === docs.total ? 'text-green' : 'text-amber'}>
                {docs.reviewed}/{docs.total}
              </span>
              <span> <Trans>reviewed</Trans></span>
            </span>
          )}
          {/* The moment someone browses agents is the moment to ask whether a skill
              already does it. */}
          {group === 'agents' && <SkillsToExtendHint insightsHref={`${base}/insights#skill-audit`} />}
        </div>
      }
    />
  );
}
