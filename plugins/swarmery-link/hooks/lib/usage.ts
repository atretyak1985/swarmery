// The session.measure payload. Pure: normalizes what `session.measure`'s `e`
// or `$.session.usage()` answered into the M2 shape the daemon decodes
// (mods-format.md M2; testdata/fixtures/mods/session-usage.json).
//
// A unit the engine did not report is left out, never zeroed: the daemon keeps
// a column's last value for an absent unit ("not reported" is not "zero").

export type UsageUnit = 'context' | 'rateLimits' | 'cost'

export type RateLimit = {
  readonly kind: string
  readonly percentUsed: number
  readonly resetsAt?: string
}

export type MeasurePayload = {
  readonly context?: { readonly tokens?: number; readonly window: number; readonly percent?: number }
  readonly rateLimits?: readonly RateLimit[]
  readonly cost?: { readonly usd: number }
  readonly changed: readonly UsageUnit[]
}

const UNITS: readonly UsageUnit[] = ['context', 'rateLimits', 'cost']

const isRecord = (v: unknown): v is Readonly<Record<string, unknown>> =>
  typeof v === 'object' && v !== null && !Array.isArray(v)

const num = (v: unknown): number | undefined => (typeof v === 'number' && Number.isFinite(v) ? v : undefined)

const context = (v: unknown): MeasurePayload['context'] => {
  if (!isRecord(v)) return undefined
  const window = num(v.window)
  if (window === undefined) return undefined
  const tokens = num(v.tokens)
  const percent = num(v.percent)
  return {
    ...(tokens !== undefined ? { tokens } : {}),
    window,
    ...(percent !== undefined ? { percent } : {}),
  }
}

const rateLimit = (v: unknown): RateLimit | undefined => {
  if (!isRecord(v) || typeof v.kind !== 'string') return undefined
  const percentUsed = num(v.percentUsed)
  if (percentUsed === undefined) return undefined
  return typeof v.resetsAt === 'string'
    ? { kind: v.kind, percentUsed, resetsAt: v.resetsAt }
    : { kind: v.kind, percentUsed }
}

const rateLimits = (v: unknown): readonly RateLimit[] | undefined => {
  if (!Array.isArray(v)) return undefined
  const out = v.map(rateLimit).filter((r): r is RateLimit => r !== undefined)
  return out.length > 0 ? out : undefined
}

const cost = (v: unknown): MeasurePayload['cost'] => {
  if (!isRecord(v)) return undefined
  const usd = num(v.usd)
  return usd === undefined ? undefined : { usd }
}

const changed = (v: unknown, present: readonly UsageUnit[]): readonly UsageUnit[] => {
  if (!Array.isArray(v)) return present
  const named = UNITS.filter(u => v.includes(u))
  return named.length > 0 ? named : present
}

/**
 * The payload for one measurement. `raw` is `session.measure`'s `e` (which
 * names what `changed`) or `$.session.usage()` (which does not: a snapshot
 * names every unit it carries). Undefined when nothing usable was reported.
 */
export const measurePayload = (raw: unknown): MeasurePayload | undefined => {
  if (!isRecord(raw)) return undefined
  const ctx = context(raw.context)
  const limits = rateLimits(raw.rateLimits)
  const usd = cost(raw.cost)
  const present = UNITS.filter(
    u => (u === 'context' && ctx !== undefined) || (u === 'rateLimits' && limits !== undefined) || (u === 'cost' && usd !== undefined),
  )
  if (present.length === 0) return undefined
  return {
    ...(ctx !== undefined ? { context: ctx } : {}),
    ...(limits !== undefined ? { rateLimits: limits } : {}),
    ...(usd !== undefined ? { cost: usd } : {}),
    changed: changed(raw.changed, present),
  }
}
