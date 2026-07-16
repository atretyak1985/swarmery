// Projects list (sidebar "Projects"): every project the daemon knows about,
// flagged managed (swarmery plugin enabled in its .claude/settings.json) vs
// telemetry-only, with lifetime session/token/cost totals. Pinned projects
// come first (server order) with a pin toggle per row; tag chips are editable
// via ProjectActions and filterable via the chip row. Below the list, a
// week-over-week health comparison table (GET /api/projects/health). Each row
// links to the project detail (/projects/:id); row actions (archive / restore
// / detach / tags) live in the shared ProjectActions control.

import { useCallback, useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import type { Project, ProjectHealth } from '../api/types';
import { fetchProjects, fetchProjectsHealth, patchProject } from '../api';
import { fmtAgo, fmtCost, fmtTokens } from '../lib/format';
import { ProjectName } from '../components/ProjectName';
import { PluginBadge, ProjectActions } from '../components/ProjectActions';
import { Card, Empty, ErrorBox, Loading, SectionTitle } from '../components/ui';

function Metric({ label, value }: { label: string; value: string }): JSX.Element {
  return (
    <span className="whitespace-nowrap">
      <span className="text-ink-2">{value}</span>
      <span className="text-ink-faint"> {label}</span>
    </span>
  );
}

/* ----- pin toggle (PATCH /api/projects/{id} {pinned}) ----- */

function PinToggle({ project, onChanged }: { project: Project; onChanged: () => void }): JSX.Element {
  const [busy, setBusy] = useState(false);
  return (
    <button
      type="button"
      disabled={busy}
      aria-pressed={project.pinned}
      aria-label={project.pinned ? `unpin ${project.slug}` : `pin ${project.slug}`}
      title={project.pinned ? 'unpin' : 'pin to top'}
      onClick={() => {
        setBusy(true);
        patchProject(project.id, { pinned: !project.pinned })
          .then(onChanged)
          .catch(() => undefined) // transient failure — the list reload is the truth
          .finally(() => setBusy(false));
      }}
      className={`font-mono text-[13px] leading-none transition-colors disabled:opacity-40 ${
        project.pinned ? 'text-brand' : 'text-ink-faint hover:text-ink'
      }`}
    >
      {project.pinned ? '◆' : '◇'}
    </button>
  );
}

/* ----- one project row ----- */

function ProjectRow({ project, onChanged }: { project: Project; onChanged: () => void }): JSX.Element {
  const packs = project.plugin?.packs ?? [];
  return (
    <Card>
      <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1.5">
        <PinToggle project={project} onChanged={onChanged} />
        <Link to={`/projects/${String(project.id)}`} className="group flex items-center gap-2">
          <ProjectName
            name={project.name}
            slug={project.slug}
            className="font-display text-[14px] font-semibold group-hover:underline"
          />
        </Link>
        <PluginBadge project={project} />
        {packs.map((pack) => (
          <span
            key={pack}
            className="rounded-full border border-brand/40 bg-brand/10 px-2 py-0.5 font-mono text-[10px] whitespace-nowrap text-brand"
          >
            {pack}
          </span>
        ))}
        {project.tags.map((tag) => (
          <span
            key={tag}
            className="rounded-full border border-line-strong px-2 py-0.5 font-mono text-[10px] whitespace-nowrap text-ink-dim"
          >
            #{tag}
          </span>
        ))}
        {project.archived && (
          <span className="rounded-full border border-line px-2 py-0.5 font-mono text-[10px] whitespace-nowrap text-ink-faint">
            archived
          </span>
        )}

        <div className="ml-auto">
          <ProjectActions project={project} onChanged={onChanged} />
        </div>
      </div>

      <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 font-mono text-[11px] text-ink-dim">
        <Metric label="sessions" value={String(project.sessions)} />
        <Metric label="tokens" value={project.tokens !== null ? fmtTokens(project.tokens) : '—'} />
        <span className="whitespace-nowrap text-ink-2">{fmtCost(project.costUsd)}</span>
        {project.lastActivity !== null && (
          <span className="whitespace-nowrap text-ink-faint">{fmtAgo(project.lastActivity)}</span>
        )}
      </div>
      <div className="mt-1 truncate font-mono text-[10.5px] text-ink-faint" title={project.path}>
        {project.path}
      </div>
    </Card>
  );
}

/* ----- health comparison table ----- */

function TrendArrow({ curr, prev }: { curr: number | null; prev: number | null }): JSX.Element {
  if (curr === null || prev === null || curr === prev) {
    return (
      <span className="text-ink-faint" title="no week-over-week comparison">
        →
      </span>
    );
  }
  return curr > prev ? (
    <span className="text-brand" title="up vs previous week">
      ↑
    </span>
  ) : (
    <span className="text-green" title="down vs previous week">
      ↓
    </span>
  );
}

function fmtDuration(ms: number | null): string {
  if (ms === null) return '—';
  const min = Math.round(ms / 60_000);
  if (min < 1) return '<1m';
  if (min < 60) return `${String(min)}m`;
  return `${String(Math.floor(min / 60))}h ${String(min % 60)}m`;
}

function fmtRate(rate: number | null): string {
  if (rate === null) return '—';
  return `${(rate * 100).toFixed(1)}%`;
}

function HealthTable({ rows }: { rows: ProjectHealth[] }): JSX.Element {
  if (rows.length === 0) return <Empty>no health data yet</Empty>;
  return (
    <div className="overflow-x-auto rounded-[14px] border border-line">
      <table className="w-full border-collapse font-mono text-[11px]">
        <thead>
          <tr className="border-b border-line text-left text-[10px] tracking-[0.1em] text-ink-faint uppercase">
            <th className="px-3 py-2 font-normal">project</th>
            <th className="px-3 py-2 text-right font-normal">cost 7d</th>
            <th className="px-3 py-2 text-right font-normal">prev 7d</th>
            <th className="px-3 py-2 text-right font-normal">error rate 7d</th>
            <th className="px-3 py-2 text-right font-normal">avg session 7d</th>
            <th className="px-3 py-2 text-right font-normal">last activity</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-line-soft">
          {rows.map((r) => (
            <tr key={r.id}>
              <td className="px-3 py-2">
                <Link to={`/projects/${String(r.id)}`} className="hover:underline">
                  <ProjectName name={r.name} slug={r.slug} />
                </Link>
                {r.pinned && (
                  <span className="ml-1.5 text-brand" title="pinned" aria-label="pinned">
                    ◆
                  </span>
                )}
              </td>
              <td className="px-3 py-2 text-right text-ink-2">
                {fmtCost(r.costWeekUsd)}{' '}
                <TrendArrow curr={r.costWeekUsd} prev={r.costPrevWeekUsd} />
              </td>
              <td className="px-3 py-2 text-right text-ink-dim">{fmtCost(r.costPrevWeekUsd)}</td>
              <td className="px-3 py-2 text-right text-ink-2">{fmtRate(r.errorRate)}</td>
              <td className="px-3 py-2 text-right text-ink-2">{fmtDuration(r.avgSessionMs)}</td>
              <td className="px-3 py-2 text-right text-ink-faint">
                {r.lastActivity !== null ? fmtAgo(r.lastActivity) : '—'}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/* ----- tag filter chips ----- */

function TagFilter({
  tags,
  value,
  onChange,
}: {
  tags: string[];
  value: string | null;
  onChange: (tag: string | null) => void;
}): JSX.Element | null {
  if (tags.length === 0) return null;
  return (
    <div className="mt-3 flex flex-wrap items-center gap-2">
      {tags.map((tag) => {
        const selected = value === tag;
        return (
          <button
            key={tag}
            type="button"
            aria-pressed={selected}
            onClick={() => onChange(selected ? null : tag)}
            className={`shrink-0 rounded-full border px-[11px] py-1 font-mono text-[10.5px] whitespace-nowrap transition-colors ${
              selected
                ? 'border-[#4a4e58] bg-surface2 text-ink'
                : 'border-line-strong text-ink-dim hover:text-ink'
            }`}
          >
            #{tag}
          </button>
        );
      })}
    </div>
  );
}

/* ----- screen ----- */

export function Projects(): JSX.Element {
  const [projects, setProjects] = useState<Project[] | null>(null);
  const [health, setHealth] = useState<ProjectHealth[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [showArchived, setShowArchived] = useState(false);
  const [tag, setTag] = useState<string | null>(null);

  const load = useCallback((): void => {
    fetchProjects(showArchived)
      .then((list) => {
        setProjects(list);
        setError(null);
      })
      .catch((e: unknown) => setError(String(e)));
    fetchProjectsHealth()
      .then(setHealth)
      .catch(() => setHealth(null)); // health endpoint absent → table hidden
  }, [showArchived]);

  useEffect(() => {
    setProjects(null);
    setHealth(null); // both refetch in load(); keeping stale health would desync the table
    load();
  }, [load]);

  const managed = (projects ?? []).filter((p) => p.plugin?.managed).length;
  const allTags = [...new Set((projects ?? []).flatMap((p) => p.tags))].sort();
  // Server order is pinned-first already; the tag filter narrows client-side.
  const visible = (projects ?? []).filter((p) => tag === null || p.tags.includes(tag));
  const visibleHealth = (health ?? []).filter(
    (h) => tag === null || h.tags.includes(tag),
  );

  return (
    <div className="px-4 pt-6 pb-20 desk:px-10 desk:pt-[34px] desk:pb-28">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h1 className="font-display text-[26px] font-medium tracking-[-0.01em] desk:text-[30px]">
          Projects
        </h1>
        <label className="flex cursor-pointer items-center gap-1.5 font-mono text-[11px] text-ink-dim">
          <input
            type="checkbox"
            checked={showArchived}
            onChange={(e) => setShowArchived(e.target.checked)}
            className="accent-brand"
          />
          show archived
        </label>
      </div>
      <div className="mt-1.5 font-mono text-[11px] text-ink-dim">
        {projects !== null
          ? `${String(visible.length)} project${visible.length === 1 ? '' : 's'} · ${String(managed)} managed`
          : ' '}
      </div>

      <TagFilter tags={allTags} value={tag} onChange={setTag} />

      {error !== null && <ErrorBox message={error} onRetry={load} />}
      {projects === null && error === null && <Loading label="projects…" />}
      {projects !== null && visible.length === 0 && (
        <Empty>
          {tag !== null ? (
            <>
              no projects tagged <span className="font-mono text-ink">#{tag}</span>
            </>
          ) : (
            <>
              no projects yet — run{' '}
              <span className="font-mono text-ink">swarmery ingest &lt;file.jsonl&gt;</span> or
              onboard one from the command deck
            </>
          )}
        </Empty>
      )}

      <div className="mt-5">
        {visible.map((p) => (
          <ProjectRow key={p.id} project={p} onChanged={load} />
        ))}
      </div>

      {health !== null && (
        <section className="mt-8">
          <SectionTitle>Health · last 7 days vs previous</SectionTitle>
          <HealthTable rows={visibleHealth} />
        </section>
      )}
    </div>
  );
}
