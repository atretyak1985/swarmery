// Shared world for swarmery-link's tests. The test kit has no file system or
// network beneath the plugin, so the token file and the daemon are answered
// here. Not imported by register.ts, so it never loads in a session.

import type { On } from 'claude-code'
import { type MockClock, mock } from 'claude-code/testing'

export const HOME = '/home/dev'
export const TOKEN_PATH = `${HOME}/.swarmery/link-token`
export const TOKEN = 'a'.repeat(64)
export const SESSION_ID = 'efaa9b56-306f-48b7-bba8-51c5ffa08f79'
export const CWD = '/work/apps/main-app'
export const START = { cwd: CWD, surface: null, isInteractive: false } as const

/** `$.session.usage()` after turn 1 (testdata/fixtures/mods/session-usage.json, M2). */
export const USAGE_AFTER_TURN_1 = {
  startedAt: 1791920751978,
  context: { tokens: 32108, window: 1000000, percent: 3 },
  rateLimits: [
    { kind: 'five_hour', percentUsed: 9, resetsAt: '2026-10-13T20:20:00.000Z' },
    { kind: 'seven_day', percentUsed: 48, resetsAt: '2026-10-17T14:00:00.000Z' },
  ],
  cost: { usd: 0.0043368899999999995 },
}

export type Request = {
  readonly url: string
  readonly method: string | undefined
  readonly headers: Readonly<Record<string, string>>
  readonly body: { readonly events: ReadonlyArray<Readonly<Record<string, unknown>>> }
}

export type World = {
  /** The token file's text per read, in order; the last repeats. Absent: no file. */
  readonly tokens?: readonly string[]
  /**
   * The daemon's answer per request, in order; the last repeats. A status is a
   * response, `throw` a connection that fails, `hang` a daemon that accepts
   * the connection and never answers. Default: 202.
   */
  readonly replies?: ReadonlyArray<number | 'throw' | 'hang'>
  readonly now?: number
}

export type Recorded = {
  readonly clock: MockClock
  readonly requests: Request[]
  readonly logs: string[]
  /** How many times the token file was read. */
  readonly tokenReads: () => number
  /** What reached the engine's own tool.check beneath the plugin. */
  readonly checks: unknown[]
}

const pick = <T>(list: readonly T[] | undefined, i: number, fallback: T): T =>
  list === undefined || list.length === 0 ? fallback : (list[Math.min(i, list.length - 1)] ?? fallback)

/**
 * Answers everything swarmery-link reads through `$` and the engine's bottom
 * of each event it hooks, and records the requests and log lines. Call before
 * the test's first `$` call.
 */
export const world = (on: On, w: World = {}): Recorded => {
  const requests: Request[] = []
  const logs: string[] = []
  const checks: unknown[] = []
  let reads = 0
  const clock = mock.clock(on, { now: w.now ?? Date.parse('2026-10-13T10:00:00.000Z') })
  mock.env(on, { HOME })

  on('session.start', ($, e) => ({ cwd: e.cwd }))
  on('session.id', () => ({ value: SESSION_ID }))
  on('session.cwd', () => ({ value: CWD }))
  on('session.usage', () => ({ value: USAGE_AFTER_TURN_1 }))
  on('fs.read', ($, e) => {
    if (e.path !== TOKEN_PATH) return { deny: `ENOENT: ${e.path}` }
    const text = w.tokens === undefined ? undefined : pick(w.tokens, reads, undefined)
    reads += 1
    return text === undefined ? { deny: `ENOENT: ${e.path}` } : { value: text }
  })
  on('http.fetch', ($, e) => {
    const reply = pick(w.replies, requests.length, 202)
    requests.push({
      url: e.url,
      method: e.init?.method,
      headers: e.init?.headers ?? {},
      body: JSON.parse(e.init?.body ?? '{"events":[]}'),
    })
    if (reply === 'hang') return new Promise<never>(() => undefined)
    if (reply === 'throw') return { deny: 'connect ECONNREFUSED 127.0.0.1:7777' }
    return { value: { status: reply, ok: reply >= 200 && reply < 300, headers: {}, text: '{}' } }
  })
  on('ui.log', ($, e) => {
    logs.push(e.text)
    return { value: undefined }
  })
  on('turn.start', ($, e) => ({ turnId: e.turnId }))
  on('turn.complete', ($, e) => ({ text: e.answer }))
  on('session.measure', ($, e) => ({ changed: e.changed }))
  on('session.end', ($, e) => ({ sessionId: e.sessionId }))
  on('tool.call', () => ({ result: 'ran' }))
  on('tool.check', ($, e) => {
    checks.push(e)
    return { decision: 'allow' }
  })

  return { clock, requests, logs, tokenReads: () => reads, checks }
}

/** Every event the recorded requests carried, in order. */
export const sent = (requests: readonly Request[]): ReadonlyArray<Readonly<Record<string, unknown>>> =>
  requests.flatMap(r => r.body.events)

/** Watches a hook call without awaiting it, so a test can see it is still pending. */
export const track = (pending: Promise<unknown>): { readonly isDone: () => boolean } => {
  let isDone = false
  void pending.then(
    () => {
      isDone = true
    },
    () => {
      isDone = true
    },
  )
  return { isDone: () => isDone }
}

export const END = { reason: 'other', sessionId: SESSION_ID, resume: { sessionId: SESSION_ID } as never } as const

export const TURN = { text: 'say hi', turnId: 'turn-1' } as const
export const COMPLETE = {
  answer: 'hi',
  durationMs: 1200,
  isAborted: false,
  turnId: 'turn-1',
  reason: 'answer',
} as const
