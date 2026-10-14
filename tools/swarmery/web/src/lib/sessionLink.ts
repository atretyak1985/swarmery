// Pure model behind the link-mod chips (components/SessionLinkChips.tsx) and
// the Insights cost-source panel (pages/health/ModCostPanel.tsx): which rate
// limit to headline and when the mod's cost and the pricing table disagree.
// No rendering and no copy here — both callers word it themselves.

import type { SessionLinkRateLimit } from '../api/types';

/** Relative gap between the mod's cost and the pricing-table cost above which
 * the UI flags the pair (phase doc: "розбіжність > 10 %"). It is the input for
 * calibrating config/pricing.json, not an error. */
export const COST_DIVERGENCE_THRESHOLD = 0.1;

/** The rate limit nearest to exhaustion — highest percentUsed; the first one
 * wins a tie. Null when the mod reported none (off a subscription). */
export function topRateLimit(limits: readonly SessionLinkRateLimit[]): SessionLinkRateLimit | null {
  return limits.reduce<SessionLinkRateLimit | null>(
    (best, r) => (best === null || r.percentUsed > best.percentUsed ? r : best),
    null,
  );
}

/**
 * Signed relative gap of the mod's cost against the pricing table:
 * (mod − pricing) / pricing. Null when either side is missing or the pricing
 * side is not positive — there is nothing to compare a ratio against.
 */
export function costGap(modUsd: number | null | undefined, pricingUsd: number | null | undefined): number | null {
  if (modUsd == null || pricingUsd == null || pricingUsd <= 0) return null;
  return (modUsd - pricingUsd) / pricingUsd;
}

/** True when the two costs differ by more than COST_DIVERGENCE_THRESHOLD. */
export function costDiverges(modUsd: number | null | undefined, pricingUsd: number | null | undefined): boolean {
  const gap = costGap(modUsd, pricingUsd);
  return gap !== null && Math.abs(gap) > COST_DIVERGENCE_THRESHOLD;
}

