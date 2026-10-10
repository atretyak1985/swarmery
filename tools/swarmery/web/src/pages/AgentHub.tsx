// Agent Hub (fusion phase 17): agent-centric roster + per-agent tabbed profile,
// aggregating what already exists across System (definition/versions), Retro
// (scorecards/lessons/proposals), Analytics (cost) and Sessions (runs). Built on
// the reusable HubShell (pages/agent-hub/HubShell.tsx) so the System Hub (phase
// 18) can extend the same split-pane pattern.
//
// Routing: /agents (fleet roster) + /agents/:id (fleet, agent selected), and the
// workspace mirror /p/:slug/agents(/:id). The selected agent is the :id route
// param; the active tab is ?tab=. Definition editing is NOT reimplemented — the
// Definition tab embeds the existing System editor (SystemItemPanel), which owns
// the versioned write surface (edit / versions / diff / rollback).

import type { MessageDescriptor } from '@lingui/core';
import { msg } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { useCallback, useEffect, useMemo, useState } from 'react';
import { useNavigate, useParams, useSearchParams } from 'react-router-dom';
import type { AgentProfile, AgentRosterRow, WSMessage } from '../api/types';
import { fetchAgentProfile, fetchAgentRoster } from '../api/agentHub';
import { fetchProjects } from '../api';
import { fmtAgo, fmtCost } from '../lib/format';
import { ScopeChip } from '../components/ScopeChip';
import { useProjectScope, useScope } from '../lib/scope';
import { useLiveUpdates } from '../lib/ws';
import { Empty, ErrorBox, Loading } from '../components/ui';
import { SystemItemPanel } from './system/ItemDetail';
import { FiltersRow } from './system/shared';
import { HubShell, healthTone, type HubTab } from './agent-hub/HubShell';
import { ActivityTab, InsightsTab, OverviewTab, RunsTab, TasksTab } from './agent-hub/Tabs';
import { RunNowButton } from './agent-hub/RunNow';
import { boardParked } from '../lib/parked';

type ProfileTab = 'overview' | 'docs' | 'runs' | 'tasks' | 'activity' | 'insights' | 'definition';
const TABS: ProfileTab[] = [
  'overview',
  'docs',
  'runs',
  'tasks',
  'activity',
  'insights',
  'definition',
];
const TAB_LABELS: Record<ProfileTab, MessageDescriptor> = {
  overview: msg`Overview`,
  docs: msg`Docs`,
  runs: msg`Runs`,
  tasks: msg`Tasks`,
  activity: msg`Activity`,
  insights: msg`Insights`,
  definition: msg`Definition`,
};

function parseTab(value: string | null): ProfileTab {
  return (TABS as string[]).includes(value ?? '') ? (value as ProfileTab) : 'overview';
}

/* ----- roster card ----- */

function RosterCard({
  agent,
  projectNames,
}: {
  agent: AgentRosterRow;
  projectNames: Record<string, string>;
}): JSX.Element {
  const { t } = useLingui();
  const health = healthTone(agent.failedShare);
  const healthLabel = health.label;
  const failedPct = Math.round(agent.failedShare * 100);
  const runs30d = String(agent.runs30d);
  const ago = agent.lastActiveAt !== null ? fmtAgo(agent.lastActiveAt) : null;
  // Where the row comes FROM, which is the question a project roster raises now
  // that it lists the effective set (own + global + enabled packs):
  //   project row  → the OWNING project (several projects can define an agent
  //                  with the same name, so the bare word "project" can't tell
  //                  those rows apart);
  //   plugin row   → the pack that ships it ("core", "uav-pack", …);
  //   otherwise    → "global" (the user's own ~/.claude).
  const scopeBadge =
    agent.scope === 'project' && agent.projectSlug !== null
      ? (projectNames[agent.projectSlug] ?? agent.projectSlug)
      : agent.origin === 'plugin' && agent.pluginName !== null
        ? agent.pluginName
        : agent.scope;
  return (
    <>
      <div className="flex flex-wrap items-center gap-2">
        <span
          className={`inline-block h-[8px] w-[8px] shrink-0 rounded-full ${health.dot}`}
          data-tip={t`${healthLabel} · ${failedPct}% failed-run share`}
        />
        <span className="text-[13.5px] font-semibold text-ink">{agent.name}</span>
        {agent.model !== null && (
          <span className="font-mono text-[10px] text-ink-faint">{agent.model}</span>
        )}
        <span
          className="ml-auto max-w-[140px] truncate rounded-[6px] border border-line-strong px-1.5 py-[1px] font-mono text-[9.5px] text-ink-dim"
          data-tip={agent.scope === 'project' ? (agent.projectSlug ?? undefined) : undefined}
        >
          {scopeBadge}
        </span>
      </div>
      <div className="mt-1.5 flex flex-wrap items-center gap-x-3 font-mono text-[10px] text-ink-faint">
        <span className="whitespace-nowrap">
          <Trans>runs 30d {runs30d}</Trans>
        </span>
        <span className="whitespace-nowrap">{fmtCost(agent.cost30d)}</span>
        <span className="whitespace-nowrap">
          {ago !== null ? t`active ${ago}` : t`idle`}
        </span>
      </div>
    </>
  );
}

/* ----- detail header (identity + Run now + Definition link) ----- */

function ProfileHeader({
  agent,
  scopeSlug,
}: {
  agent: AgentProfile;
  scopeSlug: string | null;
}): JSX.Element {
  const health = healthTone(agent.failedShare);
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
      <span className={`inline-block h-[9px] w-[9px] shrink-0 rounded-full ${health.dot}`} />
      <span className="font-display text-[18px] leading-none font-medium text-ink">{agent.name}</span>
      {agent.model !== null && (
        <span className="font-mono text-[11px] text-ink-dim">{agent.model}</span>
      )}
      <span className="font-mono text-[10px] text-ink-faint">{health.label}</span>
      <span className="ml-auto flex items-center gap-2">
        {!boardParked() && <RunNowButton agentName={agent.name} scopeSlug={scopeSlug} />}
      </span>
    </div>
  );
}

/* ----- the page ----- */

/** Props are all optional so the standalone /agents and /p/:slug/agents mounts
 * behave exactly as before. The tabbed System shell (pages/SystemShell.tsx)
 * mounts this EMBEDDED: it supplies its own outer heading + owns the URL base,
 * so it passes `embedded` (suppress the roster heading + gate the detail rail on
 * selection + show the scope segmented control), an explicit `routeBase` (so the
 * component's internal navigation stays on the /system/agents tree) and the
 * resolved `scopeSlug`. */
export function AgentHub({
  embedded = false,
  routeBase: routeBaseProp,
  scopeSlug: scopeSlugProp,
  projectScoped = false,
  originScope,
}: {
  embedded?: boolean;
  routeBase?: string;
  scopeSlug?: string | null;
  /** PROJECT mode (/p/:slug/system/agents): the roster is the project's
   * EFFECTIVE set — the server narrows it via ?projectId= (own + global +
   * enabled packs). Only the empty-state copy differs from fleet mode. */
  projectScoped?: boolean;
  /** Origin-scope filter OWNED BY THE CALLER (SystemShell renders one row of
   * chips above its tab bar so the same filter applies to every tab). When
   * supplied this hub renders no chips of its own; when absent (the standalone
   * /agents mount) it keeps its own local chips. */
  originScope?: 'global' | 'project' | null;
} = {}): JSX.Element {
  const { i18n, t } = useLingui();
  const params = useParams();
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const { scope } = useScope();
  // Workspace mount (/p/:slug/agents) carries the slug in the route; fleet mount
  // uses the global scope switcher. Either narrows the rollup window. When
  // embedded, the shell passes both explicitly (it owns the route base).
  // Resolved to the numeric project id before it reaches the API: /api/agents/hub
  // matches slug-or-id-or-name and would accept the pretty slug, but the sibling
  // /api/system/* template endpoints do NOT — one convention for the whole hub
  // family beats two that differ only where it breaks (see useProjectScope).
  // `scopePending` is load-bearing, not a nicety: the unscoped roster is the
  // WHOLE fleet, so firing it while the project list resolves lets it land last
  // and overwrite the scoped one.
  const { id: scopeSlug, pending: scopePending } = useProjectScope(
    scopeSlugProp !== undefined ? scopeSlugProp : (params.slug ?? scope),
  );
  const routeBase =
    routeBaseProp ?? (params.slug !== undefined ? `/p/${params.slug}/agents` : '/agents');

  // Origin scope (all scopes / global / project), filtered CLIENT-SIDE against
  // AgentRosterRow.scope (no API change). The shell-embedded mount takes the
  // value from its caller — SystemShell hoisted the chips above the tab bar so
  // one control narrows Agents, Toolkit and Hooks alike; the standalone /agents
  // mount keeps this local state and renders the chips itself.
  const [localScopeChip, setLocalScopeChip] = useState<'global' | 'project' | null>(null);
  const scopeChip = originScope !== undefined ? originScope : localScopeChip;

  // Selected agent: standalone reads it from the /agents/:id route; embedded
  // keeps it in LOCAL state (the shell's /system/* route has no :id segment, so
  // params.id is never populated under it). The detail sub-tab stays on ?tab=.
  const [embSelId, setEmbSelId] = useState<number | null>(null);
  const routeSelId = params.id !== undefined && /^\d+$/.test(params.id) ? Number(params.id) : null;
  const selectedId = embedded ? embSelId : routeSelId;
  const tab = parseTab(searchParams.get('tab'));

  const [roster, setRoster] = useState<AgentRosterRow[] | null>(null);
  const [rosterError, setRosterError] = useState<string | null>(null);
  const [profile, setProfile] = useState<AgentProfile | null>(null);
  const [profileError, setProfileError] = useState<string | null>(null);
  const [defRefresh, setDefRefresh] = useState(0);
  const [projectNames, setProjectNames] = useState<Record<string, string>>({});

  const loadRoster = useCallback((): void => {
    if (scopePending) return; // see scopePending above — never fetch unscoped first
    setRosterError(null);
    fetchAgentRoster(scopeSlug ?? undefined)
      .then((r) => setRoster(r.agents))
      .catch((e: unknown) => setRosterError(String(e)));
  }, [scopeSlug, scopePending]);
  useEffect(loadRoster, [loadRoster]);

  useEffect(() => {
    fetchProjects()
      .then((ps) => setProjectNames(Object.fromEntries(ps.map((p) => [p.slug, p.name ?? p.slug]))))
      .catch(() => setProjectNames({}));
  }, []);

  const loadProfile = useCallback((): void => {
    if (selectedId === null) {
      setProfile(null);
      return;
    }
    if (scopePending) return;
    setProfileError(null);
    fetchAgentProfile(selectedId, scopeSlug ?? undefined)
      .then(setProfile)
      .catch((e: unknown) => setProfileError(String(e)));
  }, [selectedId, scopeSlug, scopePending]);
  useEffect(loadProfile, [loadProfile]);

  // Live: a registry edit (WS system_item_updated) refetches the roster, the
  // profile, and bumps the embedded Definition editor — the same invalidation
  // hint the System page uses (payload carries ids only).
  const onMessage = useCallback(
    (msg: WSMessage): void => {
      if (msg.type === 'system_item_updated') {
        loadRoster();
        loadProfile();
        setDefRefresh((k) => k + 1);
      }
    },
    [loadRoster, loadProfile],
  );
  const resync = useCallback((): void => {
    loadRoster();
    loadProfile();
  }, [loadRoster, loadProfile]);
  useLiveUpdates(onMessage, resync);

  const onSelect = useCallback(
    (key: string | null): void => {
      if (embedded) {
        setEmbSelId(key === null || !/^\d+$/.test(key) ? null : Number(key));
        return;
      }
      navigate(key === null ? routeBase : `${routeBase}/${key}${window.location.search}`);
    },
    [embedded, navigate, routeBase],
  );
  const onTab = useCallback(
    (id: string): void => {
      setSearchParams(
        (prev) => {
          const next = new URLSearchParams(prev);
          if (id === 'overview') next.delete('tab');
          else next.set('tab', id);
          return next;
        },
        { replace: true },
      );
    },
    [setSearchParams],
  );

  // Not memoised: the labels resolve through the active locale on every render.
  const tabs: HubTab[] = TABS.map((tabId) => {
    const badge =
      tabId === 'insights' && profile !== null
        ? profile.insights.recommendations.length +
          profile.insights.proposals.length +
          profile.insights.lessons.length
        : tabId === 'runs' && profile !== null
          ? profile.runs.length
          : undefined;
    return { id: tabId, label: i18n._(TAB_LABELS[tabId]), ...(badge !== undefined ? { badge } : {}) };
  });

  const rowMatches = useCallback(
    (a: AgentRosterRow, q: string): boolean =>
      [a.name, a.model, a.description].some((v) => v != null && v.toLowerCase().includes(q)),
    [],
  );

  // Client-side origin-scope filter. The ROSTER ITSELF is already narrowed by
  // the server: /api/agents/hub?projectId= returns the agents EFFECTIVE in that
  // project (its own + global + the packs it enables), so another project's
  // local agents never arrive here. These chips only slice what did arrive:
  // scopeChip === null ("all scopes") passes the roster through untouched.
  const visibleRoster = useMemo(() => {
    if (roster === null || scopeChip === null) return roster;
    return roster.filter((a) => a.scope === scopeChip);
  }, [roster, scopeChip]);

  // Scope segmented control (all scopes / global / project) — reuses the System
  // page's chips. Rendered in HubShell's full-width top bar, like the toolkit
  // catalog. It stays in PROJECT mode too: with the effective roster there, the
  // chips are how you narrow to the project's OWN agents.
  // The PROJECT-scope chip leads that row on the standalone /agents mount only:
  // embedded, SystemShell already renders one above the tab bar, and two chips
  // driving the same context on one screen is a bug, not a convenience — which
  // is also why the ORIGIN chips disappear once the caller owns them.
  const scopeFilters =
    originScope !== undefined ? undefined : (
      <div className="flex flex-wrap items-center gap-2">
        {!embedded && <ScopeChip />}
        <FiltersRow scope={scopeChip} onScope={setLocalScopeChip} />
      </div>
    );

  return (
    <HubShell<AgentRosterRow>
      {...(embedded ? {} : { title: t`Agents` })}
      hideDetailWhenUnselected={false}
      roster={visibleRoster}
      rosterError={rosterError}
      onRosterRetry={loadRoster}
      rowKey={(a) => String(a.id)}
      rowMatches={rowMatches}
      renderRow={(a) => <RosterCard agent={a} projectNames={projectNames} />}
      selectedKey={selectedId === null ? null : String(selectedId)}
      onSelect={onSelect}
      topBar={scopeFilters}
      searchPlaceholder={t`filter agents…`}
      rosterEmptyLabel={
        // An active origin filter is the likely cause of an empty roster, so
        // name it instead of claiming the machine has none.
        scopeChip !== null
          ? t`no ${scopeChip}-scope agents here`
          : projectScoped
            ? t`No agents resolve for this project — enable a pack in Settings, or add one under .claude/agents/.`
            : t`no agents on this machine`
      }
      tabs={tabs}
      activeTab={tab}
      onTab={onTab}
      detailHeader={profile !== null ? <ProfileHeader agent={profile} scopeSlug={scopeSlug} /> : undefined}
      detailPlaceholder={
        <Empty>
          <Trans>select an agent to see its profile</Trans>
        </Empty>
      }
    >
      {selectedId !== null && (
        <ProfilePanel
          tab={tab}
          selectedId={selectedId}
          profile={profile}
          profileError={profileError}
          onProfileRetry={loadProfile}
          projectNames={projectNames}
          scopeSlug={scopeSlug}
          defRefresh={defRefresh}
          onDefinitionMutated={() => {
            setDefRefresh((k) => k + 1);
            loadRoster();
            loadProfile();
          }}
        />
      )}
    </HubShell>
  );
}

/** The active tab's panel. Definition mounts the existing System editor; every
 * other tab renders a slice of the aggregated profile bundle. */
function ProfilePanel({
  tab,
  selectedId,
  profile,
  profileError,
  onProfileRetry,
  projectNames,
  scopeSlug,
  defRefresh,
  onDefinitionMutated,
}: {
  tab: ProfileTab;
  selectedId: number;
  profile: AgentProfile | null;
  profileError: string | null;
  onProfileRetry: () => void;
  projectNames: Record<string, string>;
  scopeSlug: string | null;
  defRefresh: number;
  onDefinitionMutated: () => void;
}): JSX.Element {
  const { t } = useLingui();
  // Docs tab: the agent's `# How to use` guide. Same panel, same registry id,
  // 'docs' variant — the hub's own tab bar is the section switcher here, so
  // the panel renders the guide alone. Deep-link is the native ?tab=docs.
  if (tab === 'docs') {
    return (
      <SystemItemPanel
        kind="agents"
        id={selectedId}
        refreshKey={defRefresh}
        projectNames={projectNames}
        onClose={() => undefined}
        onMutated={onDefinitionMutated}
        onDeleted={onDefinitionMutated}
        onReadonly={() => undefined}
        variant="docs"
      />
    );
  }

  // Definition tab: reuse the existing versioned System editor verbatim. It
  // fetches its own detail by the SAME registry id, so it works standalone —
  // create/delete stay on the System page (this is edit/versions/rollback only).
  if (tab === 'definition') {
    return (
      <SystemItemPanel
        kind="agents"
        id={selectedId}
        refreshKey={defRefresh}
        projectNames={projectNames}
        onClose={() => undefined}
        onMutated={onDefinitionMutated}
        onDeleted={onDefinitionMutated}
        onReadonly={() => undefined}
        variant="editor"
      />
    );
  }

  if (profileError !== null) return <ErrorBox message={profileError} onRetry={onProfileRetry} />;
  if (profile === null) return <Loading label={t`profile…`} />;

  switch (tab) {
    case 'runs':
      return <RunsTab runs={profile.runs} />;
    case 'tasks':
      return <TasksTab tasks={profile.tasks} projectSlug={scopeSlug} />;
    case 'activity':
      return <ActivityTab activity={profile.activity} />;
    case 'insights':
      return <InsightsTab insights={profile.insights} />;
    default:
      // Overview leads with the moved History + Versions (SystemItemPanel meta),
      // then the open-insights preview.
      return (
        <div className="space-y-4">
          <SystemItemPanel
            kind="agents"
            id={selectedId}
            refreshKey={defRefresh}
            projectNames={projectNames}
            onClose={() => undefined}
            onMutated={onDefinitionMutated}
            onDeleted={onDefinitionMutated}
            onReadonly={() => undefined}
            variant="meta"
          />
          <OverviewTab topInsights={profile.insights.recommendations} />
        </div>
      );
  }
}
