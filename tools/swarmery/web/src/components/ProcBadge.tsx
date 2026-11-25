import type { Session } from '../api/types';
import { ExplainPair } from './Explain';

type BadgeKind = 'orphaned' | 'detached' | 'dead';

/** Derive the visible badge kind from session fields. */
export function procBadgeKind(session: Session): BadgeKind | null {
  if (session.procState === 'dead') return 'dead';
  if (session.procState === 'detached') return 'detached';
  if (session.procState === 'orphaned') return 'orphaned';
  return null;
}

const BADGE_STYLES: Record<BadgeKind, string> = {
  orphaned: 'border-amber-500/40 bg-amber-500/10 text-amber-500',
  detached: 'border-amber-500/40 bg-amber-500/10 text-amber-500',
  dead: 'border-ink-dim/40 bg-ink-dim/10 text-ink-dim',
};

/** Renders an orphaned / dead badge, or null when the session is clean. */
export function ProcBadge({ session }: { session: Session }): JSX.Element | null {
  const kind = procBadgeKind(session);
  if (!kind) return null;
  // The explainer lives inside the component, not at its call sites: SessionCard
  // renders ProcBadge from both of its layouts (the stacked card and the desktop
  // grid are both mounted, CSS shows one), so one edit covers both and they
  // cannot drift — and only one is ever *visible*, so there is never a duplicate
  // popover on screen.
  //
  // ExplainPair keeps the trigger bound to its badge rather than adrift in the
  // host row's own gap: the stacked card puts this straight after ContextBadge,
  // the desktop grid puts it between TaskChip and the Stop/Kill control.
  return (
    <ExplainPair id="proc-badge">
      <span
        className={`inline-flex items-center rounded border px-1.5 py-px font-mono text-[10px] font-medium ${BADGE_STYLES[kind]}`}
      >
        {kind}
      </span>
    </ExplainPair>
  );
}
