// System shell — the single top-level "System" destination that hosts its
// sections (Agents · Skills · Plugins · Hooks · Routines · Insights) as TABS. It
// is a thin OUTER tab bar over existing pages: Agents embeds <AgentHub>; Skills
// (the skills/commands/templates catalog, formerly "Toolkit"), Hooks and
// Insights embed <SystemHub>; Routines embeds <Routines embedded> (phase 8,
// /routines redirects here); Plugins embeds <ProjectPlugins> under a project and
// an empty state in the fleet. Nothing is reimplemented — the embedded pages
// suppress their own heading and hand URL ownership to this shell.
//
// Routing: /system/:tab (fleet) and /p/:slug/system/:tab (workspace). The active
// tab is the first path segment after /system; each embedded hub keeps its own
// detail sub-tabs on ?tab= and (for Agents) its selection in the
// /system/agents/:id path via the routeBase we pass. Bare /system redirects to
// Agents; the retired /system/toolkit(/…) lands on Skills.
//
// Canvas v3 2a lists no Hooks tab; it is kept on purpose (dropping it would
// remove a working view) — a recorded deviation, not an oversight.

import { useEffect, useMemo, useState } from 'react';
import { Navigate, useLocation, useNavigate, useParams } from 'react-router-dom';
import type { SystemHubSummary, SystemSummary } from '../api/types';
import { fetchSystemHubSummary } from '../api/systemHub';
import { fetchSystemSummary } from '../api/system';
import { ProjectPlugins } from '../components/ProjectPlugins';
import { ScopeChip } from '../components/ScopeChip';
import { Empty, Loading } from '../components/ui';
import { useProjectScope, useScope } from '../lib/scope';
import { useLiveUpdates } from '../lib/ws';
import { AgentHub } from './AgentHub';
import { Routines } from './Routines';
import { SystemHub } from './SystemHub';
import { FiltersRow } from './system/shared';

/** Origin scope of a catalog row — where the component is DEFINED (the user's
 * ~/.claude + the plugin cache = global; a project's own .claude/ = project).
 * Distinct from the project SCOPE chip next to it, which picks *which* project's
 * effective catalog is listed. */
type OriginScope = 'global' | 'project' | null;

type SystemTab = 'agents' | 'skills' | 'plugins' | 'hooks' | 'routines' | 'insights';
const TABS: SystemTab[] = ['agents', 'skills', 'plugins', 'hooks', 'routines', 'insights'];
const TAB_LABELS: Record<SystemTab, string> = {
  agents: 'Agents',
  skills: 'Skills',
  plugins: 'Plugins',
  hooks: 'Hooks',
  routines: 'Routines',
  insights: 'Insights',
};

/** Retired tab slugs → their new home (phase 8 renamed Toolkit to Skills). */
const RENAMED_TABS: Record<string, SystemTab> = { toolkit: 'skills' };

/** Tabs without per-row origin scope: the origin chips are hidden there rather
 * than rendered inert. */
const NO_ORIGIN_FILTER: ReadonlySet<SystemTab> = new Set(['plugins', 'routines', 'insights']);

function parseTab(value: string | undefined): SystemTab | null {
  return (TABS as string[]).includes(value ?? '') ? (value as SystemTab) : null;
}

export function SystemShell(): JSX.Element {
  const params = useParams();
  const navigate = useNavigate();
  const location = useLocation();
  const { scope } = useScope();

  // Workspace mount (/p/:slug/system/*) carries the slug; fleet mode uses the
  // global scope switcher. Either narrows the rollup window + template scope.
  // In PROJECT mode (a concrete :slug in the route) the embedded hubs show the
  // project's EFFECTIVE system — its own components PLUS the global ones PLUS
  // the built-ins of the packs it enables — never another project's locals. The
  // narrowing is the server's (?projectId=); fleet mode keeps the full catalog.
  const slug = params.slug;
  const projectScoped = slug !== undefined;
  // The scope REFERENCE (URL slug or the global scope chip). This shell's own
  // summary fetch needs it as a NUMERIC id — /api/system/* template matchers
  // accept the DB path slug or the id only — but the embedded hubs re-resolve it
  // themselves, so they get the reference verbatim (handing them a resolved id
  // would make `null while resolving` indistinguishable from `no scope`, and
  // they would fetch the whole fleet).
  const scopeRef = slug ?? scope;
  // scopePending: the unscoped summary counts the WHOLE machine, so firing it
  // while the project list resolves lets it land last and show fleet badges over
  // a project page.
  const { id: scopeSlug, pending: scopePending } = useProjectScope(scopeRef);
  const base = projectScoped ? `/p/${slug}/system` : '/system';

  // Origin-scope filter, hoisted OUT of the Agents tab so one control narrows
  // every roster (Agents · Toolkit · Hooks). Filtering stays client-side inside
  // each hub — the rosters are already the effective catalog for the project
  // scope, and these chips only slice what arrived. Insights is an advisory
  // inbox with no per-row scope, so the chips are hidden there rather than
  // rendered inert.
  const [originScope, setOriginScope] = useState<OriginScope>(null);

  // Active tab = first path segment after the base (params['*'] is the splat).
  const splat = params['*'] ?? '';
  const [firstSeg = '', ...restSegs] = splat.split('/');
  const tab = parseTab(firstSeg);
  const renamed = RENAMED_TABS[firstSeg];

  // Tab-bar count badges (agents · toolkit total · hooks · insights) from the
  // hub summary; live-refreshed on registry edits like the hubs themselves.
  const [summary, setSummary] = useState<SystemHubSummary | null>(null);
  useEffect(() => {
    let cancelled = false;
    if (scopePending) return;
    const load = (): void => {
      fetchSystemHubSummary(scopeSlug ?? undefined)
        .then((s) => {
          if (!cancelled) setSummary(s);
        })
        .catch(() => {
          if (!cancelled) setSummary(null);
        });
    };
    load();
    return () => {
      cancelled = true;
    };
  }, [scopeSlug, scopePending]);
  useLiveUpdates(
    (msg) => {
      if (msg.type === 'system_item_updated') {
        fetchSystemHubSummary(scopeSlug ?? undefined)
          .then(setSummary)
          .catch(() => setSummary(null));
      }
    },
    () => {
      fetchSystemHubSummary(scopeSlug ?? undefined)
        .then(setSummary)
        .catch(() => setSummary(null));
    },
  );

  // Usage-guide coverage. A separate fetch on purpose: the hub summary
  // (/api/system/hub/summary) carries only the nav badge counts, while the
  // docs block lives on /api/system/summary (systemDocsCoverageDTO). Coverage
  // is the whole point of the docs contract -- "we went through every item"
  // has to be a number on the page, not a claim in a commit message -- so it
  // belongs in the header rather than only in the Insights tab.
  const [docs, setDocs] = useState<SystemSummary['docs'] | null>(null);
  useEffect(() => {
    let cancelled = false;
    fetchSystemSummary()
      .then((s) => {
        if (!cancelled) setDocs(s.docs);
      })
      .catch(() => {
        if (!cancelled) setDocs(null);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const badges: Record<SystemTab, number | undefined> = useMemo(
    () =>
      summary === null
        ? {
            agents: undefined,
            skills: undefined,
            plugins: undefined,
            hooks: undefined,
            routines: undefined,
            insights: undefined,
          }
        : {
            agents: summary.agents,
            skills: summary.skills + summary.commands + summary.templates,
            // The hub summary carries no plugin or routine count.
            plugins: undefined,
            hooks: summary.hooks,
            routines: undefined,
            insights: summary.insights,
          },
    [summary],
  );

  // Retired /system/toolkit(/…) → the same place under Skills, keeping any
  // deeper selection and the query. `replace` so back doesn't loop.
  if (renamed !== undefined) {
    const rest = restSegs.length > 0 ? `/${restSegs.join('/')}` : '';
    return <Navigate to={{ pathname: `${base}/${renamed}${rest}`, search: location.search }} replace />;
  }
  // Bare /system → Agents tab (default).
  if (tab === null) {
    return <Navigate to={`${base}/agents`} replace />;
  }

  const goTab = (next: SystemTab): void => {
    if (next !== tab) navigate(`${base}/${next}`);
  };

  return (
    <div className="flex h-full min-h-0 flex-col px-4 pt-6 desk:px-10 desk:pt-[34px]">
      <h1 className="mb-3 font-display text-[30px] leading-tight font-medium tracking-[-0.01em]">
        System
      </h1>
      {/* Own row, NOT inside the tablist below: a non-tab child would break
          that element's role contract. This shell resolves scopeSlug for every
          embedded hub, so the chip belongs here rather than in each hub. */}
      <div className="mb-3 flex shrink-0 flex-wrap items-center gap-x-3 gap-y-2">
        <ScopeChip />
        {!NO_ORIGIN_FILTER.has(tab) && (
          <FiltersRow inline scope={originScope} onScope={setOriginScope} />
        )}
        {docs !== null && docs.total > 0 && (
          <span
            className="font-mono text-[11px] text-ink-dim"
            data-tip="usage-guide coverage: every agent, skill and command the marketplace ships must carry a '# How to use' block"
          >
            <span className={docs.documented === docs.total ? 'text-green' : 'text-amber'}>
              {docs.documented}/{docs.total}
            </span>
            <span className="text-ink-faint"> documented · </span>
            <span className={docs.reviewed === docs.total ? 'text-green' : 'text-amber'}>
              {docs.reviewed}/{docs.total}
            </span>
            <span className="text-ink-faint"> reviewed</span>
          </span>
        )}
      </div>
      <div
        className="mb-4 flex gap-1 overflow-x-auto border-b border-line [-webkit-overflow-scrolling:touch]"
        role="tablist"
        aria-label="System sections"
      >
        {TABS.map((t) => {
          const active = t === tab;
          const badge = badges[t];
          return (
            <button
              key={t}
              type="button"
              role="tab"
              aria-selected={active}
              onClick={() => goTab(t)}
              className={`-mb-px flex shrink-0 items-center gap-1.5 border-b-2 px-3.5 py-[8px] text-[13px] font-medium whitespace-nowrap transition-colors ${
                active ? 'border-brand text-brand' : 'border-transparent text-ink-dim hover:text-ink'
              }`}
            >
              {TAB_LABELS[t]}
              {badge !== undefined && badge > 0 && (
                <span className="inline-flex h-[16px] min-w-[16px] items-center justify-center rounded-full bg-line-strong px-1 font-mono text-[9.5px] font-bold text-ink-dim">
                  {badge}
                </span>
              )}
            </button>
          );
        })}
      </div>

      {/* The active tab's embedded hub owns the pane below (its own roster,
          search, detail rail and detail sub-tabs). Each hub manages its own
          internal selection/sub-tab state; the outer tab lives in the URL. */}
      <div className="min-h-0 flex-1">
        <SystemTabPanel
          tab={tab}
          base={base}
          scopeRef={scopeRef}
          projectId={scopeSlug}
          projectScoped={projectScoped}
          originScope={originScope}
        />
      </div>
    </div>
  );
}

/** Mounts the embedded hub for the active tab. Agents → AgentHub (roster +
 * selection-gated detail); Toolkit/Hooks/Insights → SystemHub pinned to the
 * matching category. Each receives an explicit routeBase so its internal
 * navigation stays on the /system tree. */
function SystemTabPanel({
  tab,
  base,
  scopeRef,
  projectId,
  projectScoped,
  originScope,
}: {
  tab: SystemTab;
  base: string;
  /** Resolved numeric project id (as a string) for the Plugins tab, or null
   * while resolving / in the fleet. */
  projectId: string | null;
  /** Project REFERENCE (URL slug / global scope value), not a resolved id —
   * each hub resolves it and gates its own fetches while that is in flight. */
  scopeRef: string | null;
  /** PROJECT mode (/p/:slug/system): the hubs list the project's effective
   * system rather than the machine-wide catalog. */
  projectScoped: boolean;
  /** Shell-owned origin-scope filter (all scopes / global / project) — the hub
   * slices its roster by it instead of rendering chips of its own. */
  originScope: OriginScope;
}): JSX.Element {
  if (tab === 'agents') {
    return (
      <AgentHub
        embedded
        routeBase={`${base}/agents`}
        scopeSlug={scopeRef}
        projectScoped={projectScoped}
        originScope={originScope}
      />
    );
  }
  if (tab === 'routines') {
    return (
      <div className="h-full overflow-y-auto">
        <Routines embedded />
      </div>
    );
  }
  if (tab === 'plugins') {
    // Plugins are enabled per project: the fleet has no single list to show.
    if (!projectScoped) {
      return <Empty>Plugins are enabled per project — pick one in the switcher.</Empty>;
    }
    return (
      <div className="h-full overflow-y-auto">
        {projectId === null ? (
          <Loading label="plugins…" />
        ) : (
          <ProjectPlugins projectId={Number(projectId)} />
        )}
      </div>
    );
  }
  // Skills (with the skills/commands/templates sub-pills), Hooks and Insights
  // map straight to their SystemHub categories.
  const forceCategory = tab; // 'skills' | 'hooks' | 'insights'
  return (
    <SystemHub
      embedded
      forceCategory={forceCategory}
      routeBase={`${base}/${tab}`}
      scopeSlug={scopeRef}
      projectScoped={projectScoped}
      originScope={originScope}
    />
  );
}
