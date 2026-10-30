// The swarmery link mod's live usage on a session, on the session's own detail.
// List rows carry no chips (handoff §4), so this is the one renderer: the wording,
// the thresholds and the source marker live here.
//
// What it draws, when the session carries `link`:
//   ● source dot  — green "live from session mod" while link.live (the server's
//                   2×AwaitAfter freshness rule, the one that lets the mod's
//                   status drive Session.status), dim "derived from transcript"
//                   once the mod went quiet;
//   ctx 78 %      — context.percent;
//   plan 64 % · resets 18:00 — the rate limit nearest exhaustion, every limit
//                   in the tooltip;
//   $1.25         — the mod's per-process cost, the pricing-table recomputation
//                   in the tooltip; amber with a visible Δ when the two differ
//                   by more than 10 %.
// `link == null` renders nothing at all: a session without the mod looks
// exactly as it did before.

import { useLingui } from '@lingui/react/macro';
import type { Session, SessionLinkRateLimit } from '../api/types';
import { fmtCost, fmtDateTime, fmtTime } from '../lib/format';
import { costDiverges, costGap, topRateLimit } from '../lib/sessionLink';

const CHIP =
  'shrink-0 rounded-full border px-[7px] py-0.5 font-mono text-[10px] whitespace-nowrap';
const NEUTRAL = 'border-line-strong bg-surface2 text-ink-dim';
const WARN = 'border-amber/40 bg-amber/10 text-amber';

/** Context and rate-limit chips turn amber this close to the ceiling. */
const USAGE_WARN_PCT = 80;

/** 64 → "64", 12.5 → "12.5" — the mod reports at most one decimal. */
function pct(n: number): string {
  return String(Math.round(n * 10) / 10);
}

/** Same local day → "18:00"; later → "Oct 16, 18:00" (seven-day windows). */
function fmtReset(iso: string): string {
  const at = new Date(iso);
  if (Number.isNaN(at.getTime())) return '—';
  return at.toDateString() === new Date().toDateString() ? fmtTime(at) : fmtDateTime(at);
}

/** The status/cost source marker. Exported for the Insights panel's legend. */
export function LinkSourceDot({ live }: { live: boolean }): JSX.Element {
  const { t } = useLingui();
  const label = live ? t`live from session mod` : t`derived from transcript`;
  return (
    <span
      role="img"
      aria-label={label}
      data-tip={label}
      data-testid="link-source"
      className={`shrink-0 font-mono text-[10px] leading-none ${live ? 'text-green' : 'text-ink-faint'}`}
    >
      ●
    </span>
  );
}

function ContextChip({ percent }: { percent: number }): JSX.Element {
  const { t } = useLingui();
  const ctx = pct(percent);
  return (
    <span
      data-testid="link-context"
      data-tip={t`context window ${ctx} % full, reported by the session mod`}
      className={`${CHIP} ${percent >= USAGE_WARN_PCT ? WARN : NEUTRAL}`}
    >
      {t`ctx ${ctx} %`}
    </span>
  );
}

function RateLimitChip({ limits }: { limits: readonly SessionLinkRateLimit[] }): JSX.Element | null {
  const { t } = useLingui();
  const top = topRateLimit(limits);
  if (top === null) return null;
  const kindLabel = (kind: string): string =>
    kind === 'five_hour' ? t`5-hour window` : kind === 'seven_day' ? t`7-day window` : kind;
  const line = (r: SessionLinkRateLimit): string => {
    const name = kindLabel(r.kind);
    const used = pct(r.percentUsed);
    if (r.resetsAt === null) return t`${name} ${used} % used`;
    const at = fmtReset(r.resetsAt);
    return t`${name} ${used} % used, resets ${at}`;
  };
  const used = pct(top.percentUsed);
  const reset = top.resetsAt === null ? null : fmtReset(top.resetsAt);
  return (
    <span
      data-testid="link-rate-limit"
      data-tip={limits.map(line).join(' · ')}
      className={`${CHIP} ${top.percentUsed >= USAGE_WARN_PCT ? WARN : NEUTRAL}`}
    >
      {reset === null ? t`plan ${used} %` : t`plan ${used} % · resets ${reset}`}
    </span>
  );
}

function CostChip({ modUsd, pricingUsd }: { modUsd: number; pricingUsd: number | null }): JSX.Element {
  const { t } = useLingui();
  const mod = fmtCost(modUsd);
  const pricing = fmtCost(pricingUsd);
  const gap = costGap(modUsd, pricingUsd);
  const diverges = costDiverges(modUsd, pricingUsd);
  const gapPct = gap === null ? '' : `${gap > 0 ? '+' : ''}${String(Math.round(gap * 100))}`;
  const tip = t`${mod} reported by Claude Code for this process (a resumed session restarts it from 0) · pricing table: ${pricing} for the whole session`;
  return (
    <span data-testid="link-cost" data-tip={tip} className={`${CHIP} ${diverges ? WARN : NEUTRAL}`}>
      {mod}
      {diverges && (
        <>
          {' '}
          <span aria-hidden="true">Δ {gapPct} %</span>
          <span className="sr-only">{t`differs from the pricing table by ${gapPct} %`}</span>
        </>
      )}
    </span>
  );
}

/**
 * The link chips of one session, or null when it carries no `link`.
 * `className` styles the wrapper so each host can place it in its own row.
 */
export function SessionLinkChips({
  session,
  className = '',
}: {
  session: Pick<Session, 'link' | 'costUsd'>;
  className?: string;
}): JSX.Element | null {
  const link = session.link;
  if (link == null) return null;
  return (
    <span data-testid="session-link" className={`inline-flex items-center gap-1.5 ${className}`}>
      <LinkSourceDot live={link.live} />
      {link.contextPct !== null && <ContextChip percent={link.contextPct} />}
      <RateLimitChip limits={link.rateLimits} />
      {link.costUsd !== null && <CostChip modUsd={link.costUsd} pricingUsd={session.costUsd ?? null} />}
    </span>
  );
}
