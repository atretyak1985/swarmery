// The outgoing event queue. Pure: every function takes the queue's state and
// returns the next one. The wire shape and limits are the link protocol's
// (tools/swarmery/docs/mods-link-protocol.md, POST /api/link/events).

export type LinkKind =
  | 'session.start'
  | 'turn.start'
  | 'tool.call'
  | 'session.measure'
  | 'turn.complete'
  | 'session.end'
  | 'turn.forecast'
  | 'turn.actual'
  | 'session.send'
  | 'session.receive'
  | 'claim'
  | 'release'
  | 'conflict'

export type LinkEvent = {
  readonly sessionId: string
  readonly cwd: string
  readonly kind: LinkKind
  /** RFC 3339, ms, UTC. Never rewritten once stamped: a resend carries the same `at`. */
  readonly at: string
  readonly payload?: Readonly<Record<string, unknown>>
}

/** Events a batch may carry, and the most the queue holds while the daemon is away. */
export const MAX_EVENTS = 200
/** The daemon's body limit is 1 MiB; a batch stays under it with room for the envelope. */
export const MAX_BODY_BYTES = 1_000_000

/** The last `at` (epoch ms) stamped per kind. */
export type Stamps = Readonly<Partial<Record<LinkKind, number>>>

/**
 * The event's time: `now`, or one millisecond past the last event of the same
 * kind when `now` does not move past it. The daemon's dedup key is
 * (session, kind, at), so two distinct events of a kind must never share an `at`.
 */
export const stamp = (stamps: Stamps, kind: LinkKind, now: number): { readonly at: string; readonly stamps: Stamps } => {
  const last = stamps[kind]
  const ms = last !== undefined && now <= last ? last + 1 : now
  return { at: new Date(ms).toISOString(), stamps: { ...stamps, [kind]: ms } }
}

const key = (e: LinkEvent): string => `${e.kind}\u0000${e.at}`

/**
 * The queue with `event` appended: an event already queued under the same
 * (kind, at) is not queued twice, and past `cap` the oldest events are dropped.
 */
export const enqueue = (queue: readonly LinkEvent[], event: LinkEvent, cap: number = MAX_EVENTS): readonly LinkEvent[] => {
  if (queue.some(q => key(q) === key(event))) return queue
  const next = [...queue, event]
  return next.length > cap ? next.slice(next.length - cap) : next
}

/** The queue without the events of `sent` (a batch the daemon answered), matched by (kind, at). */
export const drop = (queue: readonly LinkEvent[], sent: readonly LinkEvent[]): readonly LinkEvent[] => {
  const gone = new Set(sent.map(key))
  return queue.filter(e => !gone.has(key(e)))
}

const bytes = (text: string): number => new TextEncoder().encode(text).length

/**
 * The next batch, oldest first: at most `MAX_EVENTS` events and a body under
 * `MAX_BODY_BYTES`. An event too large to send alone is skipped (it can never
 * be accepted) and returned in `oversized` so the caller can drop it.
 */
export const takeBatch = (
  queue: readonly LinkEvent[],
): { readonly batch: readonly LinkEvent[]; readonly body: string; readonly oversized: readonly LinkEvent[] } => {
  const batch: LinkEvent[] = []
  const oversized: LinkEvent[] = []
  const envelope = bytes('{"events":[]}')
  let size = envelope
  for (const event of queue) {
    if (batch.length >= MAX_EVENTS) break
    const one = bytes(JSON.stringify(event))
    if (envelope + one > MAX_BODY_BYTES) {
      oversized.push(event)
      continue
    }
    const added = one + (batch.length > 0 ? 1 : 0)
    if (size + added > MAX_BODY_BYTES) break
    batch.push(event)
    size += added
  }
  return { batch, body: JSON.stringify({ events: batch }), oversized }
}
