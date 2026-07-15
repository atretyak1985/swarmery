// Live "is the agent working?" indicator, shown at the bottom of the Chat.
//
// Two honest modes, because the daemon only reads transcripts:
//   • our own headless resume in flight (resumeInFlight) → we KNOW it is
//     working and since when → an animated spinner + rotating gerund +
//     ticking elapsed timer ("Working · Ionizing… · 1m 08s").
//   • a live terminal process (proc_state running/orphaned, status active) →
//     we can only see transcript writes, so a softer pulse with the time since
//     the last output ("live · last output 9 s ago"). A silent extended-reason
//     turn writes nothing, so this is best-effort, not a mid-turn guarantee.
// Renders nothing when the session is neither.

import { useEffect, useState } from 'react';
import type { SessionDetail } from '../../api/types';
import { fmtAgo } from '../../lib/format';

const GERUNDS = [
  'Thinking',
  'Reasoning',
  'Ionizing',
  'Computing',
  'Percolating',
  'Synthesizing',
  'Crunching',
  'Cogitating',
  'Working',
  'Noodling',
];

function elapsedLabel(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  if (s < 60) return `${String(s)}s`;
  const m = Math.floor(s / 60);
  return `${String(m)}m ${String(s % 60).padStart(2, '0')}s`;
}

function hasLiveProcess(detail: SessionDetail): boolean {
  return (
    (detail.procState === 'running' || detail.procState === 'orphaned') &&
    detail.status === 'active'
  );
}

export function LiveActivity({ detail }: { detail: SessionDetail }): JSX.Element | null {
  const resuming = detail.resumeInFlight === true;
  const live = hasLiveProcess(detail);
  const active = resuming || live;

  // Tick once a second only while something is live (cheap, and stops on idle).
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [active]);

  if (resuming) {
    const startedMs =
      detail.resumeStartedAt != null ? new Date(detail.resumeStartedAt).getTime() : now;
    const elapsed = Number.isNaN(startedMs) ? 0 : now - startedMs;
    const gerund = GERUNDS[Math.floor(elapsed / 4000) % GERUNDS.length];
    return (
      <div className="my-2 flex items-center gap-2.5 font-mono text-[11.5px] text-brand">
        <span
          className="h-3 w-3 shrink-0 animate-spin rounded-full border-[1.5px] border-brand/30 border-t-brand"
          aria-hidden="true"
        />
        <span>
          {gerund}
          <span className="animate-pulse">…</span>
          <span className="text-ink-faint"> · {elapsedLabel(elapsed)}</span>
        </span>
      </div>
    );
  }

  if (live) {
    const lastEvent =
      detail.events.length > 0 ? detail.events[detail.events.length - 1] : undefined;
    return (
      <div className="my-2 flex items-center gap-2 font-mono text-[11px] text-ink-faint">
        <span
          className="h-[7px] w-[7px] shrink-0 animate-blink-dot rounded-full bg-green"
          aria-hidden="true"
        />
        <span>
          live{lastEvent !== undefined ? ` · last output ${fmtAgo(lastEvent.ts)}` : ''}
        </span>
      </div>
    );
  }

  return null;
}
