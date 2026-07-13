import type { Session } from '../api/types';

type BadgeKind = 'orphaned' | 'stale' | 'dead';

/** Derive the visible badge kind from session fields. stale is client-only. */
export function procBadgeKind(session: Session): BadgeKind | null {
  if (session.procState === 'dead') return 'dead';
  if (session.procState === 'orphaned') return 'orphaned';
  if (session.status === 'idle' && session.procState === 'running') return 'stale';
  return null;
}

const BADGE_STYLES: Record<BadgeKind, string> = {
  orphaned: 'border-amber-500/40 bg-amber-500/10 text-amber-500',
  stale: 'border-yellow-500/40 bg-yellow-500/10 text-yellow-500',
  dead: 'border-ink-dim/40 bg-ink-dim/10 text-ink-dim',
};

/** Renders an orphaned / stale / dead badge, or null when the session is clean. */
export function ProcBadge({ session }: { session: Session }): JSX.Element | null {
  const kind = procBadgeKind(session);
  if (!kind) return null;
  return (
    <span
      className={`inline-flex items-center rounded border px-1.5 py-px font-mono text-[10px] font-medium ${BADGE_STYLES[kind]}`}
    >
      {kind}
    </span>
  );
}
