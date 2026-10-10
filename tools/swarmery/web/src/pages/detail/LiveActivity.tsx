// Live "is the agent working?" indicator, shown at the bottom of the Chat.
//
// Two honest modes, because the daemon only reads transcripts:
//   • our own headless resume in flight (resumeInFlight) → we KNOW it is
//     working and since when → an animated spinner + rotating gerund +
//     ticking elapsed timer ("Working · Ionizing… · 1m 08s").
//   • a live OS process (proc_state running/orphaned — regardless of the
//     time-based status, which idles out during long silent tool calls) →
//     working spinner while output is fresh, and a "live · working quietly"
//     pulse through silent stretches (builds, test suites, long thinks).
// Renders nothing when the session has no live process — that absence IS the
// "session is not doing anything" signal the operator reads.

import type { I18n } from '@lingui/core';
import { msg } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { useEffect, useState } from 'react';
import type { SessionDetail } from '../../api/types';
import { fmtAgo } from '../../lib/format';

const GERUNDS = [
  msg`Thinking`,
  msg`Reasoning`,
  msg`Ionizing`,
  msg`Computing`,
  msg`Percolating`,
  msg`Synthesizing`,
  msg`Crunching`,
  msg`Cogitating`,
  msg`Working`,
  msg`Noodling`,
];

/** The gerund for a 4-second slot, rotating through GERUNDS. */
function gerundAt(i18n: I18n, ms: number): string {
  const message = GERUNDS[Math.floor(ms / 4000) % GERUNDS.length];
  return message !== undefined ? i18n._(message) : '';
}

function elapsedLabel(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  if (s < 60) return `${String(s)}s`;
  const m = Math.floor(s / 60);
  return `${String(m)}m ${String(s % 60).padStart(2, '0')}s`;
}

// Process aliveness alone drives the indicator — NOT the time-based status.
// A long silent tool call (a full test suite, a build) writes nothing to the
// transcript for minutes, which flips status active→idle; the OS process is
// still there and procwatch refreshes proc_state every 30s. The contract the
// operator relies on: indicator visible ⇔ the session's process is alive.
function hasLiveProcess(detail: SessionDetail): boolean {
  return detail.procState === 'running' || detail.procState === 'orphaned';
}

export function LiveActivity({ detail }: { detail: SessionDetail }): JSX.Element | null {
  const { t, i18n } = useLingui();
  const resuming = detail.resumeInFlight === true;
  const live = hasLiveProcess(detail);
  const active = resuming || live;

  // Tick once a second only while something is live (cheap, and stops on idle).
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return;
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [active]);

  if (resuming) {
    const startedMs =
      detail.resumeStartedAt != null ? new Date(detail.resumeStartedAt).getTime() : now;
    const elapsed = Number.isNaN(startedMs) ? 0 : now - startedMs;
    const gerund = gerundAt(i18n, elapsed);
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
    const lastMs = lastEvent !== undefined ? new Date(lastEvent.ts).getTime() : NaN;
    const sinceOutput = Number.isNaN(lastMs) ? Infinity : now - lastMs;

    // Fresh output (< 2 min) → the agent is visibly producing: full working
    // spinner. Quiet stretch → the process is alive but inside a silent tool
    // call (build, test suite, long think): keep the indicator up, say so.
    if (sinceOutput < 2 * 60_000) {
      const gerund = gerundAt(i18n, now);
      const lastAgo = lastEvent !== undefined ? fmtAgo(lastEvent.ts) : '';
      return (
        <div className="my-2 flex items-center gap-2.5 font-mono text-[11.5px] text-brand">
          <span
            className="h-3 w-3 shrink-0 animate-spin rounded-full border-[1.5px] border-brand/30 border-t-brand"
            aria-hidden="true"
          />
          <span>
            {gerund}
            <span className="animate-pulse">…</span>
            {lastEvent !== undefined && (
              <span className="text-ink-faint">
                {' · '}
                <Trans>last output {lastAgo}</Trans>
              </span>
            )}
          </span>
        </div>
      );
    }
    const quietFor = lastEvent !== undefined ? fmtAgo(lastEvent.ts) : '';
    return (
      <div
        className="my-2 flex items-center gap-2 font-mono text-[11px] text-ink-faint"
        data-tip={t`the OS process is alive (procwatch, 30s resolution); nothing written to the transcript during this stretch — typical for long builds/tests`}
      >
        <span
          className="h-[7px] w-[7px] shrink-0 animate-blink-dot rounded-full bg-green"
          aria-hidden="true"
        />
        <span>
          <Trans>live · working quietly</Trans>
          {lastEvent !== undefined ? ` · ${t`no output for ${quietFor}`}` : ''}
        </span>
      </div>
    );
  }

  return null;
}
