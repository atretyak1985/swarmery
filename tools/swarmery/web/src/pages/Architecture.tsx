// Architecture page (/architecture): per-project architecture-map.html served
// through the daemon's jailed static route (architecture-out/). Union-gated:
// the feed lists projects with architecture-pack enabled OR an existing artifact,
// so the dropdown also shows pack-enabled projects that have not yet run
// /architecture-map (hasMap=false). Staleness badge compares analyzedAtCommit
// (baked into the map JSON) against headCommit (current HEAD, no exec).
// Clone of the Graphify page idioms — single load with ErrorBox retry, project
// selection by id, same-origin iframe.

import { useCallback, useEffect, useRef, useState } from 'react';
import type { ProvisionState, ToolsResponse } from '../api/types';
import { fetchTools } from '../api';
import { Card, Empty, ErrorBox, Loading, SectionTitle } from '../components/ui';
import { fmtAgo } from '../lib/format';

// Provision states that are still in flight — while any project sits in one of
// these, the page settle-polls /api/tools until it lands on a terminal state.
const ACTIVE_STATES = new Set<ProvisionState['state']>(['pending', 'installing', 'generating']);
const POLL_MS = 3_000;

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
  // Selection prefers hasMap (a built project) over an unbuilt one.
  const project =
    projects.find((p) => p.id === selectedId) ??
    projects.find((p) => p.hasMap) ??
    projects[0];

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
              </div>
            </Card>
            {project.provision !== null && ACTIVE_STATES.has(project.provision.state) ? (
              <div className="mt-3">
                <span className="inline-flex items-center gap-1.5 rounded-full border border-amber/40 bg-amber/10 px-2.5 py-0.5 font-mono text-[11px] text-amber">
                  <span aria-hidden>⟳</span>
                  {project.provision.state}
                  {project.provision.lastLine !== '' ? ` — ${project.provision.lastLine}` : ''}
                </span>
              </div>
            ) : project.provision?.state === 'failed' ? (
              <div className="mt-3">
                <ErrorBox
                  message={`${project.provision.error !== '' ? project.provision.error : 'provision failed'} — toggle the pack off/on to retry, or run /architecture-map manually`}
                  onRetry={load}
                />
              </div>
            ) : project.hasMap ? (
              <div className="mt-3">
                <iframe
                  key={project.id}
                  src={project.mapPath}
                  title="Architecture map"
                  className="h-[calc(100vh-180px)] w-full rounded-xl border border-line bg-surface"
                />
              </div>
            ) : (
              <div className="mt-3">
                <Empty>no map yet — enabling architecture-pack generates it automatically</Empty>
              </div>
            )}
          </>
        )
      ) : null}
    </div>
  );
}
