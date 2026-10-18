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
  /**
   * The daemon's answer per GET /api/link/commands, in order; the last repeats.
   * A command is a 200 carrying it. Default: `hang` (a poll held open forever).
   */
  readonly polls?: ReadonlyArray<PollReply>
  /** The status per POST /api/link/commands/{id}/ack, in order; the last repeats. Default: 204. */
  readonly ackReplies?: ReadonlyArray<number | 'throw'>
  /**
   * The engine beneath answers `$.prompt.submit` with `{ drop: submitDrop }`.
   * (A test hook that throws is skipped by the kit, so a rejection cannot be
   * staged here; a drop carries the same reason.) Absent: the prompt enters.
   */
  readonly submitDrop?: string
  /**
   * The model's answer per `$.model.complete`, in order; the last repeats. A
   * string is an answered reply with that text, `hang` a call that never
   * settles, a `reason` an unanswered one, `{ text, afterMs }` an answer
   * that comes once the mock clock moved that far. Default: FORECAST_REPLY.
   */
  readonly models?: ReadonlyArray<ModelReply>
  /**
   * How the tool beneath ends each `tool.call`, in order; the last repeats.
   * `error` is a tool that reported an error (a red test run), `deny` a call
   * refused beneath. Default: `ok`, answered `{ result: 'ran' }`.
   */
  readonly tools?: ReadonlyArray<'ok' | 'error' | 'deny'>
  readonly now?: number
}

export type ModelReply =
  | string
  | 'hang'
  | { readonly reason: 'api-error' | 'empty-reply' | 'aborted' }
  | { readonly text: string; readonly afterMs: number }

/** A model reply that parses: one file, tests pass, 4 steps, 0.7 confidence. */
export const FORECAST_REPLY = '{"files":["src/a.ts"],"testsPass":true,"steps":4,"confidence":0.7}'

/** The usage every staged model reply reports. */
export const MODEL_USAGE = { input_tokens: 180, output_tokens: 40, cache_read_input_tokens: 0, cache_creation_input_tokens: 0 } as const

export type ModelCall = {
  readonly model: string
  readonly prompt: string
  readonly system: string | undefined
  readonly maxTokens: number | undefined
}

export type WireCommand = { readonly id: number; readonly kind: string; readonly payload: Readonly<Record<string, unknown>> }

export type PollReply = number | 'throw' | 'hang' | { readonly command: WireCommand }

export type Poll = { readonly url: string; readonly method: string | undefined; readonly authorization: string | undefined; readonly at: number }

export type AckSent = { readonly url: string; readonly body: { readonly result: string; readonly message?: string } }

export type Submitted = { readonly text: string; readonly asUser: boolean }

export type Recorded = {
  readonly clock: MockClock
  readonly requests: Request[]
  readonly logs: string[]
  /** The log lines meant for the transcript (not `to: 'debug'`). */
  readonly shown: string[]
  /** How many times the token file was read. */
  readonly tokenReads: () => number
  /** What reached the engine's own tool.check beneath the plugin. */
  readonly checks: unknown[]
  /** Every GET to the command endpoint, with the mock clock's time it was made. */
  readonly polls: Poll[]
  readonly acks: AckSent[]
  /** The turnId of every `$.turn.abort`. */
  readonly aborts: string[]
  readonly submits: Submitted[]
  /** What `$.session.id()` and `$.session.cwd()` answer from now on, as after a `/clear`. */
  readonly moveTo: (sessionId: string, cwd?: string) => void
  /** Every `$.model.complete` that reached the engine beneath the plugin. */
  readonly modelCalls: ModelCall[]
}

const COMMANDS = '/api/link/commands'

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
  const shown: string[] = []
  const checks: unknown[] = []
  const polls: Poll[] = []
  const acks: AckSent[] = []
  const aborts: string[] = []
  const submits: Submitted[] = []
  const modelCalls: ModelCall[] = []
  let toolCalls = 0
  let reads = 0
  let sessionId: string = SESSION_ID
  let cwd: string = CWD
  const clock = mock.clock(on, { now: w.now ?? Date.parse('2026-10-13T10:00:00.000Z') })
  mock.env(on, { HOME })

  on('session.start', ($, e) => ({ cwd: e.cwd }))
  on('session.id', () => ({ value: sessionId }))
  on('session.cwd', () => ({ value: cwd }))
  on('session.usage', () => ({ value: USAGE_AFTER_TURN_1 }))
  on('fs.read', ($, e) => {
    if (e.path !== TOKEN_PATH) return { deny: `ENOENT: ${e.path}` }
    const text = w.tokens === undefined ? undefined : pick(w.tokens, reads, undefined)
    reads += 1
    return text === undefined ? { deny: `ENOENT: ${e.path}` } : { value: text }
  })
  on('http.fetch', ($, e) => {
    if (e.url.includes(`${COMMANDS}/`)) {
      const status = pick(w.ackReplies, acks.length, 204)
      acks.push({ url: e.url, body: JSON.parse(e.init?.body ?? '{}') })
      if (status === 'throw') return { deny: 'connect ECONNREFUSED 127.0.0.1:7777' }
      return { value: { status, ok: status >= 200 && status < 300, headers: {}, text: '' } }
    }
    if (e.url.includes(`${COMMANDS}?`)) {
      const reply = pick<PollReply>(w.polls, polls.length, 'hang')
      polls.push({ url: e.url, method: e.init?.method, authorization: e.init?.headers?.authorization, at: clock.now() })
      if (reply === 'hang') return new Promise<never>(() => undefined)
      if (reply === 'throw') return { deny: 'connect ECONNREFUSED 127.0.0.1:7777' }
      if (typeof reply === 'number') return { value: { status: reply, ok: reply >= 200 && reply < 300, headers: {}, text: '' } }
      return { value: { status: 200, ok: true, headers: {}, text: JSON.stringify(reply.command) } }
    }
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
    if (e.to !== 'debug') shown.push(e.text)
    return { value: undefined }
  })
  on('turn.start', ($, e) => ({ turnId: e.turnId }))
  on('turn.complete', ($, e) => ({ text: e.answer }))
  on('session.measure', ($, e) => ({ changed: e.changed }))
  on('session.end', ($, e) => ({ sessionId: e.sessionId }))
  on('tool.call', () => {
    const outcome = pick(w.tools, toolCalls, 'ok')
    toolCalls += 1
    if (outcome === 'deny') return { deny: 'refused beneath' }
    if (outcome === 'error') return { isError: true, result: 'Exit code 1', text: 'Exit code 1' }
    return { result: 'ran' }
  })
  on('model.complete', ($, e) => {
    const reply = pick<ModelReply>(w.models, modelCalls.length, FORECAST_REPLY)
    modelCalls.push({ model: e.model, prompt: e.prompt, system: e.system, maxTokens: e.maxTokens })
    if (reply === 'hang') return new Promise<never>(() => undefined)
    if (typeof reply === 'string') return { value: { isAnswered: true, text: reply, usage: MODEL_USAGE } }
    if ('text' in reply) {
      const answer = { value: { isAnswered: true, text: reply.text, usage: MODEL_USAGE } } as const
      return clock.sleep(reply.afterMs).then(() => answer)
    }
    if (reply.reason === 'api-error') {
      return { value: { isAnswered: false, reason: 'api-error', status: 529, error: 'overloaded', usage: MODEL_USAGE } }
    }
    return { value: { isAnswered: false, reason: reply.reason, usage: MODEL_USAGE } }
  })
  on('tool.check', ($, e) => {
    checks.push(e)
    return { decision: 'allow' }
  })

  on('turn.abort', ($, e) => {
    aborts.push(e.turnId)
    return { value: undefined }
  })
  on('prompt.submit', ($, e) => {
    const origin = e.origin as { readonly asUser?: boolean }
    submits.push({ text: e.text, asUser: origin.asUser === true })
    return w.submitDrop !== undefined ? { drop: w.submitDrop } : { text: e.text }
  })

  const moveTo = (id: string, dir: string = cwd): void => {
    sessionId = id
    cwd = dir
  }

  return { clock, requests, logs, shown, tokenReads: () => reads, checks, polls, acks, aborts, submits, moveTo, modelCalls }
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
