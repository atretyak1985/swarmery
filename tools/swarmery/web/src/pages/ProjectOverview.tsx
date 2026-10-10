// Project overview (/p/:slug index — Canvas v2 editorial home): the phase-2
// rebuild that replaces the old stats list with a full editorial layout —
// hero sentence, right-now tiles, this-week deltas, project-scoped funnel,
// insights, capability cards, and needs-attention rows.
// Telemetry-only projects (no plugin) still hide the Capability section.

import type { MessageDescriptor } from '@lingui/core';
import { msg } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { useCallback, useEffect, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import type {
  AttentionItem,
  FunnelResp,
  OverviewTile,
  ProjectComponent,
  ProjectDetail as ProjectDetailData,
  ProjectOverviewResp,
  Recommendation,
  WeekMetric,
} from '../api/types';
import {
  fetchFunnel,
  fetchProject,
  fetchProjectOverview,
  fetchProjectRecommendations,
  runProjectAdvise,
} from '../api';
import { useProjectWorkspace } from '../workspace/ProjectContext';
import { Empty, ErrorBox, Loading } from '../components/ui';
import { ProjectName } from '../components/ProjectName';
import { boardParked } from '../lib/parked';
import { PluginBadge, ProjectActions } from '../components/ProjectActions';

// ── helpers ──────────────────────────────────────────────────────────────────

/** Color utility for tone → Tailwind class. */
function toneClass(tone: string | null | undefined): string {
  switch (tone) {
    case 'green':
      return 'text-green';
    case 'red':
      return 'text-red';
    case 'amber':
      return 'text-amber';
    default:
      return 'text-ink';
  }
}

/** Dot element driven by a tone string. */
function ToneDot({ tone }: { tone: string }): JSX.Element {
  const base = 'inline-block shrink-0 h-[7px] w-[7px] rounded-full';
  if (tone === 'green')
    return <span className={`${base} bg-green animate-pulse-dot`} />;
  if (tone === 'amber')
    return <span className={`${base} bg-amber animate-blink-dot`} />;
  if (tone === 'red')
    return <span className={`${base} bg-red`} />;
  return <span className={`${base} bg-ink-faint`} />;
}

// ── section rule ─────────────────────────────────────────────────────────────

function SectionRule({
  label,
  right,
}: {
  label: string;
  right?: JSX.Element | string;
}): JSX.Element {
  return (
    <div className="mt-6 flex items-center gap-3">
      <span className="font-mono text-[10px] uppercase tracking-[0.14em] text-ink-faint whitespace-nowrap">
        {label}
      </span>
      <span className="h-px flex-1 bg-line" />
      {right !== undefined && (
        <span className="font-mono text-[10px] text-ink-faint whitespace-nowrap">{right}</span>
      )}
    </div>
  );
}

// ── hero sentence ─────────────────────────────────────────────────────────────

function HeroSentence({ thisWeek }: { thisWeek: WeekMetric[] }): JSX.Element {
  // The labels are the server's metric keys (GET /api/projects/{id}/overview),
  // compared verbatim — not UI copy.
  const tasksShipped = thisWeek.find((m) => m.label === 'tasks shipped'); // i18n-ignore
  const approvalsAsked = thisWeek.find((m) => m.label === 'approvals asked'); // i18n-ignore
  const tasks = tasksShipped?.value ?? '0';
  const approvals = approvalsAsked?.value ?? '0';

  return (
    <p className="mt-[18px] max-w-[34ch] font-display text-[26px] font-medium leading-[1.28] tracking-[-0.01em] text-ink text-wrap-pretty">
      <Trans>
        Shipped <span className="text-green">{tasks} tasks</span> this week — and asked you{' '}
        <span className="text-brand">{approvals} times</span> to do it.
      </Trans>
    </p>
  );
}

// ── right-now tiles ───────────────────────────────────────────────────────────

function RightNowTile({ tile }: { tile: OverviewTile }): JSX.Element {
  const valueClass = toneClass(tile.tone);
  return (
    <div className="rounded-[12px] border border-line bg-surface px-[14px] py-3">
      <div className="flex items-center gap-[7px]">
        <ToneDot tone={tile.tone} />
        <span className="font-mono text-[9.5px] uppercase tracking-[0.1em] text-ink-faint">
          {tile.label}
        </span>
      </div>
      <div className="mt-[6px] flex items-baseline gap-[7px]">
        <span className={`font-display text-[23px] font-semibold ${valueClass}`}>
          {tile.value}
        </span>
        <span className="font-mono text-[10.5px] text-ink-faint">{tile.sub}</span>
      </div>
    </div>
  );
}

function RightNowSection({ tiles }: { tiles: OverviewTile[] }): JSX.Element {
  const { t } = useLingui();
  return (
    <>
      <SectionRule label={t`Right now`} />
      <div className="mt-[10px] grid grid-cols-[repeat(auto-fit,minmax(150px,1fr))] gap-[10px]">
        {tiles.map((tile) => (
          <RightNowTile key={tile.label} tile={tile} />
        ))}
      </div>
    </>
  );
}

// ── this-week tiles ──────────────────────────────────────────────────────────

function WeekTile({ metric }: { metric: WeekMetric }): JSX.Element {
  const deltaClass = toneClass(metric.deltaTone);
  return (
    <div className="rounded-[12px] border border-line px-[14px] py-3">
      <div className="font-mono text-[9.5px] uppercase tracking-[0.1em] text-ink-faint">
        {metric.label}
      </div>
      <div className="mt-[6px] flex items-baseline gap-[7px]">
        <span className="font-display text-[23px] font-semibold text-ink">
          {metric.value ?? '—'}
        </span>
        {metric.delta !== null && (
          <span className={`font-mono text-[11px] ${deltaClass}`}>{metric.delta}</span>
        )}
      </div>
      <div className="mt-[3px] font-mono text-[10.5px] text-ink-dim">{metric.sub}</div>
    </div>
  );
}

function ThisWeekSection({ metrics }: { metrics: WeekMetric[] }): JSX.Element {
  const { t } = useLingui();
  return (
    <>
      <SectionRule label={t`This week`} right={t`vs previous 7 d`} />
      <div className="mt-[10px] grid grid-cols-[repeat(auto-fit,minmax(170px,1fr))] gap-[10px]">
        {metrics.map((m) => (
          <WeekTile key={m.label} metric={m} />
        ))}
      </div>
    </>
  );
}

// ── where-work-sits (inline funnel) ──────────────────────────────────────────

const FUNNEL_LABELS: Record<string, MessageDescriptor> = {
  triage: msg`triage`,
  todo: msg`to do`,
  in_progress: msg`in progress`,
  in_review: msg`in review`,
  done: msg`done`,
  archived: msg`archived`,
};

function InlineFunnel({ funnel, slug }: { funnel: FunnelResp; slug: string }): JSX.Element {
  const { t, i18n } = useLingui();
  const maxCount = Math.max(1, ...funnel.columns.map((c) => c.count));
  const BAR_MAX_H = 40; // px for the tallest bar
  const rate = (funnel.completionRate * 100).toFixed(0);
  const stallNote =
    funnel.completionRate < 0.5 && funnel.columns.some((c) => c.count > 0)
      ? t`${rate}% completion rate — some tasks may be stalled.`
      : null;

  return (
    <>
      <SectionRule
        label={t`Where work sits`}
        {...(boardParked()
          ? {}
          : {
              right: (
                <Link
                  to={`/p/${slug}/board`}
                  className="font-mono text-[10.5px] text-ink-dim hover:text-brand transition-colors"
                >
                  <Trans>open board →</Trans>
                </Link>
              ),
            })}
      />
      <div className="mt-[10px] rounded-[12px] border border-line px-4 py-[14px]">
        <div className="flex items-flex-end gap-2">
          {funnel.columns.map((c) => {
            const barH = Math.max(3, Math.round((c.count / maxCount) * BAR_MAX_H));
            const columnLabel = FUNNEL_LABELS[c.column];
            return (
              <div key={c.column} className="min-w-0 flex-1">
                <div className="flex items-baseline gap-[5px]">
                  <span className="font-display text-[19px] font-semibold text-ink">
                    {c.count}
                  </span>
                </div>
                <div
                  className="mt-[5px] w-full rounded-[3px] bg-brand/30"
                  style={{ height: `${barH}px` }}
                />
                <div className="mt-[5px] overflow-hidden text-ellipsis whitespace-nowrap font-mono text-[9.5px] uppercase tracking-[0.06em] text-ink-faint">
                  {columnLabel !== undefined ? i18n._(columnLabel) : c.column}
                </div>
              </div>
            );
          })}
        </div>
        {stallNote !== null && (
          <p className="mt-3 text-[12.5px] leading-[1.55] text-ink-3">{stallNote}</p>
        )}
      </div>
    </>
  );
}

// ── insights (kept verbatim) ──────────────────────────────────────────────────

/** Lifecycle status chip for a recommendation. */
function InsightStatusChip({ status }: { status: Recommendation['status'] }): JSX.Element {
  const tone: Record<Recommendation['status'], string> = {
    proposed: 'border-line text-ink-dim',
    accepted: 'border-amber/40 bg-amber/10 text-amber',
    adopted: 'border-blue/40 bg-blue/10 text-blue',
    verified: 'border-green/40 bg-green/10 text-green',
    // The advisor closed it because the condition stopped reproducing — green
    // like `verified` (it is a good ending) but muted: nobody measured it, the
    // problem simply went away.
    resolved: 'border-green/40 text-green',
    dismissed: 'border-line text-ink-faint',
  };
  return (
    <span
      className={`shrink-0 rounded-[6px] border px-1.5 py-[2px] font-mono text-[10px] ${tone[status]}`}
    >
      {status}
    </span>
  );
}

/** Per-project Insights with generate button and settle-poll. */
function InsightsCard({ slug }: { slug: string }): JSX.Element {
  const { t } = useLingui();
  const [recs, setRecs] = useState<Recommendation[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [generating, setGenerating] = useState(false);
  const disposed = useRef(false);

  useEffect(() => {
    disposed.current = false;
    return () => {
      disposed.current = true;
    };
  }, []);

  const load = useCallback((): Promise<void> => {
    return fetchProjectRecommendations(slug)
      .then((r) => {
        if (disposed.current) return;
        setRecs(r.recommendations);
        setError(null);
      })
      .catch((e: unknown) => {
        if (disposed.current) return;
        setError(e instanceof Error ? e.message : String(e));
      });
  }, [slug]);

  useEffect(() => {
    setRecs(null);
    void load();
  }, [load]);

  const generate = useCallback((): void => {
    setGenerating(true);
    setError(null);
    runProjectAdvise(slug)
      .then(() => load())
      .catch((e: unknown) => {
        if (!disposed.current) setError(e instanceof Error ? e.message : String(e));
      })
      .finally(() => {
        if (!disposed.current) setGenerating(false);
      });
  }, [slug, load]);

  const top = (recs ?? []).slice(0, 3);

  return (
    <>
      <SectionRule
        label={t`Insights`}
        right={
          (
            <button
              type="button"
              onClick={generate}
              disabled={generating}
              className="rounded-[7px] border border-line-strong bg-transparent px-[10px] py-[3px] font-mono text-[10.5px] text-ink-dim transition-colors hover:text-ink disabled:opacity-50"
            >
              {generating ? t`analyzing…` : t`generate insights`}
            </button>
          ) as unknown as string
        }
      />
      {error !== null ? (
        <ErrorBox message={error} onRetry={() => void load()} />
      ) : recs === null ? (
        <Loading label={t`insights…`} />
      ) : top.length === 0 ? (
        <div className="mt-[10px] rounded-xl border border-dashed border-line px-3.5 py-4 font-mono text-[11.5px] text-ink-dim">
          <Trans>no recommendations yet — Generate insights runs the advisor now</Trans>
        </div>
      ) : (
        <>
          <div className="mt-[10px] flex flex-col gap-[2px]">
            {top.map((rec) => (
              <div
                key={rec.id}
                className="flex flex-wrap items-baseline gap-[9px] border-t border-line-soft px-0 py-[10px] first:border-t-0"
              >
                <span className="font-mono text-[10px] text-ink-faint">{rec.rule}</span>
                <span className="min-w-[200px] flex-1 text-[13px] font-medium text-ink">
                  {rec.title}
                </span>
                <InsightStatusChip status={rec.status} />
                <span className="flex-[100%] text-[12px] leading-[1.5] text-ink-dim">
                  {rec.detail}
                </span>
              </div>
            ))}
          </div>
          <button
            type="button"
            className="mt-[10px] bg-transparent p-0 font-mono text-[10.5px] text-ink-dim hover:text-brand transition-colors"
          >
            <Link to={`/p/${slug}/retro`} className="text-ink-dim hover:text-brand">
              <Trans>all recommendations in Retro →</Trans>
            </Link>
          </button>
        </>
      )}
    </>
  );
}

// ── capability cards ──────────────────────────────────────────────────────────

const CAP_CATEGORIES: { key: keyof { agents: ProjectComponent[]; skills: ProjectComponent[]; commands: ProjectComponent[]; hooks: ProjectComponent[] }; label: MessageDescriptor }[] = [
  { key: 'agents', label: msg`agents` },
  { key: 'skills', label: msg`skills` },
  { key: 'commands', label: msg`commands` },
  { key: 'hooks', label: msg`hooks` },
];

function CapabilityCard({
  title,
  items,
}: {
  title: string;
  items: ProjectComponent[];
}): JSX.Element {
  const { t } = useLingui();
  return (
    <div className="rounded-[12px] border border-line px-[15px] py-[13px]">
      <div className="flex items-baseline gap-2">
        <span className="font-mono text-[10px] uppercase tracking-[0.12em] text-ink-dim">
          {title}
        </span>
        <span className="font-mono text-[10px] text-ink-faint">{items.length}</span>
        <span className="mx-1 h-px flex-1 bg-line-soft" />
      </div>
      <div className="mt-[6px] max-h-[148px] overflow-y-auto overscroll-contain pr-[10px] [scrollbar-color:theme(colors.line-strong)_transparent] [scrollbar-width:thin]">
        {items.length === 0 ? (
          <div className="py-[6px] font-mono text-[11px] text-ink-faint">
            <Trans>none</Trans>
          </div>
        ) : (
          items.map((c) => {
            const source = c.source;
            return (
              <div
                key={c.name}
                data-tip={t`source: ${source}`}
                className="flex items-baseline gap-2 border-t border-line-soft py-[6px] font-mono text-[11.5px] first:border-t-0"
              >
                <span className="min-w-0 flex-1 overflow-hidden text-ellipsis whitespace-nowrap text-ink-2">
                  {c.name}
                </span>
              </div>
            );
          })
        )}
      </div>
    </div>
  );
}

function CapabilitySection({
  components,
  slug,
}: {
  components: ProjectDetailData['components'];
  slug: string;
}): JSX.Element {
  const { t, i18n } = useLingui();
  return (
    <>
      <SectionRule label={t`Capability`} right={t`local to this project`} />
      <div className="mt-[10px] grid grid-cols-[repeat(auto-fit,minmax(290px,1fr))] items-start gap-3">
        {CAP_CATEGORIES.map(({ key, label }) => (
          <CapabilityCard key={key} title={i18n._(label)} items={components[key]} />
        ))}
      </div>
      <div className="mt-3 font-mono text-[11px] text-ink-faint">
        <Trans>
          manage plugins + detach in{' '}
          <Link to={`/p/${slug}/settings`} className="text-ink-dim underline hover:text-ink">
            Settings →
          </Link>
        </Trans>
      </div>
    </>
  );
}

// ── needs attention ───────────────────────────────────────────────────────────

function AttentionRow({ item }: { item: AttentionItem }): JSX.Element {
  return (
    <div className="flex flex-wrap items-baseline gap-[10px] border-t border-line-soft py-[10px] first:border-t-0">
      <ToneDot tone={item.tone} />
      <span className="min-w-[200px] flex-1 text-[12.5px] leading-[1.5] text-ink-2">
        {item.text}
      </span>
      <Link
        to={item.href}
        className="rounded-[7px] border border-line-strong bg-transparent px-[10px] py-[3px] font-mono text-[10.5px] text-ink-dim transition-colors hover:border-amber/45 hover:text-amber"
      >
        {item.action}
      </Link>
    </div>
  );
}

function NeedsAttentionSection({ items }: { items: AttentionItem[] }): JSX.Element {
  const { t } = useLingui();
  return (
    <>
      <SectionRule label={t`Needs attention`} />
      {items.length === 0 ? (
        <div className="mt-2 font-mono text-[11.5px] text-ink-faint">
          <Trans>nothing flagged</Trans>
        </div>
      ) : (
        <div className="mt-2">
          {items.map((item, i) => (
            // eslint-disable-next-line react/no-array-index-key
            <AttentionRow key={i} item={item} />
          ))}
        </div>
      )}
    </>
  );
}

// ── root page ─────────────────────────────────────────────────────────────────

export function ProjectOverview(): JSX.Element {
  const { t } = useLingui();
  const { slug, projectId, loading: projLoading } = useProjectWorkspace();

  // fetchProject for header + Capability
  const [data, setData] = useState<ProjectDetailData | null>(null);
  const [dataError, setDataError] = useState<string | null>(null);

  // fetchProjectOverview for hero + right-now + this-week + attention
  const [overview, setOverview] = useState<ProjectOverviewResp | null>(null);
  const [overviewError, setOverviewError] = useState<string | null>(null);

  // fetchFunnel scoped to this project
  const [funnel, setFunnel] = useState<FunnelResp | null>(null);

  const loadData = useCallback((): void => {
    if (projectId === null) return;
    fetchProject(projectId)
      .then((d) => {
        setData(d);
        setDataError(null);
      })
      .catch((e: unknown) => setDataError(e instanceof Error ? e.message : String(e)));
  }, [projectId]);

  const loadOverview = useCallback((): void => {
    if (projectId === null) return;
    fetchProjectOverview(projectId)
      .then((d) => {
        setOverview(d);
        setOverviewError(null);
      })
      .catch((e: unknown) => setOverviewError(e instanceof Error ? e.message : String(e)));
  }, [projectId]);

  const loadFunnel = useCallback((): void => {
    if (slug === '') return;
    fetchFunnel({ project: slug })
      .then((f) => setFunnel(f))
      .catch(() => setFunnel(null));
  }, [slug]);

  useEffect(() => {
    setData(null);
    loadData();
  }, [loadData]);

  useEffect(() => {
    setOverview(null);
    loadOverview();
  }, [loadOverview]);

  useEffect(() => {
    setFunnel(null);
    loadFunnel();
  }, [loadFunnel]);

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
  if (dataError !== null) return wrap(<ErrorBox message={dataError} onRetry={loadData} />);
  if (data === null) return wrap(<Loading label={t`project…`} />);

  const { project, components } = data;
  const managed = project.plugin?.managed ?? false;

  return wrap(
    <>
      {/* ── §1 Header (kept) ── */}
      <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1.5">
        <ProjectName
          name={project.name}
          slug={project.slug}
          className="font-display text-[22px] font-medium tracking-[-0.01em] desk:text-[26px]"
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
            <Trans>archived</Trans>
          </span>
        )}
        <div className="ml-auto">
          <ProjectActions project={project} onChanged={loadData} />
        </div>
      </div>
      <div className="mt-1.5 font-mono text-[11px] text-ink-faint" data-tip-mono data-tip={project.path}>
        {project.path}
      </div>

      {/* ── §2 Hero sentence ── */}
      {overview !== null && <HeroSentence thisWeek={overview.thisWeek} />}
      {overviewError !== null && (
        <p className="mt-3 font-mono text-[11px] text-red">{overviewError}</p>
      )}

      {/* ── §3 Right now ── */}
      {overview !== null && overview.rightNow.length > 0 && (
        <RightNowSection tiles={overview.rightNow} />
      )}

      {/* ── §4 This week ── */}
      {overview !== null && overview.thisWeek.length > 0 && (
        <ThisWeekSection metrics={overview.thisWeek} />
      )}

      {/* ── §5 Where work sits ── */}
      {funnel !== null && <InlineFunnel funnel={funnel} slug={project.slug} />}

      {/* ── §6 Insights (kept verbatim) ── */}
      <InsightsCard slug={project.slug} />

      {/* ── §7 Capability ── */}
      {managed ? (
        <CapabilitySection components={components} slug={project.slug} />
      ) : (
        <>
          <SectionRule label={t`Capability`} right={t`local to this project`} />
          <div className="mt-[10px] rounded-xl border border-dashed border-line px-3.5 py-4 font-mono text-[11.5px] text-ink-dim">
            {project.plugin === null
              ? t`telemetry-only — no .claude/settings.json, the swarmery plugin is not installed here`
              : t`the swarmery plugin is not enabled for this project`}
          </div>
        </>
      )}

      {/* ── §8 Needs attention ── */}
      {overview !== null && (
        <NeedsAttentionSection items={overview.attention} />
      )}
    </>,
  );
}
