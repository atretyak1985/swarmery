// Project settings (/p/:slug/settings): the same frame as /settings, with what
// belongs to one project: its identity and archive/detach actions (ProjectActions),
// its plugin toggles (ProjectPlugins), and how its sessions run (the permission
// preset and, on a multi-account machine, the account picker). Scoped by the
// workspace project id. Appearance is app-global and lives on /settings.

import { useLingui } from '@lingui/react/macro';
import { useCallback, useEffect, useState } from 'react';
import type { ProjectDetail as ProjectDetailData } from '../api/types';
import { fetchProject } from '../api';
import { AccountSelector } from '../components/AccountSelector';
import { PageDetail, PagePlace, usePlaceParams } from '../components/place/PageDetail';
import { PermissionPresets } from '../components/PermissionPresets';
import { ProjectActions } from '../components/ProjectActions';
import { ProjectPlugins } from '../components/ProjectPlugins';
import { Empty, ErrorBox, Loading } from '../components/ui';
import { useProjectWorkspace } from '../workspace/ProjectContext';

const PROJECT_TABS = ['project', 'plugins', 'sessions'] as const;
type ProjectTab = (typeof PROJECT_TABS)[number];

function DetailBody({
  tab,
  data,
  reload,
}: {
  tab: ProjectTab;
  data: ProjectDetailData;
  reload: () => void;
}): JSX.Element {
  const { t } = useLingui();
  const { project } = data;
  const marketplace = project.plugin?.marketplace ?? '';

  if (tab === 'plugins') {
    return (
      <PageDetail
        kind={t`settings`}
        meta={t`plugins`}
        title={t`Plugins`}
        lead={
          project.plugin?.managed === true
            ? null
            : project.plugin === null
              ? t`telemetry-only — no .claude/settings.json to manage plugins for`
              : t`the swarmery plugin is not enabled for this project`
        }
      >
        {project.plugin?.managed === true && (
          <div className="mt-[18px]">
            <ProjectPlugins projectId={project.id} />
          </div>
        )}
      </PageDetail>
    );
  }
  if (tab === 'sessions') {
    return (
      <PageDetail
        kind={t`settings`}
        meta={t`sessions`}
        title={t`Sessions`}
        lead={t`What its runs may do without asking, and which account they use.`}
      >
        {/* The preset applies to dispatch and approvals whatever the plugin state. */}
        <div className="mt-[18px]">
          <PermissionPresets projectId={project.id} />
        </div>
        {/* Invisible on a single-account machine: its own return-null guard. */}
        <div className="mt-[18px]">
          <AccountSelector projectId={project.id} />
        </div>
      </PageDetail>
    );
  }
  return (
    <PageDetail
      kind={t`settings`}
      meta={t`project`}
      title={project.name ?? project.slug}
      kv={[
        { k: t`path`, v: project.path },
        ...(marketplace !== '' ? [{ k: t`marketplace`, v: marketplace }] : []),
      ]}
    >
      <div className="mt-[22px]">
        <ProjectActions project={project} onChanged={reload} />
      </div>
    </PageDetail>
  );
}

export function ProjectSettings(): JSX.Element {
  const { t } = useLingui();
  const { projectId, loading: projLoading } = useProjectWorkspace();
  const { tab, pick } = usePlaceParams<ProjectTab>(PROJECT_TABS, 'project');
  const [data, setData] = useState<ProjectDetailData | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback((): void => {
    if (projectId === null) return;
    fetchProject(projectId)
      .then((d) => {
        setData(d);
        setError(null);
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)));
  }, [projectId]);

  useEffect(() => {
    setData(null);
    load();
  }, [load]);

  const detail = (): JSX.Element => {
    if (projLoading && projectId === null) return <Loading label={t`workspace…`} />;
    if (projectId === null) return <Empty>{t`unknown project`}</Empty>;
    if (error !== null) return <ErrorBox message={error} onRetry={load} />;
    if (data === null) return <Loading label={t`settings…`} />;
    return <DetailBody tab={tab} data={data} reload={load} />;
  };

  return (
    <PagePlace
      title={t`Settings`}
      sub={data?.project.name ?? data?.project.slug ?? ''}
      searchHint={t`filter settings`}
      groups={[
        {
          key: 'settings',
          label: t`Settings`,
          items: [
            { key: 'project', glyph: '⚙', title: t`Project`, summary: t`path · archive · detach` },
            { key: 'plugins', glyph: '⚙', title: t`Plugins`, summary: t`which packs this project enables` },
            { key: 'sessions', glyph: '⚙', title: t`Sessions`, summary: t`auto-approve preset · account` },
          ],
        },
      ]}
      openGroup="settings"
      selected={tab}
      onOpenGroup={() => undefined}
      onSelect={(key) => pick(key as ProjectTab)}
      detail={detail()}
    />
  );
}
