// First-request card (ProjectDetail): how big a session's FIRST request is in
// this project — the cost every session pays before the operator has typed
// anything — and roughly what fills it.
//
// Two kinds of number, never blended: the headline is MEASURED (median of real
// usage over the window, GET /api/projects/{id}/first-request), the stacked bar
// is an ESTIMATE at ~4 bytes per token with "other" as the remainder, and says
// so in its own label. Display-only: nothing here trims or edits a config.

import { plural } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { useEffect, useState } from 'react';
import { fetchProjectFirstRequest } from '../../api';
import type { FirstRequest } from '../../api/types';
import { ErrorBox, Loading } from '../../components/ui';
import { fmtTokens } from '../../lib/format';

type Part = { key: 'claudeMd' | 'mcp' | 'skills' | 'other'; label: string; tokens: number; className: string };

/** The bar's segments in display order; zero-width parts are kept in the
 * legend (a 0 is information) but drawn as nothing. */
function parts(data: FirstRequest, labels: Record<Part['key'], string>): Part[] {
  const e = data.estimate;
  return [
    { key: 'claudeMd', label: labels.claudeMd, tokens: e.claudeMd, className: 'bg-series-1' },
    { key: 'mcp', label: labels.mcp, tokens: e.mcp, className: 'bg-series-2' },
    { key: 'skills', label: labels.skills, tokens: e.skills, className: 'bg-series-3' },
    { key: 'other', label: labels.other, tokens: e.other, className: 'bg-line-stronger' },
  ];
}

/** Pure view over a loaded payload — what the tests render. */
export function FirstRequestView({ data }: { data: FirstRequest }): JSX.Element {
  const { t } = useLingui();
  const { measured, estimate, windowDays } = data;
  const segs = parts(data, {
    claudeMd: t`CLAUDE.md`,
    mcp: t`MCP tools`,
    skills: t`skills`,
    other: t`other parts`,
  });
  const total = segs.reduce((sum, p) => sum + p.tokens, 0);
  const sessions = measured.sessions;
  const promptLabel = fmtTokens(measured.medianPromptTokens);
  const writeLabel = fmtTokens(measured.medianTokens);
  const days = windowDays;
  const nameOnly = estimate.mcpTools - estimate.mcpSchemas;
  const toolCount = estimate.mcpTools;
  const barLabel = segs.map((p) => `${p.label} ~${fmtTokens(p.tokens)}`).join(', ');

  return (
    <div className="rounded-xl border border-line bg-surface px-3.5 py-3" data-testid="first-request-card">
      <div className="flex flex-wrap items-baseline gap-x-2.5 gap-y-1">
        {sessions > 0 ? (
          <>
            <span className="font-mono text-[20px] text-ink" data-testid="first-request-measured">
              {promptLabel}
            </span>
            <span className="font-mono text-[11px] text-ink-dim">
              <Trans>tokens in a session's first request</Trans>
            </span>
          </>
        ) : (
          <span className="font-mono text-[11.5px] text-ink-dim" data-testid="first-request-measured">
            <Trans>no session in the last {days} days to measure</Trans>
          </span>
        )}
      </div>
      {sessions > 0 && (
        <div className="mt-0.5 font-mono text-[10.5px] text-ink-faint">
          {plural(sessions, {
            one: `measured: median of # session over ${days} days · cache write ${writeLabel}`,
            few: `measured: median of # sessions over ${days} days · cache write ${writeLabel}`,
            many: `measured: median of # sessions over ${days} days · cache write ${writeLabel}`,
            other: `measured: median of # sessions over ${days} days · cache write ${writeLabel}`,
          })}
        </div>
      )}

      <div className="mt-3 flex items-center gap-2">
        <span
          className="shrink-0 rounded-full border border-amber/40 bg-amber/10 px-2 py-px font-mono text-[10px] whitespace-nowrap text-amber"
          data-tip={estimate.note}
        >
          <Trans>estimate (~bytes/4)</Trans>
        </span>
        <div
          role="img"
          aria-label={t`estimated split: ${barLabel}`}
          className="flex h-2.5 min-w-0 flex-1 overflow-hidden rounded-full bg-surface2"
        >
          {total > 0 &&
            segs
              .filter((p) => p.tokens > 0)
              .map((p) => (
                <span
                  key={p.key}
                  data-testid={`first-request-seg-${p.key}`}
                  className={p.className}
                  style={{ width: `${String((p.tokens / total) * 100)}%` }}
                />
              ))}
        </div>
      </div>
      <ul className="mt-2 flex flex-wrap gap-x-4 gap-y-1">
        {segs.map((p) => (
          <li key={p.key} className="flex items-center gap-1.5 font-mono text-[10.5px] text-ink-dim">
            <span aria-hidden="true" className={`h-2 w-2 rounded-sm ${p.className}`} />
            <span>{p.label}</span>
            <span className="text-ink-2" data-testid={`first-request-part-${p.key}`}>
              ~{fmtTokens(p.tokens)}
            </span>
          </li>
        ))}
      </ul>
      <div className="mt-2 font-mono text-[10px] text-ink-faint">
        <Trans>
          CLAUDE.md files, MCP tools and skill descriptions are sized from disk at ~4 bytes per token;
          “other parts” are the measured first request minus them (system prompt, built-in tools, agent list).
        </Trans>
        {nameOnly > 0 && (
          <>
            {' '}
            {plural(toolCount, {
              one: `MCP: ${nameOnly} of # tool counted by name only — its schema is readable only from the running server.`,
              few: `MCP: ${nameOnly} of # tools counted by name only — their schemas are readable only from the running server.`,
              many: `MCP: ${nameOnly} of # tools counted by name only — their schemas are readable only from the running server.`,
              other: `MCP: ${nameOnly} of # tools counted by name only — their schemas are readable only from the running server.`,
            })}
          </>
        )}
      </div>
    </div>
  );
}

/** The card as ProjectDetail mounts it: fetch, then FirstRequestView. */
export function FirstRequestCard({ projectId }: { projectId: number }): JSX.Element {
  const { t } = useLingui();
  const [data, setData] = useState<FirstRequest | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    let cancelled = false;
    fetchProjectFirstRequest(projectId)
      .then((d) => {
        if (cancelled) return;
        setData(d);
        setError(null);
      })
      .catch((e: unknown) => {
        if (!cancelled) setError(e instanceof Error ? e.message : String(e));
      });
    return () => {
      cancelled = true;
    };
  }, [projectId, attempt]);

  if (error !== null) return <ErrorBox message={error} onRetry={() => setAttempt((a) => a + 1)} />;
  if (data === null) return <Loading label={t`first request…`} />;
  return <FirstRequestView data={data} />;
}
