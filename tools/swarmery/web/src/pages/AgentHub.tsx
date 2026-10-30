// The agent half of the System place (handoff §9). `AgentHub` is what the old
// /agents(/:id) routes still mount: it lands on /system/agents, keeping the id and
// `?tab=`. `AgentDetail` is the page itself: model · prompt vN · tools · permission
// tiers from the agent's registry row, then the profile sections that the blocks
// cannot express, which keep `?tab=`. Definition editing is NOT reimplemented: the
// Definition section embeds the existing versioned editor (SystemItemPanel).

import type { MessageDescriptor } from '@lingui/core';
import { msg } from '@lingui/core/macro';
import { useLingui } from '@lingui/react/macro';
import { useCallback, useEffect, useState } from 'react';
import { Navigate, useLocation, useParams } from 'react-router-dom';
import type { AgentProfile, AgentRosterRow, SystemItemDetail } from '../api/types';
import { fetchAgentProfile } from '../api/agentHub';
import { fetchSystemItemDetail } from '../api/system';
import { type PageKv, PageDetail, type PlaceItem, actionClass } from '../components/place/PageDetail';
import { fmtAgo, fmtCost } from '../lib/format';
import { ErrorBox, Loading } from '../components/ui';
import { boardParked } from '../lib/parked';
import { SystemItemPanel } from './system/ItemDetail';
import { DetailSections } from './system/SectionTabs';
import { parseFrontmatter } from './system/shared';
import { healthTone } from './agent-hub/HubShell';
import { ActivityTab, InsightsTab, OverviewTab, RunsTab, TasksTab } from './agent-hub/Tabs';
import { RunNowButton } from './agent-hub/RunNow';

type ProfileTab = 'overview' | 'docs' | 'runs' | 'tasks' | 'activity' | 'insights' | 'definition';
const TABS: readonly ProfileTab[] = ['overview', 'docs', 'runs', 'tasks', 'activity', 'insights', 'definition'];
const TAB_LABELS: Record<ProfileTab, MessageDescriptor> = {
  overview: msg`Overview`,
  docs: msg`Docs`,
  runs: msg`Runs`,
  tasks: msg`Tasks`,
  activity: msg`Activity`,
  insights: msg`Insights`,
  definition: msg`Definition`,
};

/** No `?tab=`, or one this page lacks: every section folded (the design's page). */
function parseTab(value: string | null): ProfileTab | null {
  return (TABS as readonly string[]).includes(value ?? '') ? (value as ProfileTab) : null;
}

/** The old routes land on the one place. */
export function AgentHub(): JSX.Element {
  const { slug, id } = useParams();
  const { search } = useLocation();
  const base = slug !== undefined ? `/p/${slug}/system/agents` : '/system/agents';
  return <Navigate to={{ pathname: id !== undefined ? `${base}/${id}` : base, search }} replace />;
}

/** Where the row comes FROM, which is what a project's effective roster raises:
 * the owning project, the pack that ships it, or "global" (the user's ~/.claude). */
function originLabel(agent: AgentRosterRow, projectNames: Record<string, string>): string {
  if (agent.scope === 'project' && agent.projectSlug !== null) {
    return projectNames[agent.projectSlug] ?? agent.projectSlug;
  }
  return agent.origin === 'plugin' && agent.pluginName !== null ? agent.pluginName : agent.scope;
}

export function agentItem(agent: AgentRosterRow, projectNames: Record<string, string>, runsLabel: string): PlaceItem {
  const health = healthTone(agent.failedShare);
  const sick = health.tone !== 'green';
  return {
    key: String(agent.id),
    glyph: '☷',
    title: agent.name,
    tag: originLabel(agent, projectNames),
    summary: [agent.model, runsLabel].filter((part) => part !== null).join(' · '),
    ...(agent.lastActiveAt !== null ? { age: fmtAgo(agent.lastActiveAt) } : {}),
    ...(sick ? { right: `${String(Math.round(agent.failedShare * 100))}%`, rightTone: health.tone } : {}),
  };
}

/** `tools: [Read, Edit]`, `tools: Read, Edit` or a YAML list, as one mono line. */
function listValue(raw: string): string {
  return raw
    .replace(/^\[|\]$/g, '')
    .split(/[,\n]/)
    .map((part) => part.replace(/^-\s*/, '').trim())
    .filter((part) => part !== '')
    .join(' · ');
}

export function AgentDetail({
  id,
  tabParam,
  onTab,
  scopeSlug,
  projectNames,
  healthHref,
  refreshKey,
  onChanged,
}: {
  id: number;
  /** The raw `?tab=` value. */
  tabParam: string | null;
  onTab: (tab: string | null) => void;
  scopeSlug: string | null;
  projectNames: Record<string, string>;
  healthHref: string;
  /** Bumped on a registry edit: refetches and refreshes the embedded editor. */
  refreshKey: number;
  onChanged: () => void;
}): JSX.Element {
  const { t, i18n } = useLingui();
  const tab = parseTab(tabParam);
  const [profile, setProfile] = useState<AgentProfile | null>(null);
  const [item, setItem] = useState<SystemItemDetail | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback((): void => {
    setError(null);
    fetchAgentProfile(id, scopeSlug ?? undefined)
      .then(setProfile)
      .catch((e: unknown) => setError(String(e)));
    fetchSystemItemDetail('agents', id)
      .then(setItem)
      .catch(() => setItem(null));
  }, [id, scopeSlug]);
  useEffect(load, [load, refreshKey]);

  if (error !== null) return <ErrorBox message={error} onRetry={load} />;
  if (profile === null) return <Loading label={t`profile…`} />;

  const frontmatter = new Map((parseFrontmatter(item?.frontmatter ?? '') ?? []).map((row) => [row.key, row.value]));
  const pick = (key: string): string | undefined => {
    const value = frontmatter.get(key);
    return value === undefined || value === '' ? undefined : value;
  };
  const tools = pick('tools');
  const effort = pick('effort');
  const permission = pick('permissionMode');
  const isolation = pick('isolation');
  const versions = item?.versions ?? [];
  const latest = versions[0];
  const version = versions.length > 0 ? `v${String(versions.length)}` : null;
  const changed = latest !== undefined ? fmtAgo(latest.createdAt) : null;
  const health = healthTone(profile.failedShare);
  const failed = String(Math.round(profile.failedShare * 100));
  const runs = String(profile.runs30d);
  const healthLabel = health.label;
  const promptLabel = version !== null ? t`prompt ${version}` : null;
  const runsLabel = t`${runs} runs / 30 d`;

  const kv: PageKv[] = [
    ...(profile.model !== null ? [{ k: t`model`, v: profile.model }] : []),
    ...(version !== null ? [{ k: t`prompt`, v: changed !== null ? t`${version} · changed ${changed}` : version }] : []),
    ...(tools !== undefined ? [{ k: t`tools`, v: listValue(tools) }] : []),
    ...(permission !== undefined ? [{ k: t`permission tiers`, v: permission }] : []),
    ...(effort !== undefined ? [{ k: t`effort`, v: effort }] : []),
    { k: t`health`, v: t`${healthLabel} · ${failed}% failed`, tone: health.tone },
    { k: t`cost · 30 d`, v: fmtCost(profile.cost30d) },
  ];

  const labels = Object.fromEntries(
    TABS.map((id_) => {
      const badge =
        id_ === 'runs'
          ? profile.runs.length
          : id_ === 'insights'
            ? profile.insights.recommendations.length + profile.insights.proposals.length + profile.insights.lessons.length
            : 0;
      const label = i18n._(TAB_LABELS[id_]);
      return [id_, badge > 0 ? `${label} ${String(badge)}` : label];
    }),
  );

  return (
    <PageDetail
      kind={t`system · agent`}
      meta={[profile.model, promptLabel, runsLabel].filter((part) => part !== null).join(' · ')}
      title={profile.name}
      lead={profile.description}
      kv={kv}
      // The agent's own frontmatter says this, and the Definition section is what
      // changes it, so the switch reads the value and does not write one.
      toggles={
        item === null
          ? []
          : [
              {
                key: 'isolation',
                label: t`Worktree isolation`,
                hint: t`Edits land in an isolated git worktree · set in Definition`,
                on: isolation === 'worktree',
                disabled: true,
                onChange: () => undefined,
              },
            ]
      }
      actions={[
        { key: 'edit', label: t`edit prompt`, onClick: () => onTab('definition') },
        ...(boardParked()
          ? []
          : [{ key: 'run', label: t`run once`, node: <RunNowButton agentName={profile.name} scopeSlug={scopeSlug} className={actionClass('brand')} /> }]),
      ]}
      link={{ label: t`health →`, to: healthHref }}
    >
      <DetailSections
        idPrefix={`agent-${String(id)}`}
        ariaLabel={t`Agent sections`}
        tabs={TABS}
        labels={labels}
        active={tab}
        onSelect={onTab}
      >
        {tab !== null && (
          <ProfilePanel
            tab={tab}
            selectedId={id}
            profile={profile}
            projectNames={projectNames}
            scopeSlug={scopeSlug}
            defRefresh={refreshKey}
            onDefinitionMutated={onChanged}
          />
        )}
      </DetailSections>
    </PageDetail>
  );
}

/** The active section's panel. Definition mounts the existing System editor; every
 * other section renders a slice of the aggregated profile bundle. */
function ProfilePanel({
  tab,
  selectedId,
  profile,
  projectNames,
  scopeSlug,
  defRefresh,
  onDefinitionMutated,
}: {
  tab: ProfileTab;
  selectedId: number;
  profile: AgentProfile;
  projectNames: Record<string, string>;
  scopeSlug: string | null;
  defRefresh: number;
  onDefinitionMutated: () => void;
}): JSX.Element {
  const panel = (variant: 'docs' | 'editor' | 'meta'): JSX.Element => (
    <SystemItemPanel
      kind="agents"
      id={selectedId}
      refreshKey={defRefresh}
      projectNames={projectNames}
      onClose={() => undefined}
      onMutated={onDefinitionMutated}
      onDeleted={onDefinitionMutated}
      onReadonly={() => undefined}
      variant={variant}
    />
  );
  switch (tab) {
    case 'docs':
      return panel('docs');
    case 'definition':
      return panel('editor');
    case 'runs':
      return <RunsTab runs={profile.runs} />;
    case 'tasks':
      return <TasksTab tasks={profile.tasks} projectSlug={scopeSlug} />;
    case 'activity':
      return <ActivityTab activity={profile.activity} />;
    case 'insights':
      return <InsightsTab insights={profile.insights} />;
    default:
      // Overview leads with History + Versions (SystemItemPanel meta), then the
      // open-insights preview.
      return (
        <div className="space-y-4">
          {panel('meta')}
          <OverviewTab topInsights={profile.insights.recommendations} />
        </div>
      );
  }
}
