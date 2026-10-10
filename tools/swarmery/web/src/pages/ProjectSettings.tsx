// Project settings (/p/:slug/settings — fusion phase 4): the plugin toggles +
// attach/detach controls rehomed out of the old ProjectDetail body. Reuses the
// existing ProjectPlugins card (per-pack toggle) and ProjectActions (archive /
// detach / restore). Scoped by the workspace project id.

import { Trans, useLingui } from '@lingui/react/macro';
import { useCallback, useEffect, useState } from 'react';
import type { ProjectDetail as ProjectDetailData } from '../api/types';
import { fetchProject } from '../api';
import { useProjectWorkspace } from '../workspace/ProjectContext';
import { ProjectActions } from '../components/ProjectActions';
import { ProjectPlugins } from '../components/ProjectPlugins';
import { PermissionPresets } from '../components/PermissionPresets';
import { AccountSelector } from '../components/AccountSelector';
import { Empty, ErrorBox, Loading, SectionTitle } from '../components/ui';

export function ProjectSettings(): JSX.Element {
  const { t } = useLingui();
  const { projectId, loading: projLoading } = useProjectWorkspace();
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

  const wrap = (inner: JSX.Element): JSX.Element => (
    <div className="px-4 pt-5 pb-10 desk:px-8 desk:pt-7">{inner}</div>
  );

  if (projLoading && projectId === null) return wrap(<Loading label={t`workspace…`} />);
  if (projectId === null)
    return wrap(
      <Empty>
        <Trans>unknown project</Trans>
      </Empty>,
    );
  if (error !== null) return wrap(<ErrorBox message={error} onRetry={load} />);
  if (data === null) return wrap(<Loading label={t`settings…`} />);

  const { project } = data;
  const managed = project.plugin?.managed ?? false;
  const marketplace = project.plugin?.marketplace ?? '';

  return wrap(
    <>
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-display text-[20px] font-medium tracking-[-0.01em] text-ink">
          <Trans>settings</Trans>
        </span>
        <div className="ml-auto">
          <ProjectActions project={project} onChanged={load} />
        </div>
      </div>
      <div className="mt-1.5 font-mono text-[11px] text-ink-faint" data-tip-mono data-tip={project.path}>
        {project.path}
      </div>
      {marketplace !== '' && (
        <div className="mt-0.5 font-mono text-[10.5px] text-ink-faint">
          <Trans>marketplace: {marketplace}</Trans>
        </div>
      )}

      {/* Appearance is app-global and now lives on the /settings page (session
          mode); this per-project page keeps only project-scoped settings. */}

      {managed ? (
        <div className="mt-5">
          <ProjectPlugins projectId={project.id} />
        </div>
      ) : (
        <>
          <SectionTitle>
            <Trans>plugins</Trans>
          </SectionTitle>
          <div className="rounded-xl border border-dashed border-line px-3.5 py-4 font-mono text-[11.5px] text-ink-dim">
            {project.plugin === null
              ? t`telemetry-only — no .claude/settings.json to manage plugins for`
              : t`the swarmery plugin is not enabled for this project`}
          </div>
        </>
      )}

      {/* Permission preset applies to dispatch + approvals regardless of plugin
          management state, so it shows for every project. */}
      <div className="mt-5">
        <PermissionPresets projectId={project.id} />
      </div>

      {/* Multi-account picker: invisible on a single-account machine (see
          AccountSelector's own return-null guard), so it never disturbs the
          settings page for the common case. */}
      <div className="mt-5">
        <AccountSelector projectId={project.id} />
      </div>
    </>,
  );
}
