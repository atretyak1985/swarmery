// Serena page (/serena): daemon-managed serena LSP dashboards for lsp-pack
// projects. A project dropdown, a state pill (stopped/starting/running/failed),
// start/stop with 2s settle-polling (max 30s), a collapsible log tail while
// not running, and an iframe of serena's own dashboard origin when running
// (dashboardUrl — the daemon proxy can't host it: serena's dashboard.js makes
// root-absolute ajax calls that escape any path prefix).
// The route exists only while the sidebar item does (serena.projects > 0),
// but the page still renders honest empty states on direct navigation.

import { Trans, useLingui } from '@lingui/react/macro';
import { useCallback, useEffect, useRef, useState } from 'react';
import type { ToolsResponse, ToolsSerenaProject } from '../api/types';
import { fetchTools, serenaStart, serenaStop } from '../api';
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

export function Serena({ scopedSlug }: { scopedSlug?: string } = {}): JSX.Element {
  const { t } = useLingui();
  const [data, setData] = useState<ToolsResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // Selection is kept by project id so it survives reloads; null → default
  // (first running project, else the first entry). In project-workspace mode
  // (scopedSlug set) the workspace switcher owns the project, so the in-page
  // dropdown is hidden and the selection is pinned to that slug.
  const [selectedId, setSelectedId] = useState<number | null>(null);
  // Expanded state only — Esc, the body scroll lock, focus containment and focus
  // restore all belong to <ExpandableSection> (components/ui.tsx), the same
  // primitive pages/Architecture.tsx uses. Note the frame below is cross-origin,
  // so the section's in-frame Esc listener cannot attach here: while focus is
  // inside serena's dashboard, Esc belongs to that document and the ✕ close
  // button (or clicking back out to the host) is the way out.
  const [expanded, setExpanded] = useState(false);

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
  const scoped = scopedSlug !== undefined;
  const project = scoped
    ? (findProject(projects, scopedSlug ?? null) ?? undefined)
    : (projects.find((p) => p.id === selectedId) ??
      projects.find((p) => p.state === 'running') ??
      projects[0]);

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

  const startedAgo =
    project !== undefined && project.startedAt !== null ? fmtAgo(project.startedAt) : '';
  const logLines = project?.logTail.length ?? 0;

  return (
    // Fill route (`handle: { fill: true }`, src/main.tsx): the shell has stopped
    // scrolling, so the page is the flex column that spends the leftover height —
    // every fixed-height chunk is `shrink-0` and the dashboard pane takes the rest.
    // The bottom padding is a gap under the pane, not the old document-page rhythm.
    <div className="flex h-full min-h-0 min-w-0 flex-col px-4 pt-6 pb-4 desk:px-10 desk:pt-[34px] desk:pb-6">
      {/* SectionTitle owns its margins and takes no className, so the shrink-0
          flex item is a wrapper around it. */}
      <div className="shrink-0">
        <SectionTitle>
          <Trans>serena</Trans>
        </SectionTitle>
      </div>
      {error !== null && (
        <div className="mb-2 shrink-0">
          <ErrorBox message={error} onRetry={load} />
        </div>
      )}
      {data === null && error === null ? (
        <Loading label={t`serena…`} />
      ) : data !== null ? (
        !data.serena.available ? (
          <Empty>
            <Trans>serena binary not found on this machine</Trans>
          </Empty>
        ) : project === undefined ? (
          <Empty>
            {scoped
              ? t`lsp-pack is not enabled for this project — enable it in Settings`
              : t`no projects with lsp-pack enabled — enable it in a project’s plugins card`}
          </Empty>
        ) : (
          <>
            {/* The fragment is transparent to layout: this Card and the dashboard
                pane below are both flex items of the page root. */}
            <Card className="shrink-0">
              <div className="flex flex-wrap items-center gap-3">
                {!scoped && (
                  <select
                    value={String(project.id)}
                    onChange={(e) => setSelectedId(Number(e.target.value))}
                    aria-label={t`serena project`}
                    className="rounded-[9px] border border-line-strong bg-field px-2.5 py-[6px] font-mono text-[12px] text-ink transition-colors outline-none focus:border-ink-dim"
                  >
                    {projects.map((p) => (
                      <option key={p.id} value={String(p.id)}>
                        {p.name ?? p.slug}
                      </option>
                    ))}
                  </select>
                )}
                <StatePill state={project.state} />
                {project.startedAt !== null && (
                  <span className="font-mono text-[10.5px] text-ink-faint">
                    <Trans>started {startedAgo}</Trans>
                  </span>
                )}
                <span className="ml-auto flex items-center gap-2">
                  <button
                    type="button"
                    disabled={busy}
                    aria-label={
                      busy
                        ? t`busy`
                        : project.state === 'running' || project.state === 'starting'
                          ? t`stop serena`
                          : t`start serena`
                    }
                    onClick={() => toggle(project)}
                    className="rounded-lg border border-line bg-surface px-3 py-1.5 text-[12px] font-semibold text-ink-2 transition-colors hover:bg-surface2 disabled:cursor-not-allowed disabled:opacity-50"
                  >
                    {busy
                      ? '…'
                      : project.state === 'starting' || project.state === 'running'
                        ? t`stop`
                        : t`start`}
                  </button>
                  {/* Only meaningful while there is a pane to expand. */}
                  {project.state === 'running' && project.dashboardUrl !== '' && (
                    <ExpandButton onClick={() => setExpanded(true)} expanded={expanded} />
                  )}
                </span>
              </div>
              {project.state === 'failed' && project.error !== '' && (
                // break-all for the same reason the log tail below has it: this
                // is verbatim tool output, so it can be one unbroken token (a
                // path, a URL, a stack frame) that no word-break rule would
                // split — and at 390px that token is what pushes the card past
                // the viewport into a horizontal scrollbar.
                <div className="mt-2 font-mono text-[11px] break-all text-red">{project.error}</div>
              )}
              {project.state !== 'running' && project.logTail.length > 0 && (
                <details className="mt-2">
                  <summary className="cursor-pointer font-mono text-[10.5px] text-ink-faint transition-colors hover:text-ink">
                    <Trans>log tail ({logLines})</Trans>
                  </summary>
                  {/* The tail is unbounded (it grows with the log) and the card
                      above is `shrink-0`, so an open <details> would push the page
                      past the viewport and put the scrollbar back on the whole
                      document — the exact thing fill mode removes. Cap it and let
                      the tail scroll inside itself, the `max-h-… + overflow-y-auto`
                      idiom the rest of the app uses for log/JSON bodies. */}
                  <div className="mt-1.5 max-h-40 overflow-y-auto rounded-lg border border-line bg-field px-3 py-2">
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
            {project.state === 'running' && project.dashboardUrl !== '' && (
              // The pane that spends the leftover height (flex-1/min-h-0 come from
              // ExpandableSection's own collapsed class), and the same subtree in
              // both states: expanding only swaps the wrapper's classes, so the
              // iframe is never remounted and the dashboard keeps its session.
              //
              // CROSS-ORIGIN, deliberately: `dashboardUrl` is serena's own origin
              // (127.0.0.1:24282), not the daemon proxy — serena's dashboard.js
              // makes root-absolute ajax calls that escape any path prefix. So the
              // embed-CSS/theme injection pages/Architecture.tsx does into its
              // same-origin frame is impossible here and is not missing by
              // oversight: the host may only set this frame's OUTER box (height
              // via the flex parent) and expand it. Nothing inside it can be
              // restyled, and Esc does not reach the host while focus is in the
              // frame — the ✕ close button covers that.
              <ExpandableSection
                expanded={expanded}
                onToggle={setExpanded}
                label={t`serena dashboard`}
                className="mt-3"
              >
                <iframe
                  key={project.id}
                  src={project.dashboardUrl}
                  title={t`Serena dashboard`}
                  // One class list for both states — the height comes from the flex
                  // parent, never from the viewport. The previous height was viewport
                  // math minus a hardcoded 220px for the chrome above it; every guess
                  // that ran short overshot into a second scrollbar, and no constant
                  // can be right in both shells at both breakpoints.
                  className="h-full w-full rounded-xl border border-line bg-surface"
                />
              </ExpandableSection>
            )}
          </>
        )
      ) : null}
    </div>
  );
}
