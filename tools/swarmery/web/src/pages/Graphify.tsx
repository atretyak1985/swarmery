// Graphify page (/graphify): static graph.html visualizations for
// graphify-pack projects, served through the daemon's jailed static route.
// A project dropdown, a "graph built …" info row, and a same-origin iframe
// when the viz artifact exists — honest hints otherwise. Artifacts are
// static files, so a single load with ErrorBox retry is enough (no polling);
// there is deliberately no rebuild button (the daemon does not run graphify).
// The route exists only while the sidebar item does (graphify.projects > 0),
// but the page still renders an honest empty state on direct navigation.

import { Trans, useLingui } from '@lingui/react/macro';
import { useCallback, useEffect, useRef, useState } from 'react';
import type { ToolsResponse } from '../api/types';
import { fetchTools } from '../api';
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

export function Graphify({ scopedSlug }: { scopedSlug?: string } = {}): JSX.Element {
  const { t } = useLingui();
  const [data, setData] = useState<ToolsResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  // Selection is kept by project id so it survives reloads; null → default
  // (first project with a viz, else the first entry). In project-workspace mode
  // (scopedSlug set) the switcher owns project choice, so the in-page dropdown
  // is hidden and the selection is pinned to that slug.
  const [selectedId, setSelectedId] = useState<number | null>(null);
  // Expanded state only — Esc (including from inside the same-origin viz frame),
  // the body scroll lock, focus containment and focus restore all belong to
  // <ExpandableSection> (components/ui.tsx), the same primitive pages/Architecture.tsx
  // uses. Nothing about the overlay is re-implemented here.
  const [expanded, setExpanded] = useState(false);

  // Unmount guard (ProjectPlugins idiom): a ref because the load callback
  // outlives any single effect run.
  const aliveRef = useRef(true);

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

  const projects = data?.graphify.projects ?? [];
  const scoped = scopedSlug !== undefined;
  const project = scoped
    ? (findProject(projects, scopedSlug ?? null) ?? undefined)
    : (projects.find((p) => p.id === selectedId) ?? projects.find((p) => p.hasViz) ?? projects[0]);
  const builtAgo = project?.builtAt != null ? fmtAgo(project.builtAt) : '';

  return (
    // Fill route (`handle: { fill: true }`, src/main.tsx): the shell has stopped
    // scrolling, so the page is the flex column that spends the leftover height —
    // every fixed-height chunk is `shrink-0` and the viz pane takes the rest. The
    // bottom padding is a gap under the pane, not the old document-page rhythm.
    <div className="flex h-full min-h-0 min-w-0 flex-col px-4 pt-6 pb-4 desk:px-10 desk:pt-[34px] desk:pb-6">
      {/* SectionTitle owns its margins and takes no className, so the shrink-0
          flex item is a wrapper around it. */}
      <div className="shrink-0">
        <SectionTitle>
          <Trans>graphify</Trans>
        </SectionTitle>
      </div>
      {error !== null && (
        <div className="mb-2 shrink-0">
          <ErrorBox message={error} onRetry={load} />
        </div>
      )}
      {data === null && error === null ? (
        <Loading label={t`graphify…`} />
      ) : data !== null ? (
        project === undefined ? (
          <Empty>
            {scoped
              ? t`graphify-pack is not enabled for this project — enable it in Settings`
              : t`no projects with graphify-pack enabled — enable it in a project’s plugins card`}
          </Empty>
        ) : (
          <>
            {/* The fragment is transparent to layout: this Card and the viz pane
                below are both flex items of the page root. */}
            <Card className="shrink-0">
              <div className="flex flex-wrap items-center gap-3">
                {!scoped && (
                  <select
                    value={String(project.id)}
                    onChange={(e) => setSelectedId(Number(e.target.value))}
                    aria-label={t`graphify project`}
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
                    <Trans>graph built {builtAgo}</Trans>
                  </span>
                )}
                {project.hasViz && (
                  <span className="ml-auto flex items-center gap-2">
                    <ExpandButton onClick={() => setExpanded(true)} expanded={expanded} />
                  </span>
                )}
              </div>
            </Card>
            {project.hasViz ? (
              // The pane that spends the leftover height (flex-1/min-h-0 come from
              // ExpandableSection's own collapsed class), and the same subtree in
              // both states: expanding only swaps the wrapper's classes, so the
              // iframe is never remounted and the graph keeps its layout, pan/zoom
              // and selection instead of re-fetching the viz artifact.
              // `key={project.id}` is the ONE thing that may remount it — switching
              // projects is a different graph.
              <ExpandableSection
                expanded={expanded}
                onToggle={setExpanded}
                label={t`graphify visualization`}
                className="mt-3"
              >
                <iframe
                  key={project.id}
                  src={project.vizPath}
                  title={t`Graphify visualization`}
                  // One class list for both states — the height comes from the flex
                  // parent, never from the viewport. The previous height was viewport
                  // math minus a hardcoded 180px for the chrome above it; every guess
                  // that ran short overshot into a second scrollbar, and no constant
                  // can be right in both shells at both breakpoints.
                  className="h-full w-full rounded-xl border border-line bg-surface"
                />
              </ExpandableSection>
            ) : (
              // shrink-0: the honest hint is content-sized, so it sits under the
              // toolbar instead of stretching into a tall empty box of its own.
              <div className="mt-3 shrink-0">
                <Empty>
                  {project.hasGraph
                    ? t`graph.json exists but no visualization — run /graphify <repo> (without --no-viz) to generate graph.html`
                    : t`no graph yet — run /graphify in this repo`}
                </Empty>
              </div>
            )}
          </>
        )
      ) : null}
    </div>
  );
}
