/** Surprise index → the UI's words (Canvas v3: "off-plan"). Bands mirror surpriseCls in Plans. */
export type OffPlanBand = 'on plan' | 'off plan' | 'far off plan';
export const OFF_PLAN_AT = 0.3;
export const FAR_OFF_PLAN_AT = 0.6;
export function offPlanBand(index: number): OffPlanBand {
  // The band is a code, not copy: callers key Record<OffPlanBand, …> maps and
  // compare against it; the words are translated where a band is rendered.
  if (index >= FAR_OFF_PLAN_AT) return 'far off plan'; // i18n-ignore — OffPlanBand code
  if (index >= OFF_PLAN_AT) return 'off plan'; // i18n-ignore — OffPlanBand code
  return 'on plan';
}
