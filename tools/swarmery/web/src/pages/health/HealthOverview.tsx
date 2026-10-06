// Health → Overview (Canvas v3 phase 5, artboard 2c): the one sentence about
// the fleet, the three agents that moved it, and the friction the operator can
// remove right now — one denied tool no rule covers (with "+ always allow")
// and the most repeated error. Everything past that lives one tab over.

import { useCallback, useState } from 'react';
import { createApprovalRule } from '../../api';
import type { RetroAgentRow, RetroAgentsResp, RetroFrictionResp } from '../../api/types';
import { ErrorBox, Loading } from '../../components/ui';
import { fmtAgo } from '../../lib/format';
import {
  type HealthTab,
  fmtPct,
  oneSentence,
  topAgents,
  uncoveredDenied,
  untriagedErrors,
} from './healthModel';

const LABEL = 'font-mono text-[10px] tracking-[0.14em] text-ink-faint uppercase';

/** Tone of an agent row: improved → green, still high → amber, else muted. */
function agentTone(row: RetroAgentRow): { text: string; bar: string } {
  const now = row.error_rate * 100;
  const prev = row.prev.runs > 0 ? row.prev.error_rate * 100 : null;
  if (prev !== null && prev - now >= 2) return { text: 'text-green', bar: 'bg-green' };
  if (now >= 15) return { text: 'text-amber', bar: 'bg-amber' };
  return { text: 'text-ink-3', bar: 'bg-ink-3' };
}

function agentFigure(row: RetroAgentRow): string {
  const now = row.error_rate * 100;
  const prev = row.prev.runs > 0 ? row.prev.error_rate * 100 : null;
  if (prev !== null && Math.abs(prev - now) >= 2) return `${fmtPct(prev)} → ${fmtPct(now)} failed`;
  return `${fmtPct(now)} failed · ${String(row.runs)} runs`;
}

function AgentRow({ row }: { row: RetroAgentRow }): JSX.Element {
  const tone = agentTone(row);
  const width = Math.min(100, Math.max(0, Math.round(row.error_rate * 100)));
  return (
    <div className="flex items-center gap-3 rounded-[10px] border border-line px-3 py-2.5">
      <span className="min-w-0 flex-1 truncate text-[12.5px] text-ink">{row.agent}</span>
      <span className={`font-mono text-[11px] ${tone.text}`}>{agentFigure(row)}</span>
      <span className="h-1.5 w-[120px] shrink-0 overflow-hidden rounded-[3px] bg-line" aria-hidden="true">
        <span className={`block h-full ${tone.bar}`} style={{ width: `${String(width)}%` }} />
      </span>
    </div>
  );
}

function DeniedCard({
  tool,
  denied,
  calls,
  onDetails,
}: {
  tool: string;
  denied: number;
  calls: number;
  onDetails: () => void;
}): JSX.Element {
  const [state, setState] = useState<'idle' | 'busy' | 'done'>('idle');
  const [failed, setFailed] = useState<string | null>(null);
  const allow = useCallback((): void => {
    if (state !== 'idle') return;
    setState('busy');
    setFailed(null);
    createApprovalRule({ projectId: null, toolPattern: tool, note: 'created from Health overview' })
      .then(() => setState('done'))
      .catch((e: unknown) => {
        setFailed(String(e));
        setState('idle');
      });
  }, [state, tool]);
  return (
    <div className="rounded-[10px] border border-line px-3 py-2.5">
      <div className="flex items-baseline gap-2">
        <span className="font-mono text-[12px] text-ink">{tool}</span>
        <span className="font-mono text-[10.5px] text-ink-faint">
          denied {String(denied)}× · of {String(calls)} calls
        </span>
      </div>
      <div className="mt-1.5 flex gap-1.5">
        {state === 'done' ? (
          <span className="rounded-md border border-green/40 px-2.5 py-[3px] font-mono text-[10.5px] text-green">
            ✓ always allowed
          </span>
        ) : (
          <button
            type="button"
            onClick={allow}
            disabled={state === 'busy'}
            data-tip={`auto-approve every ${tool} request, in every project`}
            className="rounded-md border border-amber/50 px-2.5 py-[3px] font-mono text-[10.5px] font-semibold text-amber transition-colors hover:bg-amber/10 disabled:opacity-50"
          >
            {state === 'busy' ? '…' : '+ always allow'}
          </button>
        )}
        <button
          type="button"
          onClick={onDetails}
          className="rounded-md border border-line-strong px-2.5 py-[3px] font-mono text-[10.5px] text-ink-3 transition-colors hover:text-ink"
        >
          see friction
        </button>
      </div>
      {failed !== null && <div className="mt-1.5 font-mono text-[10.5px] text-red">{failed}</div>}
    </div>
  );
}

export function HealthOverview({
  agents,
  friction,
  error,
  onRetry,
  onTab,
}: {
  agents: RetroAgentsResp | null;
  friction: RetroFrictionResp | null;
  error: string | null;
  onRetry: () => void;
  onTab: (tab: HealthTab) => void;
}): JSX.Element {
  if (error !== null) {
    return (
      <div className="px-7 py-5">
        <ErrorBox message={error} onRetry={onRetry} />
      </div>
    );
  }
  if (agents === null) return <Loading label="health…" />;

  const rows = topAgents(agents, 3);
  const denied = friction !== null ? uncoveredDenied(friction)[0] : undefined;
  const repeated = friction !== null ? untriagedErrors(friction)[0] : undefined;

  return (
    <div className="grid gap-[22px] px-4 pt-[22px] pb-[26px] desk:grid-cols-[minmax(0,1.2fr)_minmax(0,1fr)] desk:px-7">
      <section>
        <div className={LABEL}>The one sentence</div>
        <p className="mt-2 font-display text-[19px] leading-[1.35] font-medium text-balance text-ink">
          {oneSentence(agents)}
        </p>
        {rows.length > 0 && (
          <div className="mt-3.5 flex flex-col gap-2">
            {rows.map((row) => (
              <AgentRow key={row.agent} row={row} />
            ))}
          </div>
        )}
        <button
          type="button"
          onClick={() => onTab('agents')}
          className="mt-2 font-mono text-[10.5px] text-ink-faint transition-colors hover:text-ink"
        >
          all {String(agents.agents.length)} agents → Agents tab
        </button>
      </section>

      <section>
        <div className={LABEL}>Friction you can remove today</div>
        <div className="mt-2 flex flex-col gap-2">
          {denied !== undefined && (
            <DeniedCard
              tool={denied.tool}
              denied={denied.denied}
              calls={denied.calls}
              onDetails={() => onTab('friction')}
            />
          )}
          {repeated !== undefined && (
            <div className="rounded-[10px] border border-line px-3 py-2.5">
              <div className="flex min-w-0 items-baseline gap-2">
                <span className="shrink-0 text-[12px] text-ink">
                  Same error {String(repeated.count)} times in this window
                </span>
                <span className="min-w-0 truncate font-mono text-[10.5px] text-ink-faint">
                  {repeated.example}
                </span>
              </div>
              <div className="mt-1 text-[11.5px] text-ink-3">
                Last seen {fmtAgo(repeated.last_ts)}
                {repeated.sessions.length > 0 &&
                  ` · in ${String(repeated.sessions.length)} session${repeated.sessions.length === 1 ? '' : 's'}`}
                .
              </div>
            </div>
          )}
          {friction === null ? (
            <p className="text-[12px] text-ink-dim">The friction board is unavailable right now.</p>
          ) : denied === undefined && repeated === undefined ? (
            <p className="text-[12px] text-ink-dim">
              Nothing to remove: no tool was denied without a rule and no error repeated in this window.
            </p>
          ) : null}
        </div>
      </section>
    </div>
  );
}
