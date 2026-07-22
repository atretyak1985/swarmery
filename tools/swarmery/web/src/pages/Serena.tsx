// Serena page (/serena): daemon-managed serena LSP dashboards for lsp-pack
// projects. A project dropdown, a state pill (stopped/starting/running/failed),
// start/stop with 2s settle-polling (max 30s), a collapsible log tail while
// not running, and a same-origin iframe of the dashboard when running.
// The route exists only while the sidebar item does (serena.projects > 0),
// but the page still renders honest empty states on direct navigation.

import { useCallback, useEffect, useRef, useState } from 'react';
import type { ToolsResponse, ToolsSerenaProject } from '../api/types';
import { fetchTools, serenaStart, serenaStop } from '../api';
import { Card, Empty, ErrorBox, Loading, SectionTitle } from '../components/ui';
import { fmtAgo } from '../lib/format';

const SETTLE_POLL_MS = 2_000;
const SETTLE_MAX_MS = 30_000;

const PILL_CLASS: Record<ToolsSerenaProject['state'], string> = {
  stopped: 'border-line text-ink-faint',
  starting: 'border-amber/40 bg-amber/10 text-amber',
  running: 'border-green/40 bg-green/10 text-green',
  failed: 'border-red/40 bg-red/10 text-red',
};

function StatePill({ state }: { state: ToolsSerenaProject['state'] }): JSX.Element {
  return (
    <span
      className={`rounded-full border px-2.5 py-0.5 font-mono text-[10px] ${PILL_CLASS[state]}`}
    >
      {state}
    </span>
  );
}

export function Serena(): JSX.Element {
  const [data, setData] = useState<ToolsResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // Selection is kept by project id so it survives reloads; null → default
  // (first running project, else the first entry).
  const [selectedId, setSelectedId] = useState<number | null>(null);

  // Unmount guard shared by load / actions / settle-polling (ProjectPlugins
  // idiom): a ref because these callbacks outlive any single effect run.
  const aliveRef = useRef(true);
  const pollTimer = useRef<number | null>(null);

  const stopPolling = useCallback((): void => {
    if (pollTimer.current !== null) {
      window.clearInterval(pollTimer.current);
      pollTimer.current = null;
    }
  }, []);

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
      stopPolling();
    };
  }, [load, stopPolling]);

  // After a start/stop: refetch every 2s until the project's state settles
  // (running/stopped/failed — anything but 'starting'), 30s at most.
  const pollUntilSettled = useCallback(
    (id: number): void => {
      stopPolling();
      const deadline = Date.now() + SETTLE_MAX_MS;
      pollTimer.current = window.setInterval(() => {
        if (Date.now() > deadline) {
          stopPolling();
          return;
        }
        fetchTools()
          .then((d) => {
            if (!aliveRef.current) return;
            setData(d);
            const p = d.serena.projects.find((x) => x.id === id);
            if (p === undefined || p.state !== 'starting') stopPolling();
          })
          .catch(() => {
            /* transient — the deadline check above bounds the retries */
          });
      }, SETTLE_POLL_MS);
    },
    [stopPolling],
  );

  const projects = data?.serena.projects ?? [];
  const project =
    projects.find((p) => p.id === selectedId) ??
    projects.find((p) => p.state === 'running') ??
    projects[0];

  const toggle = (p: ToolsSerenaProject): void => {
    const stopping = p.state === 'starting' || p.state === 'running';
    setBusy(true);
    (stopping ? serenaStop(p.id) : serenaStart(p.id))
      .then(() => {
        if (!aliveRef.current) return;
        setError(null);
        load();
        pollUntilSettled(p.id);
      })
      .catch((e: unknown) => {
        if (!aliveRef.current) return;
        setError(e instanceof Error ? e.message : String(e));
      })
      .finally(() => {
        if (aliveRef.current) setBusy(false);
      });
  };

  return (
    <div className="min-w-0 px-4 pt-6 pb-10 desk:px-10 desk:pt-[34px] desk:pb-[60px]">
      <SectionTitle>serena</SectionTitle>
      {error !== null && (
        <div className="mb-2">
          <ErrorBox message={error} onRetry={load} />
        </div>
      )}
      {data === null && error === null ? (
        <Loading label="serena…" />
      ) : data !== null ? (
        !data.serena.available ? (
          <Empty>serena binary not found on this machine</Empty>
        ) : project === undefined ? (
          <Empty>
            no projects with lsp-pack enabled — enable it in a project&apos;s plugins card
          </Empty>
        ) : (
          <>
            <Card>
              <div className="flex flex-wrap items-center gap-3">
                <select
                  value={String(project.id)}
                  onChange={(e) => setSelectedId(Number(e.target.value))}
                  aria-label="serena project"
                  className="rounded-[9px] border border-line-strong bg-field px-2.5 py-[6px] font-mono text-[12px] text-ink transition-colors outline-none focus:border-ink-dim"
                >
                  {projects.map((p) => (
                    <option key={p.id} value={String(p.id)}>
                      {p.name ?? p.slug}
                    </option>
                  ))}
                </select>
                <StatePill state={project.state} />
                {project.startedAt !== null && (
                  <span className="font-mono text-[10.5px] text-ink-faint">
                    started {fmtAgo(project.startedAt)}
                  </span>
                )}
                <button
                  type="button"
                  disabled={busy}
                  aria-label={
                    busy
                      ? 'busy'
                      : project.state === 'running' || project.state === 'starting'
                        ? 'stop serena'
                        : 'start serena'
                  }
                  onClick={() => toggle(project)}
                  className="ml-auto rounded-lg border border-line bg-surface px-3 py-1.5 text-[12px] font-semibold text-ink-2 transition-colors hover:bg-surface2 disabled:cursor-not-allowed disabled:opacity-50"
                >
                  {busy
                    ? '…'
                    : project.state === 'starting' || project.state === 'running'
                      ? 'stop'
                      : 'start'}
                </button>
              </div>
              {project.state === 'failed' && project.error !== '' && (
                <div className="mt-2 font-mono text-[11px] text-red">{project.error}</div>
              )}
              {project.state !== 'running' && project.logTail.length > 0 && (
                <details className="mt-2">
                  <summary className="cursor-pointer font-mono text-[10.5px] text-ink-faint transition-colors hover:text-ink">
                    log tail ({project.logTail.length})
                  </summary>
                  <div className="mt-1.5 rounded-lg border border-line bg-field px-3 py-2">
                    {project.logTail.map((line, i) => (
                      <div
                        // eslint-disable-next-line react/no-array-index-key -- append-only tail
                        key={i}
                        className="font-mono text-[10px] leading-[1.7] break-all text-ink-dim"
                      >
                        {line}
                      </div>
                    ))}
                  </div>
                </details>
              )}
            </Card>
            {project.state === 'running' && (
              <div className="mt-3">
                <iframe
                  key={project.id}
                  src={project.dashboardPath}
                  title="Serena dashboard"
                  className="h-[calc(100vh-220px)] w-full rounded-xl border border-line bg-surface"
                />
              </div>
            )}
          </>
        )
      ) : null}
    </div>
  );
}
