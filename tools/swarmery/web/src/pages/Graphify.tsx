// Graphify page (/graphify): static graph.html visualizations for
// graphify-pack projects, served through the daemon's jailed static route.
// A project dropdown, a "graph built …" info row, and a same-origin iframe
// when the viz artifact exists — honest hints otherwise. Artifacts are
// static files, so a single load with ErrorBox retry is enough (no polling);
// there is deliberately no rebuild button (the daemon does not run graphify).
// The route exists only while the sidebar item does (graphify.projects > 0),
// but the page still renders an honest empty state on direct navigation.

import { useCallback, useEffect, useRef, useState } from 'react';
import type { ToolsResponse } from '../api/types';
import { fetchTools } from '../api';
import { Card, Empty, ErrorBox, Loading, SectionTitle } from '../components/ui';
import { fmtAgo } from '../lib/format';

export function Graphify(): JSX.Element {
  const [data, setData] = useState<ToolsResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  // Selection is kept by project id so it survives reloads; null → default
  // (first project with a viz, else the first entry).
  const [selectedId, setSelectedId] = useState<number | null>(null);

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
  const project =
    projects.find((p) => p.id === selectedId) ??
    projects.find((p) => p.hasViz) ??
    projects[0];

  return (
    <div className="min-w-0 px-4 pt-6 pb-10 desk:px-10 desk:pt-[34px] desk:pb-[60px]">
      <SectionTitle>graphify</SectionTitle>
      {error !== null && (
        <div className="mb-2">
          <ErrorBox message={error} onRetry={load} />
        </div>
      )}
      {data === null && error === null ? (
        <Loading label="graphify…" />
      ) : data !== null ? (
        project === undefined ? (
          <Empty>
            no projects with graphify-pack enabled — enable it in a project&apos;s plugins card
          </Empty>
        ) : (
          <>
            <Card>
              <div className="flex flex-wrap items-center gap-3">
                <select
                  value={String(project.id)}
                  onChange={(e) => setSelectedId(Number(e.target.value))}
                  aria-label="graphify project"
                  className="rounded-[9px] border border-line-strong bg-field px-2.5 py-[6px] font-mono text-[12px] text-ink transition-colors outline-none focus:border-ink-dim"
                >
                  {projects.map((p) => (
                    <option key={p.id} value={String(p.id)}>
                      {p.name ?? p.slug}
                    </option>
                  ))}
                </select>
                {project.builtAt !== null && (
                  <span className="font-mono text-[10.5px] text-ink-faint">
                    graph built {fmtAgo(project.builtAt)}
                  </span>
                )}
              </div>
            </Card>
            {project.hasViz ? (
              <div className="mt-3">
                <iframe
                  key={project.id}
                  src={project.vizPath}
                  title="Graphify visualization"
                  className="h-[calc(100vh-180px)] w-full rounded-xl border border-line bg-surface"
                />
              </div>
            ) : (
              <div className="mt-3">
                <Empty>
                  {project.hasGraph
                    ? 'graph.json exists but no visualization — run /graphify <repo> (without --no-viz) to generate graph.html'
                    : 'no graph yet — run /graphify in this repo'}
                </Empty>
              </div>
            )}
          </>
        )
      ) : null}
    </div>
  );
}
