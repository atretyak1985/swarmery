// Project detail (/projects/:id): the enriched project header (plugin state,
// packs, marketplace, path) with the same archive/detach/restore actions as the
// list, its LOCAL component inventory (agents/skills/commands/hooks resolved
// from the project's .claude/), and headline stats with a recent-sessions list
// linking back to /sessions/:id. Telemetry-only projects (no readable
// .claude/settings.json) hide the component sections.

import { useCallback, useEffect, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import type { ProjectComponent, ProjectDetail as ProjectDetailData } from '../api/types';
import { fetchProject } from '../api';
import { fmtAgo, fmtCost, fmtDateTime, fmtTokens } from '../lib/format';
import { ProjectName } from '../components/ProjectName';
import { PluginBadge, ProjectActions } from '../components/ProjectActions';
import { ProjectPlugins } from '../components/ProjectPlugins';
import { Card, ErrorBox, Loading, SectionTitle } from '../components/ui';

function BackLink(): JSX.Element {
  return (
    <Link
      to="/projects"
      className="font-mono text-[11px] text-ink-dim transition-colors hover:text-ink"
    >
      ← projects
    </Link>
  );
}

function StatTile({ label, value }: { label: string; value: string }): JSX.Element {
  return (
    <div className="rounded-lg border border-line bg-surface px-3 py-2">
      <div className="font-mono text-[10px] tracking-[0.12em] text-ink-faint uppercase">{label}</div>
      <div className="mt-0.5 font-mono text-[13px] text-ink">{value}</div>
    </div>
  );
}

function ComponentList({ title, items }: { title: string; items: ProjectComponent[] }): JSX.Element {
  return (
    <div>
      <div className="font-mono text-[10.5px] tracking-[0.1em] text-ink-dim uppercase">
        {title} · {items.length}
      </div>
      {items.length === 0 ? (
        <div className="mt-1.5 font-mono text-[11px] text-ink-faint">none</div>
      ) : (
        <div className="mt-1.5 flex flex-wrap gap-1.5">
          {items.map((c) => (
            <span
              key={c.name}
              title={`source: ${c.source}`}
              className="rounded-full border border-line px-2 py-0.5 font-mono text-[10.5px] text-ink-2"
            >
              {c.name}
            </span>
          ))}
        </div>
      )}
    </div>
  );
}

export function ProjectDetail(): JSX.Element {
  const { id } = useParams<{ id: string }>();
  const [data, setData] = useState<ProjectDetailData | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback((): void => {
    if (id === undefined) return;
    fetchProject(id)
      .then((d) => {
        setData(d);
        setError(null);
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)));
  }, [id]);

  useEffect(() => {
    setData(null);
    load();
  }, [load]);

  if (error !== null) {
    return (
      <div className="px-4 pt-6 pb-20 desk:px-10 desk:pt-[34px]">
        <BackLink />
        <div className="mt-4">
          <ErrorBox message={error} onRetry={load} />
        </div>
      </div>
    );
  }
  if (data === null) {
    return (
      <div className="px-4 pt-6 pb-20 desk:px-10 desk:pt-[34px]">
        <BackLink />
        <Loading label="project…" />
      </div>
    );
  }

  const { project, components, stats } = data;
  const managed = project.plugin?.managed ?? false;

  return (
    <div className="px-4 pt-6 pb-20 desk:px-10 desk:pt-[34px] desk:pb-28">
      <BackLink />

      {/* Header */}
      <div className="mt-3 flex flex-wrap items-center gap-x-2.5 gap-y-1.5">
        <ProjectName
          name={project.name}
          slug={project.slug}
          className="font-display text-[24px] font-medium tracking-[-0.01em] desk:text-[28px]"
        />
        <PluginBadge project={project} />
        {(project.plugin?.packs ?? []).map((pack) => (
          <span
            key={pack}
            className="rounded-full border border-brand/40 bg-brand/10 px-2 py-0.5 font-mono text-[10px] whitespace-nowrap text-brand"
          >
            {pack}
          </span>
        ))}
        {project.archived && (
          <span className="rounded-full border border-line px-2 py-0.5 font-mono text-[10px] whitespace-nowrap text-ink-faint">
            archived
          </span>
        )}
        <div className="ml-auto">
          <ProjectActions project={project} onChanged={load} />
        </div>
      </div>
      <div className="mt-1.5 font-mono text-[11px] text-ink-faint" title={project.path}>
        {project.path}
      </div>
      {project.plugin?.marketplace !== undefined && project.plugin.marketplace !== '' && (
        <div className="mt-0.5 font-mono text-[10.5px] text-ink-faint">
          marketplace: {project.plugin.marketplace}
        </div>
      )}

      {/* Stats */}
      <SectionTitle>stats</SectionTitle>
      <div className="grid grid-cols-2 gap-2 desk:grid-cols-4">
        <StatTile label="sessions" value={String(stats.sessions)} />
        <StatTile label="tokens" value={stats.tokens !== null ? fmtTokens(stats.tokens) : '—'} />
        <StatTile label="cost" value={fmtCost(stats.costUsd)} />
        <StatTile
          label="last activity"
          value={stats.lastActivity !== null ? fmtAgo(stats.lastActivity) : '—'}
        />
      </div>

      {/* Components — only meaningful for a managed project. */}
      {managed ? (
        <>
          <SectionTitle>components (local)</SectionTitle>
          <div className="space-y-3.5">
            <ComponentList title="agents" items={components.agents} />
            <ComponentList title="skills" items={components.skills} />
            <ComponentList title="commands" items={components.commands} />
            <ComponentList title="hooks" items={components.hooks} />
          </div>
          <ProjectPlugins projectId={project.id} />
        </>
      ) : (
        <>
          <SectionTitle>components</SectionTitle>
          <div className="rounded-xl border border-dashed border-line px-3.5 py-4 font-mono text-[11.5px] text-ink-dim">
            {project.plugin === null
              ? 'telemetry-only — no .claude/settings.json, the swarmery plugin is not installed here'
              : 'the swarmery plugin is not enabled for this project'}
          </div>
        </>
      )}

      {/* Recent sessions */}
      <SectionTitle>recent sessions</SectionTitle>
      {stats.recentSessions.length === 0 ? (
        <div className="font-mono text-[11.5px] text-ink-faint">no sessions yet</div>
      ) : (
        <Card>
          <div className="divide-y divide-line-soft">
            {stats.recentSessions.map((s) => (
              <Link
                key={s.id}
                to={`/sessions/${String(s.id)}`}
                className="flex flex-wrap items-center gap-x-3 gap-y-0.5 py-1.5 font-mono text-[11px] transition-colors first:pt-0 last:pb-0 hover:text-ink"
              >
                <span className="min-w-0 flex-1 truncate text-ink-2">{s.title ?? s.sessionUuid}</span>
                <span className="text-ink-faint">{s.status}</span>
                {s.model !== null && <span className="text-ink-faint">{s.model}</span>}
                <span className="text-ink-faint">{fmtCost(s.costUsd)}</span>
                <span className="text-ink-faint">{fmtDateTime(s.startedAt)}</span>
              </Link>
            ))}
          </div>
        </Card>
      )}
    </div>
  );
}
