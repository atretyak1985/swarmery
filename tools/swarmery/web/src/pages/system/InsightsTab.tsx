// Insights tab (System screen): the promotion & drift detector — read-only
// advisory cards over GET /api/system/insights. Sections: promotion
// candidates (graduation rule, docs/EXTENDING.md), stale local overrides
// (local name colliding with a plugin item), dead components (active
// agent_dead findings), undocumented components (active docs_missing /
// docs_incomplete findings — the docs axis is kept out of lintMax, so this
// list is where it surfaces in bulk), plugin drift. Each item expands to
// copies + a redacted unified
// diff (DiffBlock, shared with the detail panel) + a copyable next-step
// hint. Display-only by design — promotion itself stays a manual flow.

import { plural } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { createContext, useContext, useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import type {
  SystemDeadComponent,
  SystemInsights,
  SystemPluginDrift,
  SystemPromotionCandidate,
  SystemStaleOverride,
  SystemUndocumentedItem,
} from '../../api/types';
import { fetchSystemInsights } from '../../api/system';
import { Empty, ErrorBox, Loading } from '../../components/ui';
import { useProjectColor } from '../../lib/projectColors';
import { DiffBlock } from './ItemDetail';
import { ScopeBadge } from './shared';

/* ----- shared atoms ----- */

function KindBadge({ kind }: { kind: string }): JSX.Element {
  return (
    <span className="shrink-0 rounded-full border border-line-strong px-2 py-px font-mono text-[10px] whitespace-nowrap text-ink-dim">
      {kind}
    </span>
  );
}

function SimilarityChip({
  identical,
  stat,
}: {
  identical: boolean;
  stat: { added: number; removed: number } | null;
}): JSX.Element {
  if (identical) {
    return (
      <span className="shrink-0 rounded-full border border-green/40 px-2 py-px font-mono text-[10px] whitespace-nowrap text-green">
        <Trans>identical</Trans>
      </span>
    );
  }
  return (
    <span className="shrink-0 rounded-full border border-amber/40 px-2 py-px font-mono text-[10px] whitespace-nowrap text-amber">
      <Trans>diverged</Trans>
      {stat !== null ? ` +${String(stat.added)}/−${String(stat.removed)}` : ''}
    </span>
  );
}

/** slug → display name map, provided once at the tab root so every chip can
 * show the clean project name instead of the raw path-derived slug. */
const NamesCtx = createContext<Record<string, string>>({});

function ProjectChip({ slug }: { slug: string | null }): JSX.Element {
  // App-wide distinct-color map (falls back to the per-slug hash off-list).
  const colorFor = useProjectColor();
  const names = useContext(NamesCtx);
  if (slug === null) {
    return (
      <span className="font-mono text-[10.5px] text-ink-dim">
        <Trans>global</Trans>
      </span>
    );
  }
  return (
    <span className="font-mono text-[10.5px]" style={{ color: colorFor(slug) }}>
      {names[slug] ?? slug}
    </span>
  );
}

/** Copyable next-step hint — display-only, no write actions (YAGNI). */
function HintLine({ hint }: { hint: string }): JSX.Element {
  const { t } = useLingui();
  const [copied, setCopied] = useState(false);
  return (
    <div className="mt-2 flex items-start gap-2 rounded-lg border border-line bg-bg px-3 py-2">
      <code className="min-w-0 flex-1 font-mono text-[11px] whitespace-pre-wrap text-ink-dim">
        {hint}
      </code>
      <button
        type="button"
        aria-label={t`copy next-step hint`}
        onClick={() => {
          // navigator.clipboard is undefined on non-secure origins (plain-HTTP
          // LAN) — optional-chain to a no-op instead of throwing; the hint
          // text itself stays visible/selectable either way.
          void navigator.clipboard
            ?.writeText(hint)
            .then(() => {
              setCopied(true);
              setTimeout(() => setCopied(false), 1500);
            })
            .catch(() => {});
        }}
        className="shrink-0 rounded border border-line-strong px-2 py-0.5 font-mono text-[10px] text-ink-dim transition-colors hover:text-ink"
      >
        {copied ? t`copied` : t`copy`}
      </button>
    </div>
  );
}

function ExpandableRow({
  header,
  children,
}: {
  header: JSX.Element;
  children: React.ReactNode;
}): JSX.Element {
  const [open, setOpen] = useState(false);
  return (
    <div className="border-b border-line-soft last:border-b-0">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        className="flex w-full items-center gap-2 px-3.5 py-2.5 text-left transition-colors hover:bg-surface2/50"
      >
        <span aria-hidden="true" className="w-3 shrink-0 font-mono text-[10px] text-ink-faint">
          {open ? '▾' : '▸'}
        </span>
        {header}
      </button>
      {open && <div className="px-3.5 pb-3 pl-[34px]">{children}</div>}
    </div>
  );
}

function InsightSection({
  title,
  count,
  subtitle,
  children,
}: {
  title: string;
  count: number;
  subtitle: string;
  children: React.ReactNode;
}): JSX.Element {
  return (
    <section className="overflow-hidden rounded-xl border border-line bg-surface">
      <div className="flex items-baseline gap-2 border-b border-line px-3.5 py-2.5">
        <h2 className="text-[13.5px] font-semibold text-ink">{title}</h2>
        <span className="font-mono text-[11px] text-ink-dim">{String(count)}</span>
        <span className="ml-auto hidden font-mono text-[10px] text-ink-faint sm:inline">
          {subtitle}
        </span>
      </div>
      {children}
    </section>
  );
}

/* ----- per-category rows ----- */

function PromotionRow({ c }: { c: SystemPromotionCandidate }): JSX.Element {
  return (
    <ExpandableRow
      header={
        <>
          <KindBadge kind={c.kind} />
          <span className="min-w-0 truncate text-[13.5px] font-semibold text-ink">{c.name}</span>
          <span className="flex min-w-0 items-center gap-1.5 truncate">
            {c.copies.map((copy) => (
              <ProjectChip key={copy.itemId} slug={copy.projectSlug} />
            ))}
          </span>
          <span className="ml-auto">
            <SimilarityChip identical={c.similarity === 'identical'} stat={c.diffStat} />
          </span>
        </>
      }
    >
      <div className="space-y-0.5">
        {c.copies.map((copy) => (
          <div key={copy.itemId} className="flex items-center gap-2 font-mono text-[10.5px] text-ink-faint">
            <ProjectChip slug={copy.projectSlug} />
            <span className="min-w-0 truncate">{copy.path}</span>
          </div>
        ))}
      </div>
      {c.diff !== '' && <DiffBlock diff={c.diff} />}
      {c.similarity === 'identical' && (
        <div className="mt-1 font-mono text-[11px] text-green">
          <Trans>all copies share one content hash — a clean promotion, no reconciliation needed</Trans>
        </div>
      )}
      <HintLine hint={c.hint} />
    </ExpandableRow>
  );
}

function OverrideRow({ o }: { o: SystemStaleOverride }): JSX.Element {
  const pluginName = o.pluginName;
  return (
    <ExpandableRow
      header={
        <>
          <KindBadge kind={o.kind} />
          <span className="min-w-0 truncate text-[13.5px] font-semibold text-ink">{o.name}</span>
          <span className="shrink-0 rounded-full border border-brand/40 px-2 py-px font-mono text-[10px] whitespace-nowrap text-brand">
            <Trans>plugin · {pluginName}</Trans>
          </span>
          <ProjectChip slug={o.local.projectSlug} />
          <span className="ml-auto">
            <SimilarityChip identical={o.identical} stat={o.diffStat} />
          </span>
        </>
      }
    >
      <div className="space-y-0.5 font-mono text-[10.5px] text-ink-faint">
        <div className="flex items-center gap-2">
          <span className="w-[42px] shrink-0 text-ink-dim">
            <Trans>local</Trans>
          </span>
          <span className="min-w-0 truncate">{o.local.path}</span>
        </div>
        <div className="flex items-center gap-2">
          <span className="w-[42px] shrink-0 text-ink-dim">
            <Trans>plugin</Trans>
          </span>
          <span className="min-w-0 truncate">{o.plugin.path}</span>
        </div>
      </div>
      {o.diff !== '' && <DiffBlock diff={o.diff} />}
      <HintLine hint={o.hint} />
    </ExpandableRow>
  );
}

function DeadRow({ d }: { d: SystemDeadComponent }): JSX.Element {
  return (
    <ExpandableRow
      header={
        <>
          <KindBadge kind={d.kind} />
          <span className="min-w-0 truncate text-[13.5px] font-semibold text-ink">{d.name}</span>
          <span className="ml-auto">
            <ScopeBadge scope={d.scope} projectSlug={d.projectSlug} />
          </span>
        </>
      }
    >
      <div className="font-mono text-[11px] text-ink-dim">{d.message}</div>
      <HintLine hint={d.hint} />
    </ExpandableRow>
  );
}

// Undocumented rows reuse the dead-component shell: a headline that names the
// item and the gap, expanding into the required subsections still absent. The
// rule chip distinguishes "no guide at all" from "guide with holes" — the two
// need different amounts of work and the linter keeps them apart.
function UndocumentedRow({ u }: { u: SystemUndocumentedItem }): JSX.Element {
  const { t } = useLingui();
  const noGuide = u.rule === 'docs_missing';
  const missingCount = u.missing.length;
  const missingList = u.missing.join(', ');
  return (
    <ExpandableRow
      header={
        <>
          <KindBadge kind={u.kind} />
          <span className="min-w-0 truncate text-[13.5px] font-semibold text-ink">{u.name}</span>
          <span
            className={`shrink-0 rounded-full border px-2 py-px font-mono text-[10px] whitespace-nowrap ${
              noGuide ? 'border-red/40 text-red' : 'border-amber/40 text-amber'
            }`}
          >
            {noGuide
              ? t`no guide`
              : plural(missingCount, { one: '# missing', few: '# missing', many: '# missing', other: '# missing' })}
          </span>
          <span className="ml-auto">
            <ScopeBadge scope={u.scope} projectSlug={u.projectSlug} />
          </span>
        </>
      }
    >
      <div className="font-mono text-[10.5px] text-ink-faint">{u.path}</div>
      {u.missing.length > 0 && (
        <div className="mt-1 font-mono text-[11px] text-ink-dim">
          <Trans>missing: {missingList}</Trans>
        </div>
      )}
      <HintLine hint={u.hint} />
    </ExpandableRow>
  );
}

// Plugin drift rows are flat — there is nothing to expand into, the message IS
// the finding. Errors mean the plugin is not loaded at all; warns mean it loads
// but is stale or came from a reclaimed cache dir.
function PluginDriftRow({ d }: { d: SystemPluginDrift }): JSX.Element {
  // The DTO carries the DB slug (path-derived); the label shows the clean
  // project name from the same map every other chip on this tab uses. The href
  // keeps the raw slug — findProject resolves path slugs exactly.
  const names = useContext(NamesCtx);
  return (
    <div className="flex flex-col gap-1 border-b border-line-soft py-2 last:border-b-0">
      <div className="flex items-center gap-2">
        <span
          className={`shrink-0 rounded-full border px-2 py-px font-mono text-[10px] whitespace-nowrap ${
            d.severity === 'error'
              ? 'border-red/40 bg-red/10 text-red'
              : 'border-amber/40 bg-amber/10 text-amber'
          }`}
        >
          {d.rule.replace('plugin_', '')}
        </span>
        <span className="min-w-0 truncate font-mono text-[12px] font-semibold text-ink">
          {d.pluginId}
        </span>
        <span className="ml-auto shrink-0">
          {d.projectSlug !== null ? (
            <Link
              to={`/p/${d.projectSlug}`}
              className="rounded-full border border-blue/40 px-2 py-px font-mono text-[10px] whitespace-nowrap text-blue hover:underline"
              data-tip-mono data-tip={d.projectPath}
            >
              {names[d.projectSlug] ?? d.projectSlug}
            </Link>
          ) : (
            <span className="font-mono text-[10px] whitespace-nowrap text-ink-faint">
              <Trans>machine-wide</Trans>
            </span>
          )}
        </span>
      </div>
      <div className="font-mono text-[11px] text-ink-dim">{d.message}</div>
    </div>
  );
}

/* ----- the tab ----- */

export function InsightsTab({
  refreshKey,
  projectNames,
  projectId,
}: {
  refreshKey: number;
  /** slug → display name (from the page's projects list) for clean chips. */
  projectNames: Record<string, string>;
  /** Numeric project id when mounted on a project page — narrows the lists to
   * the insights that project participates in. Omitted = fleet-wide. */
  projectId?: string | null;
}): JSX.Element {
  const { t } = useLingui();
  const [insights, setInsights] = useState<SystemInsights | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    let cancelled = false;
    fetchSystemInsights(projectId ?? undefined)
      .then((data) => {
        if (cancelled) return;
        setInsights(data);
        setError(null);
      })
      .catch((e: unknown) => {
        if (!cancelled) setError(String(e));
      });
    return () => {
      cancelled = true;
    };
  }, [refreshKey, attempt, projectId]);

  if (error !== null) return <ErrorBox message={error} onRetry={() => setAttempt((a) => a + 1)} />;
  if (insights === null) return <Loading label={t`insights…`} />;

  return (
    <NamesCtx.Provider value={projectNames}>
    <div className="space-y-4">
      <InsightSection
        title={t`Promotion candidates`}
        count={insights.promotionCandidates.length}
        subtitle={t`same-named local component in ≥2 projects — graduation rule (EXTENDING.md)`}
      >
        {insights.promotionCandidates.length === 0 ? (
          <Empty>
            <Trans>no component is duplicated across projects — nothing to promote</Trans>
          </Empty>
        ) : (
          insights.promotionCandidates.map((c) => <PromotionRow key={`${c.kind}:${c.name}`} c={c} />)
        )}
      </InsightSection>

      <InsightSection
        title={t`Stale local overrides`}
        count={insights.staleOverrides.length}
        subtitle={t`local name colliding with a plugin item — identical copies are safe to delete`}
      >
        {insights.staleOverrides.length === 0 ? (
          <Empty>
            <Trans>no local copy shadows a plugin component</Trans>
          </Empty>
        ) : (
          insights.staleOverrides.map((o) => (
            <OverrideRow key={`${o.kind}:${o.local.itemId}:${o.plugin.itemId}`} o={o} />
          ))
        )}
      </InsightSection>

      <InsightSection
        title={t`Dead components`}
        count={insights.dead.length}
        subtitle={t`0 telemetry mentions in 30 days (advisory)`}
      >
        {insights.dead.length === 0 ? (
          <Empty>
            <Trans>every agent has recent telemetry mentions</Trans>
          </Empty>
        ) : (
          insights.dead.map((d) => <DeadRow key={d.id} d={d} />)
        )}
      </InsightSection>

      <InsightSection
        title={t`Undocumented components`}
        count={insights.undocumented.length}
        subtitle={t`no usable \`# How to use\` guide (system-docs-format.md)`}
      >
        {insights.undocumented.length === 0 ? (
          <Empty>
            <Trans>every component carries a complete usage guide</Trans>
          </Empty>
        ) : (
          insights.undocumented.map((u) => <UndocumentedRow key={`${u.kind}:${u.id}`} u={u} />)
        )}
      </InsightSection>

      <InsightSection
        title={t`Plugin drift`}
        count={insights.pluginDrift?.length ?? 0}
        subtitle={t`enabled in a project's settings, but not actually loadable there`}
      >
        {insights.pluginDrift === undefined || insights.pluginDrift.length === 0 ? (
          <Empty>
            <Trans>every enabled plugin resolves for its project</Trans>
          </Empty>
        ) : (
          insights.pluginDrift.map((d) => (
            <PluginDriftRow key={`${d.rule}:${d.pluginId}:${d.projectPath}`} d={d} />
          ))
        )}
      </InsightSection>
    </div>
    </NamesCtx.Provider>
  );
}
