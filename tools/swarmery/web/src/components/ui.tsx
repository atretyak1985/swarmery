// Small design-system primitives shared across screens (Bloombum editorial
// language: mono eyebrows, soft-tint status badges, white hairline cards).

import type { ReactNode } from 'react';
import type { SessionStatus } from '../api/types';

/* ----- section heading — the DS ".bb-eyebrow" mono label ----- */

export function SectionTitle({ children }: { children: ReactNode }): JSX.Element {
  return (
    <h2 className="mt-[26px] mb-2.5 font-mono text-[11px] font-medium tracking-[0.14em] text-ink-dim uppercase first:mt-1">
      {children}
    </h2>
  );
}

/* ----- status chip — DS ".bb-badge": soft tint fill + status ink ----- */

const CHIP_STYLES: Record<SessionStatus, string> = {
  active: 'bg-green-soft text-green',
  waiting_approval: 'bg-amber-soft text-amber',
  idle: 'bg-surface2 text-ink-dim',
  completed: 'bg-surface2 text-ink-dim',
  killed: 'bg-red-soft text-red',
};

const CHIP_LABELS: Record<SessionStatus, string> = {
  active: 'active',
  waiting_approval: 'waiting',
  idle: 'idle',
  completed: 'done',
  killed: 'killed',
};

export function StatusChip({
  status,
  suffix,
}: {
  status: SessionStatus;
  suffix?: string;
}): JSX.Element {
  return (
    <span
      className={`rounded-md px-2 py-0.5 font-mono text-[10.5px] font-medium whitespace-nowrap ${CHIP_STYLES[status]}`}
    >
      {CHIP_LABELS[status]}
      {suffix !== undefined ? ` · ${suffix}` : ''}
    </span>
  );
}

/* ----- live dot ----- */

export function LiveDot({ status }: { status: SessionStatus }): JSX.Element | null {
  if (status === 'active') {
    return <span className="inline-block h-[7px] w-[7px] shrink-0 animate-pulse-dot rounded-full bg-green" />;
  }
  if (status === 'waiting_approval') {
    return <span className="inline-block h-[7px] w-[7px] shrink-0 rounded-full bg-amber" />;
  }
  if (status === 'idle') {
    return <span className="inline-block h-[7px] w-[7px] shrink-0 rounded-full bg-ink-dim/50" />;
  }
  return null;
}

/* ----- card shell — white on cream, 14px "radius-frame", hairline ----- */

export function Card({
  children,
  className = '',
}: {
  children: ReactNode;
  className?: string;
}): JSX.Element {
  return (
    <div className={`mb-2.5 rounded-[14px] border border-line bg-surface px-3.5 py-3 ${className}`}>
      {children}
    </div>
  );
}

/* ----- async states ----- */

export function Loading({ label = 'loading…' }: { label?: string }): JSX.Element {
  return (
    <div className="flex items-center gap-2.5 py-10 text-ink-dim justify-center" role="status">
      <span className="h-3.5 w-3.5 animate-spin rounded-full border-2 border-line border-t-brand" />
      <span className="font-mono text-[12px]">{label}</span>
    </div>
  );
}

export function ErrorBox({
  message,
  onRetry,
}: {
  message: string;
  onRetry?: () => void;
}): JSX.Element {
  return (
    <div className="my-3 rounded-lg border border-red/30 bg-red-soft/50 px-3.5 py-3" role="alert">
      <div className="font-mono text-[12px] text-red">{message}</div>
      {onRetry !== undefined && (
        <button
          type="button"
          onClick={onRetry}
          className="mt-2 rounded-lg border border-line bg-surface px-3 py-1.5 text-[12px] font-semibold text-ink-2 transition-colors hover:bg-surface2"
        >
          retry
        </button>
      )}
    </div>
  );
}

export function Empty({ children }: { children: ReactNode }): JSX.Element {
  return (
    <div className="my-3 rounded-[14px] border border-dashed border-line px-3.5 py-6 text-center text-[12.5px] text-ink-dim">
      {children}
    </div>
  );
}
