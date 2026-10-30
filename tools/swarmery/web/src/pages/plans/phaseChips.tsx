// The small marks a phase carries: its lifecycle chip, liveness of a run, how the
// last run ended, and the progress bar.

import type { MessageDescriptor } from '@lingui/core';
import { msg, t } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { useEffect, useState } from 'react';
import type { EpicPhase, PhaseRunOutcome, RunEvent } from '../../api/types';
import type { PhaseStatus } from './planModel';

/** Re-renders on an interval so elapsed labels (doc activity, running phases)
 * stay fresh. */
export function useNow(intervalMs: number): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => {
      setNow(Date.now());
    }, intervalMs);
    return () => {
      clearInterval(id);
    };
  }, [intervalMs]);
  return now;
}

function formatAgo(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  if (s < 60) return `${String(s)}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${String(m)}m`;
  const h = Math.floor(m / 60);
  return `${String(h)}h ${String(m % 60)}m`;
}

/** Past this silence window an in-progress phase is flagged as possibly stuck. */
const STALL_AFTER_MS = 15 * 60_000;

/** Liveness pulse for a phase with a LIVE run. Every executor edit (checkbox
 * tick, Status flip) touches the phase doc, so "time since last doc edit"
 * answers the question the chip alone can't: is the session actually working
 * right now, or has it silently died?
 *
 * That question only exists while a process is attached. With `running={false}`
 * there is no executor to be stuck, so the pulse and the stuck-executor tooltip
 * are dropped for a neutral `edited <ago>` — the doc's mtime, stated as such.
 * The old markup showed `no activity · 1h 7m` in amber on phases whose run had
 * exited cleanly an hour earlier, which read as an alarm about nothing. */
export function PhaseActivity({
  docUpdatedAt,
  running,
}: {
  docUpdatedAt: string | null;
  running: boolean;
}): JSX.Element | null {
  const { t } = useLingui();
  const now = useNow(30_000);
  if (docUpdatedAt === null) return null;
  const elapsed = now - Date.parse(docUpdatedAt);
  const ago = formatAgo(elapsed);
  if (!running)
    return (
      <span
        className="inline-flex shrink-0 items-center gap-1 font-mono text-[9.5px] text-ink-faint"
        data-tip={t`when this phase doc was last edited — no run is attached to it`}
      >
        <Trans>edited {ago} ago</Trans>
      </span>
    );
  const stalled = elapsed > STALL_AFTER_MS;
  return (
    <span
      className={`inline-flex shrink-0 items-center gap-1 font-mono text-[9.5px] ${
        stalled ? 'text-amber' : 'text-brand'
      }`}
      data-tip={
        stalled
          ? t`no phase-doc edits for a while — the executor may be stuck`
          : t`the executor is actively editing this phase doc`
      }
    >
      <span className={`h-1.5 w-1.5 rounded-full ${stalled ? 'bg-amber' : 'animate-pulse bg-brand'}`} />
      {stalled ? t`no activity · ${ago}` : t`active · ${ago} ago`}
    </span>
  );
}

/** Status chips. `in_progress` says `started` because that is all the DOCUMENT
 * can support — checkboxes ticked, or a `Status: In progress` marker. `running`
 * is reserved for a live process and is resolved by `phaseChip` below. */
export const PHASE_CHIP: Record<PhaseStatus, { label: MessageDescriptor; cls: string }> = {
  done: { label: msg`done`, cls: 'border-green/40 text-green' },
  // Every criterion ticked and the grade the doc asked for never arrived. Amber,
  // the app's needs-a-human colour — NOT green, because nobody confirmed this, and
  // NOT red, because nothing says the work is wrong.
  unverified: { label: msg`unverified`, cls: 'border-amber/40 bg-amber/10 text-amber' },
  // Work that landed and was never written down. Amber like `unverified` — both
  // need a human — but its own label, because the fix is different: write the
  // Completion Report and record the lesson, not re-run a grader.
  unreported: { label: msg`unreported`, cls: 'border-amber/40 bg-amber/10 text-amber' },
  in_progress: { label: msg`started`, cls: 'border-brand/40 text-brand' },
  blocked: { label: msg`blocked`, cls: 'border-red/40 text-red' },
  pending: { label: msg`pending`, cls: 'border-line text-ink-faint' },
};

/** The chip a phase row actually renders. The `running` variant lives HERE and
 * not in `PhaseStatus`, so the union — and every dependency/completion decision
 * derived from it — stays a pure function of the plan documents. */
export function phaseChip(status: PhaseStatus, running: boolean): { label: MessageDescriptor; cls: string } {
  if (running && status === 'in_progress')
    return { label: msg`running`, cls: PHASE_CHIP.in_progress.cls };
  return PHASE_CHIP[status];
}

/** Terminal run outcomes that did NOT complete the phase. Each renders as a
 * chip BUTTON opening the diagnosis modal, because "why?" is the only useful
 * next question — the old markup showed a green "Run done" here whenever the
 * process exited 0, which is precisely the claim these outcomes refute. */
type UnresolvedOutcome = 'noop' | 'partial' | 'failed';

export function isUnresolvedOutcome(o: PhaseRunOutcome): o is UnresolvedOutcome {
  return o === 'noop' || o === 'partial' || o === 'failed';
}

/** Chip copy + styling per unresolved outcome. `amber` is the app's semantic
 * needs-a-human color (same token as workspace/TaskCard.tsx's inconclusive). */
const OUTCOME_CHIP: Record<UnresolvedOutcome, { cls: string; title: MessageDescriptor }> = {
  noop: {
    cls: 'border-amber/40 bg-amber/10 text-amber',
    title: msg`the run finished but ticked no acceptance criteria — click for why`,
  },
  partial: {
    cls: 'border-amber/40 bg-amber/10 text-amber',
    title: msg`the run ticked some criteria but did not finish the phase — click for why`,
  },
  failed: {
    cls: 'border-red/40 bg-red/10 text-red',
    title: msg`the run failed — click for why`,
  },
};

/** Run/Retry button styling — keyed on the OUTCOME, so a retry after a
 * ticked-nothing run reads amber like its chip instead of neutral brand. */
export function runButtonCls(outcome: PhaseRunOutcome): string {
  if (outcome === 'failed') return 'border-red/40 text-red hover:bg-red/10';
  if (outcome === 'noop' || outcome === 'partial')
    return 'border-amber/40 text-amber hover:bg-amber/10';
  return 'border-brand/40 text-brand hover:bg-brand/10';
}

function outcomeChipLabel(p: EpicPhase, outcome: UnresolvedOutcome): string {
  if (outcome === 'failed') return t`failed`;
  if (outcome === 'partial') {
    const done = String(p.checkboxesDone);
    const total = String(p.checkboxesTotal);
    return t`ran · ${done}/${total}`;
  }
  return t`ran · no progress`;
}

/** The clickable outcome chip. READ-ONLY — it stays enabled while a plan run
 * owns the docs, because a diagnosis is exactly what a user wants then. */
export function RunOutcomeChip({
  phase,
  outcome,
  onOpen,
}: {
  phase: EpicPhase;
  outcome: UnresolvedOutcome;
  onOpen: () => void;
}): JSX.Element {
  const { i18n } = useLingui();
  const { cls, title } = OUTCOME_CHIP[outcome];
  return (
    <button
      type="button"
      data-tip={i18n._(title)}
      onClick={(e) => {
        e.stopPropagation();
        onOpen();
      }}
      className={`rounded border px-1.5 py-px font-mono text-[9.5px] transition-colors hover:brightness-125 ${cls}`}
    >
      {outcomeChipLabel(phase, outcome)}
    </button>
  );
}

/** The completed-outcome chip, shared by every render site. It describes the
 * LAST RUN, not the present — hence `last run: done` rather than `Run done`,
 * which read as a claim about the phase and collided with a live `11/12`
 * beside it.
 *
 * Detecting that collision without a `runCheckboxesAfter` field (the DTO exposes
 * only `runCheckboxesBefore`): `completed` is derivable ONLY when the run's
 * stamped end count reached the total (internal/phasediag/outcome.go:35), and
 * that stamped count wins over the live one (`OutcomeFromRow`). So a `completed`
 * outcome sitting next to `checkboxesDone < checkboxesTotal` is precisely — and
 * only — the case where the two counts disagree, i.e. the doc changed after the
 * run ended. The measured count is rendered as `total/total`: `after >= total`
 * is exactly what the daemon asserted. */
export function RunCompletedChip({ phase }: { phase: EpicPhase }): JSX.Element {
  const { t } = useLingui();
  const total = String(phase.checkboxesTotal);
  const done = String(phase.checkboxesDone);
  const drifted = phase.checkboxesTotal > 0 && phase.checkboxesDone < phase.checkboxesTotal;
  return (
    <span
      className="rounded border border-green/40 bg-green/10 px-1.5 py-px font-mono text-[9.5px] text-green"
      data-tip={
        drifted
          ? t`the run ticked every criterion (${total}/${total}); the doc has since changed to ${done}/${total}`
          : t`the run ticked every acceptance criterion`
      }
    >
      <Trans>last run: done</Trans>
    </span>
  );
}

/** A thin progress bar. total===0 renders an empty track (no divide-by-zero). */
export function ProgressBar({
  done,
  total,
  className = '',
}: {
  done: number;
  total: number;
  className?: string;
}): JSX.Element {
  const { t } = useLingui();
  const pct = total > 0 ? Math.round((done / total) * 100) : 0;
  return (
    <div
      // Track must contrast with every card fill it sits on — the selected plan
      // card is itself bg-surface2, which made a bg-surface2 track invisible and
      // a partial fill read as a complete bar.
      className={`h-1.5 overflow-hidden rounded-full bg-line-strong/50 ${className}`}
      role="progressbar"
      aria-valuenow={pct}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-label={t`${done} of ${total} checkboxes done`}
    >
      <div className="h-full rounded-full bg-brand transition-[width]" style={{ width: `${String(pct)}%` }} />
    </div>
  );
}

/** ContinuationChip surfaces the run completion loop's decisions (`run_events`,
 * migration 0077) on the phase row.
 *
 * It renders NOTHING for a run that finished on its first turn, which is the
 * overwhelmingly common case — the chip has to earn its space. It appears
 * exactly when the harness had to intervene, because that is the fact `runState`
 * alone cannot carry: a `partial` phase nudged twice that progressed each time
 * and one that stalled on turn one read identically without it, and the
 * difference is what says whether the phase doc or the executor is at fault.
 *
 * The tooltip carries each continuation's message, so the operator can see what
 * the run was actually told rather than inferring it. */
export function ContinuationChip({ events }: { events: RunEvent[] | null }): JSX.Element | null {
  const { t } = useLingui();
  // A daemon older than the [] guard sends null for an empty timeline.
  const continuations = (events ?? []).filter((e) => e.kind === 'continuation');
  if (continuations.length === 0) return null;
  const tip = continuations
    .map((e) => `#${e.attempt}: ${e.detail.split('\n')[0]}`)
    .join(' — ');
  const resumed = String(continuations.length);
  return (
    <span
      className="inline-flex items-center gap-1 rounded border border-amber/40 bg-amber/10 px-1.5 py-px font-mono text-[9.5px] text-amber"
      data-tip={t`the run ended its turn with criteria unticked and was resumed ${resumed}× — ${tip}`}
      onClick={(e) => e.stopPropagation()}
    >
      ↻ {continuations.length}
    </span>
  );
}
