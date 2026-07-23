// Architecture page (/architecture): per-project architecture-map.html served
// through the daemon's jailed static route (architecture-out/). Artifact-gated:
// the feed lists only projects that have a built map, so the page's own empty
// state covers direct navigation. Clone of the Graphify page idioms — single
// load with ErrorBox retry, project selection by id, same-origin iframe.

import { useCallback, useEffect, useRef, useState } from 'react';
import type { ToolsResponse } from '../api/types';
import { fetchTools } from '../api';
import { Card, Empty, ErrorBox, Loading, SectionTitle } from '../components/ui';
import { fmtAgo } from '../lib/format';

export function Architecture(): JSX.Element {
  const [data, setData] = useState<ToolsResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [selectedId, setSelectedId] = useState<number | null>(null);
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

  const projects = data?.architecture.projects ?? [];
  const project = projects.find((p) => p.id === selectedId) ?? projects[0];

  return (
    <div className="min-w-0 px-4 pt-6 pb-10 desk:px-10 desk:pt-[34px] desk:pb-[60px]">
      <SectionTitle>architecture</SectionTitle>
      {error !== null && (
        <div className="mb-2">
          <ErrorBox message={error} onRetry={load} />
        </div>
      )}
      {data === null && error === null ? (
        <Loading label="architecture…" />
      ) : data !== null ? (
        project === undefined ? (
          <Empty>no architecture maps yet — run /architecture-map in a project repo</Empty>
        ) : (
          <>
            <Card>
              <div className="flex flex-wrap items-center gap-3">
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
                {project.builtAt !== null && (
                  <span className="font-mono text-[10.5px] text-ink-faint">
                    map built {fmtAgo(project.builtAt)} — commit stamp inside the map header
                  </span>
                )}
              </div>
            </Card>
            <div className="mt-3">
              <iframe
                key={project.id}
                src={project.mapPath}
                title="Architecture map"
                className="h-[calc(100vh-180px)] w-full rounded-xl border border-line bg-surface"
              />
            </div>
          </>
        )
      ) : null}
    </div>
  );
}
