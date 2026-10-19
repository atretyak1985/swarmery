// The swarm option's `$` calls: the claims registry in `$.store` and the
// dashboard's `message` command through `$.session.send`. Each is a top-level
// function declaration handed `$` by register.ts, which owns the link and its
// queue; the decisions are pure in ./lib/swarm.ts. Nothing here is called
// with `swarm` off.
//
// The registry is advisory unless `claimConflicts` is set: then register.ts's
// tool.check reads it (heldElsewhere) to ask or refuse a write another live
// session holds. This session writes only its own key,
// `swarmery-link/claims/<sessionId>`, whole with one `$.store.set`, holding
// paths and ms timestamps alone (never text or tool input); other sessions'
// keys are read, never written.

import type { EngineInterface, SessionSendResult } from 'claude-code'
import {
  type ClaimEntry,
  type Conflict,
  type SwarmTurn,
  addClaim,
  claimsKey,
  conflictsFor,
  foreignKeys,
  noteClaim,
  parseEntry,
} from './lib/swarm'

export type ClaimOutcome = {
  readonly turn: SwarmTurn
  /** The first claim of the path this turn: a `claim` event goes out. */
  readonly isNew: boolean
  /** Other sessions' live claims on the path, found by this claim. */
  readonly conflicts: readonly Conflict[]
  /** `$.store` refused; the claim event still goes out, the registry missed it. */
  readonly storeError?: unknown
}

/** The other sessions' claims entries, as the store holds them (stale ones included). */
async function readOthers($: EngineInterface, sessionId: string): Promise<readonly ClaimEntry[]> {
  const others: ClaimEntry[] = []
  for (const key of foreignKeys(await $.store.keys(), sessionId)) {
    const other = parseEntry(await $.store.get(key))
    if (other !== undefined) others.push(other)
  }
  return others
}

/**
 * Claims `path` for this session: the first claim of it this turn writes the
 * session's entry, then reads the other sessions' entries for a live claim on
 * the same path. A repeat claim touches nothing.
 */
export async function claimFile($: EngineInterface, turn: SwarmTurn, sessionId: string, cwd: string, path: string): Promise<ClaimOutcome> {
  const noted = noteClaim(turn, path)
  if (!noted.isNew) return { turn, isNew: false, conflicts: [] }
  try {
    const now = await $.clock.now()
    const entry = addClaim(noted.turn.entry, sessionId, cwd, path, now)
    await $.store.set(claimsKey(sessionId), entry)
    const written: SwarmTurn = { ...noted.turn, entry }
    const conflicts = conflictsFor(path, sessionId, await readOthers($, sessionId), now)
    return { turn: { ...written, conflicts: [...written.conflicts, ...conflicts] }, isNew: true, conflicts }
  } catch (storeError) {
    return { turn: noted.turn, isNew: true, conflicts: [], storeError }
  }
}

/**
 * The other live sessions holding `path` right now, read without writing
 * anything (the `claimConflicts` check, ahead of the call). Rejects when the
 * store refuses; the caller then leaves the verdict beneath.
 */
export async function heldElsewhere($: EngineInterface, sessionId: string, path: string): Promise<readonly Conflict[]> {
  const now = await $.clock.now()
  return conflictsFor(path, sessionId, await readOthers($, sessionId), now)
}

/** Removes this session's claims entry (a turn's end, the session's end). */
export async function releaseClaims($: EngineInterface, sessionId: string): Promise<void> {
  await $.store.delete(claimsKey(sessionId))
}

/** Sends `text` to another of the person's sessions, by the id its `$.session.id()` answers. */
export async function sendToSession($: EngineInterface, sessionId: string, text: string): Promise<SessionSendResult> {
  return $.session.send({ to: { sessionId }, text })
}
