// Architecture page (/architecture): per-project architecture-map.html served
// through the daemon's jailed static route (architecture-out/). Union-gated:
// the feed lists projects with architecture-pack enabled OR an existing artifact,
// so the dropdown also shows pack-enabled projects that have not yet run
// /architecture-map (hasMap=false). Staleness badge compares analyzedAtCommit
// (baked into the map JSON) against headCommit (current HEAD, no exec).
// Clone of the Graphify page idioms — single load with ErrorBox retry, project
// selection by id, same-origin iframe.

import { useCallback, useEffect, useRef, useState } from 'react';
import type { ExplorationResp, ProvisionState, ToolsResponse } from '../api/types';
import { fetchExploration, fetchTools, rebuildArchitectureMap, toggleProjectPlugin } from '../api';
import { Sparkline } from '../components/Sparkline';
import {
  Card,
  Empty,
  ErrorBox,
  ExpandButton,
  ExpandableSection,
  Loading,
  SectionTitle,
} from '../components/ui';
import { fmtAgo } from '../lib/format';
import { findProject } from '../lib/projectSlug';
import { useTheme } from '../lib/theme';

// Provision states that are still in flight — while any project sits in one of
// these, the page settle-polls /api/tools until it lands on a terminal state.
const ACTIVE_STATES = new Set<ProvisionState['state']>(['pending', 'installing', 'generating']);
const POLL_MS = 3_000;

// architecture-map.html ships its own hardcoded palette + a standalone light/dark
// toggle. Embedded in the dashboard it must follow the app theme instead, so the
// same-origin iframe gets the app's live tokens pushed in as INLINE custom
// properties on its <html> (inline wins over both its `:root` defaults and its
// `html[data-theme]` overrides — the map cannot re-theme itself independently).
// Map-var ← app-token pairs; values are resolved via getComputedStyle so every
// palette re-tints the map, not just the light/dark axis.
const MAP_VARS: ReadonlyArray<readonly [string, string]> = [
  ['--bg', '--color-bg'],
  ['--panel', '--color-surface'],
  ['--card', '--color-surface'],
  ['--card-on', '--color-surface2'],
  ['--line', '--color-line'],
  ['--edge', '--color-line-strong'],
  ['--chip', '--color-field'],
  ['--ink', '--color-ink'],
  ['--ink-dim', '--color-ink-dim'],
  ['--ink-faint', '--color-ink-faint'],
  ['--accent', '--color-brand'],
  ['--flow', '--color-brand'],
  // Text on the accent fill: the page background is the highest-contrast token
  // against the brand amber in both modes (near-black in dark, off-white in light).
  ['--accent-ink', '--color-bg'],
  ['--mono', '--font-mono'],
  ['--sans', '--font-sans'],
];

// Provision pipeline stages for the progress bar. Percentages are stage floors,
// not real progress — the daemon only reports the stage, so the bar shows how
// deep in the pipeline the job is and pulses to signal liveness. `generating`
// (the headless analysis) dominates wall-clock, hence the wide gap before 100.
const STAGE_PROGRESS: Record<ProvisionState['state'], { pct: number; label: string }> = {
  pending: { pct: 8, label: 'queued' },
  installing: { pct: 30, label: 'installing pack' },
  generating: { pct: 70, label: 'analyzing repo & building map' },
  installed: { pct: 100, label: 'installed' },
  done: { pct: 100, label: 'done' },
  skipped: { pct: 100, label: 'already current' },
  failed: { pct: 100, label: 'failed' },
};

/** Indeterminate-ish progress panel for an in-flight provision job — replaces
 * the bare status chip so the wait doesn't look like a dead white area. */
function ProvisionProgress({ provision }: { provision: ProvisionState }): JSX.Element {
  const stage = STAGE_PROGRESS[provision.state];
  return (
    <div className="mt-3 shrink-0 rounded-xl border border-line bg-surface p-4">
      <div className="flex flex-wrap items-center justify-between gap-2 font-mono text-[11px]">
        <span className="inline-flex items-center gap-1.5 text-amber">
          <span aria-hidden>⟳</span>
          {provision.state}
          {provision.lastLine !== '' ? ` — ${provision.lastLine}` : ''}
        </span>
        <span className="text-ink-faint">{stage.label}</span>
      </div>
      <div
        role="progressbar"
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={stage.pct}
        aria-label="architecture map generation progress"
        className="mt-2.5 h-1.5 overflow-hidden rounded-full bg-field"
      >
        <div
          className="h-full animate-pulse rounded-full bg-amber transition-[width] duration-700"
          style={{ width: `${String(stage.pct)}%` }}
        />
      </div>
    </div>
  );
}

// CSS pushed INTO the map document for as long as it is embedded here.
//
// WHY it lives dashboard-side and not in the generator's template
// (plugins/architecture-pack/templates/map.html.tpl): injecting it applies to the
// architecture-out/architecture-map.html files that consumers have ALREADY
// generated. A template fix would only reach a project after it re-ran
// /architecture-map with a newer pack, so every existing artifact would stay
// broken in the embed until someone rebuilt it. The artifact is also still opened
// standalone (file:// straight from architecture-out/), and standalone is exactly
// where these rules must NOT apply — the map is the whole page there, keeps its
// own theme toggle, and sizes against a real viewport.
//
// Scope is deliberately narrow. The artifact's internal layout already survives a
// constrained height on its own: `body` is a flex column with `overflow:hidden`,
// `#layout{flex:1;min-height:0}` and `#ref{flex:1;min-height:0;overflow:auto}`
// give it a real min-height:0 chain, `#boardwrap{flex:1;overflow:auto}` and
// `#side{overflow-y:auto}` scroll inside the frame, and `#tabs{flex:none}` is
// pinned above them. So this patches only what standalone-vs-embedded actually
// changes, and adds no rule that merely restates one the artifact already has.
const EMBED_CSS = [
  // The map ships a standalone light/dark toggle; embedded it follows the app
  // theme (pushed as inline custom properties below), so the control is dead UI.
  '#theme{display:none}',
  // The artifact sizes itself with `body{height:100vh}`. Inside an iframe that
  // already resolves to the frame's height, so this changes nothing today — it
  // re-anchors the body to its own box so the map cannot outgrow a frame whose
  // height the embedder controls, whatever a future artifact does with `100vh`.
  'html,body{height:100%;overflow:hidden}',
  // `#inspector` is `position:fixed;bottom:20px;max-height:44vh`. Inside a frame
  // `vh` is already the PANE's height, so 44vh cannot overflow the bottom on its
  // own — the panel only becomes a problem when the pane is short, where 44% of
  // very little still hides the board behind it. The cap keeps a usable board in
  // that case (it binds below ~360px of pane) and is a ceiling, not a floor.
  // `bottom` goes UP, not down as the plan's note suggested: the offset is
  // measured from the frame's bottom edge, so a larger one moves the panel AWAY
  // from `#boardwrap`'s horizontal scrollbar — 20px only just cleared the ~15px
  // gutter platforms with classic (non-overlay) scrollbars reserve.
  '#inspector{max-height:min(44vh,calc(100% - 200px));bottom:28px}',
].join('');

/** Push the dashboard theme into the map iframe: resolved mode on `data-theme`,
 * app tokens as inline vars, and the embed-only CSS above (which includes hiding
 * the map's own theme toggle — standalone opens of the artifact keep it). */
function syncMapTheme(frame: HTMLIFrameElement, resolved: 'light' | 'dark'): void {
  const doc = frame.contentDocument;
  if (doc === null) return;
  const root = doc.documentElement;
  root.dataset.theme = resolved;
  const cs = getComputedStyle(document.documentElement);
  for (const [mapVar, token] of MAP_VARS) {
    const v = cs.getPropertyValue(token).trim();
    if (v !== '') root.style.setProperty(mapVar, v);
  }
  if (doc.getElementById('swarmery-embed-style') === null) {
    const style = doc.createElement('style');
    style.id = 'swarmery-embed-style';
    style.textContent = EMBED_CSS;
    // Appended last, so equal-specificity rules (`body`, `#inspector`) win over
    // the artifact's own <style> earlier in the head without needing !important.
    doc.head.appendChild(style);
  }
}

export function Architecture({
  scopedSlug,
  scopedId,
}: { scopedSlug?: string; scopedId?: number | null } = {}): JSX.Element {
  const [data, setData] = useState<ToolsResponse | null>(null);
  // Exploration share for the selected project (14 local days). Its own state
  // and its own failure mode: the metric is a header ornament, so a failed
  // fetch leaves it null and the page renders exactly as it did before.
  const [exploration, setExploration] = useState<ExplorationResp | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [selectedId, setSelectedId] = useState<number | null>(null);
  // Expanded state only — Esc, the body scroll lock, focus containment and focus
  // restore all belong to <ExpandableSection> (components/ui.tsx). This page used
  // to carry its own copy of that overlay, stacked above the tooltip layer and
  // restoring no focus on close; the shared primitive replaces it wholesale.
  const [expanded, setExpanded] = useState(false);
  const [acting, setActing] = useState(false);
  const aliveRef = useRef(true);
  const frameRef = useRef<HTMLIFrameElement | null>(null);
  const { theme, palette } = useTheme();

  const syncTheme = useCallback((): void => {
    // Deferred one frame: this child effect runs BEFORE ThemeProvider's parent
    // effect flips data-mode on <html>, so an immediate getComputedStyle would
    // read the OUTGOING theme's tokens (the same race Analytics notes for its
    // chart tokens). rAF fires after all commit effects, before paint.
    requestAnimationFrame(() => {
      if (frameRef.current !== null) syncMapTheme(frameRef.current, theme);
    });
    // theme + palette are the triggers: the token values are re-read from the
    // live computed style, so the palette id itself is only a dependency signal.
  }, [theme, palette]);

  // Re-push on every mode/palette flip; onLoad below covers the initial load
  // and project switches (the iframe is keyed by project id).
  useEffect(() => {
    syncTheme();
  }, [syncTheme]);

  const load = useCallback((): void => {
    fetchTools()
      .then((d) => {
        if (!aliveRef.current) return;
        setData(d);
        setError(null);
      })
      .catch((e: unknown) => {
        if (!aliveRef.current) return;
        setError(e instanceof Error ? e.message : String(e));
      });
  }, []);

  useEffect(() => {
    aliveRef.current = true;
    load();
    return () => {
      aliveRef.current = false;
    };
  }, [load]);

  const projects = data?.architecture.projects ?? [];
  const scoped = scopedSlug !== undefined;
  // Selection prefers hasMap (a built project) over an unbuilt one; in
  // project-workspace mode it is pinned to the workspace slug.
  const project = scoped
    ? (findProject(projects, scopedSlug ?? null) ?? undefined)
    : (projects.find((p) => p.id === selectedId) ?? projects.find((p) => p.hasMap) ?? projects[0]);

  // The exploration share is scoped to whichever project the page is showing,
  // so it re-fetches on every project switch. Failures are swallowed on
  // purpose: this is a header ornament, and it must never take the map with it.
  const exploreSlug = project?.slug;
  useEffect(() => {
    if (exploreSlug === undefined) return;
    let alive = true;
    setExploration(null);
    fetchExploration(exploreSlug)
      .then((d) => {
        if (alive) setExploration(d);
      })
      .catch(() => {
        if (alive) setExploration(null);
      });
    return () => {
      alive = false;
    };
  }, [exploreSlug]);

  // Kick off a rebuild (or the scoped enable-and-build) and re-fetch — the
  // provision job lands as 'pending' in the feed, so the settle-poll below
  // takes over the progress display. Failures reuse the top ErrorBox.
  const kick = useCallback(
    (action: () => Promise<unknown>): void => {
      setActing(true);
      action()
        .then(() => {
          if (aliveRef.current) load();
        })
        .catch((e: unknown) => {
          if (aliveRef.current) setError(e instanceof Error ? e.message : String(e));
        })
        .finally(() => {
          if (aliveRef.current) setActing(false);
        });
    },
    [load],
  );

  // While any project has an in-flight provision job, re-fetch every 3s until
  // it settles (mirrors Serena's interval-until-settled; the aliveRef guard in
  // `load` bounds writes to a mounted component). The effect re-runs whenever
  // `projects` changes, so once every job is terminal `active` is false and no
  // new interval is scheduled — it does not loop forever.
  useEffect(() => {
    const active = projects.some((p) => p.provision !== null && ACTIVE_STATES.has(p.provision.state));
    if (!active) return;
    const t = window.setInterval(load, POLL_MS);
    return () => window.clearInterval(t);
  }, [projects, load]);

  return (
    // Fill route (`handle: { fill: true }`, src/main.tsx): the shell has stopped
    // scrolling, so the page is the flex column that spends the leftover height —
    // every fixed-height chunk is `shrink-0` and the map pane takes the rest. The
    // bottom padding is a gap under the map, not the old document-page rhythm; at
    // 60px the pane visibly "hung" above the edge instead of filling to it.
    <div className="flex h-full min-h-0 min-w-0 flex-col px-4 pt-6 pb-4 desk:px-10 desk:pt-[34px] desk:pb-6">
      {/* SectionTitle owns its margins and takes no className, so the shrink-0
          flex item is a wrapper around it. */}
      <div className="shrink-0">
        <SectionTitle>architecture</SectionTitle>
      </div>
      {error !== null && (
        <div className="mb-2 shrink-0">
          <ErrorBox message={error} onRetry={load} />
        </div>
      )}
      {data === null && error === null ? (
        <Loading label="architecture…" />
      ) : data !== null ? (
        project === undefined ? (
          scoped && scopedId !== null && scopedId !== undefined ? (
            // The project has neither the pack nor an artifact: one button does
            // the whole Settings flow in place — enable architecture-pack, which
            // auto-provisions (install → generate); the feed then picks the
            // project up and the settle-poll shows progress.
            <Empty>
              <span className="mr-3">no architecture map for this project yet</span>
              <button
                type="button"
                disabled={acting}
                onClick={() => {
                  kick(() => toggleProjectPlugin(scopedId, 'architecture-pack', true));
                }}
                className="rounded-[9px] border border-line-strong bg-field px-2.5 py-[6px] font-mono text-[12px] text-ink transition-colors hover:border-ink-dim disabled:opacity-50"
              >
                {acting ? 'starting…' : '⚒ enable architecture-pack & build map'}
              </button>
            </Empty>
          ) : (
            <Empty>
              {scoped
                ? 'no architecture map for this project — enable architecture-pack in Settings to generate it'
                : 'no architecture maps yet — run /architecture-map in a project repo'}
            </Empty>
          )
        ) : (
          <>
            {/* The fragment is transparent to layout: this Card, the progress
                panel and the map pane below are all flex items of the page root. */}
            <Card className="shrink-0">
              <div className="flex flex-wrap items-center gap-3">
                {!scoped && (
                  <select
                    value={String(project.id)}
                    onChange={(e) => setSelectedId(Number(e.target.value))}
                    aria-label="architecture project"
                    className="rounded-[9px] border border-line-strong bg-field px-2.5 py-[6px] font-mono text-[12px] text-ink transition-colors outline-none focus:border-ink-dim"
                  >
                    {projects.map((p) => (
                      <option key={p.id} value={String(p.id)}>
                        {p.name ?? p.slug}
                      </option>
                    ))}
                  </select>
                )}
                {project.builtAt !== null && (
                  <span className="font-mono text-[10.5px] text-ink-faint">
                    map built {fmtAgo(project.builtAt)}
                    {project.analyzedAtCommit !== null && (
                      <> · @ {project.analyzedAtCommit.slice(0, 7)}</>
                    )}
                    {project.analyzedAtCommit !== null && project.headCommit !== null &&
                      (project.headCommit === project.analyzedAtCommit ? (
                        <span className="ml-1 text-green"> · current</span>
                      ) : (
                        <span className="ml-1 text-amber">
                          {' '}
                          · stale (HEAD {project.headCommit.slice(0, 7)})
                        </span>
                      ))}
                  </span>
                )}
                {exploration !== null && exploration.totals.calls > 0 && (
                  // Exploration share: the number the context layer is judged
                  // by. The sparkline is the daily share over the same 14 days
                  // the server defaults to; `[&>svg]:mt-0` cancels the
                  // component's tile-stack top margin so it sits on this row.
                  <span
                    className="flex items-center gap-2 font-mono text-[10.5px] text-ink-faint"
                    title="share of tool calls spent exploring the repo (reads, greps, finds) over the last 14 days"
                  >
                    explore {(exploration.totals.share * 100).toFixed(0)}%
                    <span className="block w-16 [&>svg]:mt-0">
                      <Sparkline
                        values={exploration.days.map((d) => d.share)}
                        highlight={exploration.days.length - 1}
                      />
                    </span>
                  </span>
                )}
                <span className="ml-auto flex items-center gap-2">
                  {(project.provision === null || !ACTIVE_STATES.has(project.provision.state)) && (
                    <button
                      type="button"
                      disabled={acting}
                      onClick={() => {
                        kick(() => rebuildArchitectureMap(project.id));
                      }}
                      className="rounded-[9px] border border-line-strong bg-field px-2.5 py-[6px] font-mono text-[12px] text-ink transition-colors hover:border-ink-dim disabled:opacity-50"
                    >
                      {acting ? 'starting…' : project.hasMap ? '↻ rebuild' : '⚒ build map'}
                    </button>
                  )}
                  {project.hasMap && (
                    <ExpandButton
                      onClick={() => setExpanded(true)}
                      expanded={expanded}
                    />
                  )}
                </span>
              </div>
            </Card>
            {project.provision !== null && ACTIVE_STATES.has(project.provision.state) && (
              // Progress replaces the old bare chip; the existing map (when
              // there is one) stays browsable below while the rebuild runs.
              <ProvisionProgress provision={project.provision} />
            )}
            {project.provision?.state === 'failed' && (
              <div className="mt-3 shrink-0">
                <ErrorBox
                  message={`${project.provision.error !== '' ? project.provision.error : 'provision failed'} — press rebuild to retry, or run /architecture-map manually`}
                  onRetry={load}
                />
              </div>
            )}
            {project.hasMap ? (
              // The pane that spends the leftover height (flex-1/min-h-0 come from
              // ExpandableSection's own collapsed class), and the same subtree in
              // both states: expanding only swaps the wrapper's classes, so the
              // iframe is never remounted and the map keeps its selected node,
              // board scroll position and pushed theme instead of re-fetching.
              // `key={project.id}` is the ONE thing that may remount it — switching
              // projects is a different map.
              <ExpandableSection
                expanded={expanded}
                onToggle={setExpanded}
                label="architecture map"
                className="mt-3"
              >
                <iframe
                  key={project.id}
                  ref={frameRef}
                  // builtAt as a cache-buster: the artifact is regenerated in
                  // place under a stable URL, and browsers heuristic-cached it
                  // before the route sent Cache-Control — a rebuild changes the
                  // query so a stale copy can never be shown.
                  src={
                    project.builtAt !== null
                      ? `${project.mapPath}?v=${encodeURIComponent(project.builtAt)}`
                      : project.mapPath
                  }
                  title="Architecture map"
                  onLoad={syncTheme}
                  // One class list for both states — the height comes from the
                  // flex parent, never from the viewport. The previous height was
                  // viewport math minus a hardcoded 180px for the chrome above it;
                  // every guess that ran short overshot into a second scrollbar,
                  // and no constant can be right in both shells at both breakpoints.
                  className="h-full w-full rounded-xl border border-line bg-surface"
                />
              </ExpandableSection>
            ) : project.provision === null || !ACTIVE_STATES.has(project.provision.state) ? (
              <div className="mt-3 shrink-0">
                <Empty>no map yet — press build map to generate it</Empty>
              </div>
            ) : null}
          </>
        )
      ) : null}
    </div>
  );
}
