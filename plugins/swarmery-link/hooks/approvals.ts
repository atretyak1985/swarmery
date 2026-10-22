// The approvals channel (option `approvals`, plan decision D3: the mod first,
// the shim as the fallback). A permission `ask` the engine reached for a real
// call goes to the daemon's queue, and the mod waits for the dashboard's
// decision inside server-held long polls (time inside `$.http.fetch` does not
// count against a hook's 10 s budget; mods-format.md §M3). An AskUserQuestion
// is put to the dashboard the same way and answered (or refused) from it (§M4).
//
// Every failure falls back to the engine's verdict (tool.check) or to the
// question's own dialog (tool.call): no answer, a timeout, a 5xx, a second
// 401, the deadline, the person interrupting. The mod never answers `ask` of
// its own and never refuses on a failure. Only an interrupt mid-wait gives
// the daemon's request up (a fire-and-forget DELETE); on every other end the
// request stays pending for the shim to attach to.
//
// The `$` calls live in the functions declared at the top of this file;
// ./lib/approvals.ts holds the pure parts.

import type { Args, EngineInterface, ResultOf, Timer } from 'claude-code'
import {
  type Answer,
  type HttpReply,
  LOCAL_NOTICE,
  MAX_POLL_FAILURES,
  PERMISSION_AUTH_RETRY_MS,
  PERMISSION_CANCEL_TIMEOUT_MS,
  PERMISSION_POST_TIMEOUT_MS,
  type PermissionRequest,
  type QuestionOutcome,
  WAITING_STATUS,
  checkResult,
  permissionBody,
  permissionCancel,
  permissionCancelUrl,
  permissionPoll,
  permissionPollUrl,
  permissionUrl,
  pollAnswer,
  pollBound,
  pollGap,
  postAnswer,
  questionOutcome,
  shouldAsk,
} from './lib/approvals'
import { linkRequest } from './lib/client'
import { toolInput } from './lib/hash'
import { matchProd } from './lib/prod'
import { type LinkInit, signedFetch } from './signed'

/** What the channel reads of the link, as of the call it decides. */
export type Gate = {
  /** The daemon's loopback origin. */
  readonly origin: string
  readonly sessionId: string
  readonly cwd: string
  /** The link's current token: the key every request is signed with (never sent). */
  readonly token: () => string
  /** Re-reads the token file into the link (after a 401). */
  readonly reread: () => Promise<void>
  /** How many calls wait on the dashboard right now: the status line is the plugin's one, shared. */
  readonly waiting: { count: number }
}

const PASS: Answer = { kind: 'pass' }

/** Settles `'aborted'` once the dispatch is abandoned (the person interrupted, a hook above settled first). */
const abortOf = (signal: AbortSignal): Promise<'aborted'> =>
  new Promise(resolve => {
    if (signal.aborted) {
      resolve('aborted')
      return
    }
    signal.addEventListener('abort', () => resolve('aborted'), { once: true })
  })

/** `pending`, `'timeout'` once `ms` pass on `$.clock`, or `'aborted'`, whichever comes first. No timer: `'timeout'` at once. */
async function bounded<T>($: EngineInterface, pending: Promise<T>, ms: number, aborted: Promise<'aborted'>): Promise<T | 'timeout' | 'aborted'> {
  let guard: Timer | undefined
  const timeout = new Promise<'timeout'>(resolve => {
    try {
      guard = $.clock.after(ms, () => resolve('timeout'))
    } catch {
      resolve('timeout')
    }
  })
  try {
    return await Promise.race([pending, timeout, aborted])
  } finally {
    guard?.cancel()
  }
}

/**
 * One signed request, bounded by `ms` on `$.clock` (HttpInit has no timeout or
 * signal) and by the dispatch's abort. The reply says whether the daemon
 * signed it; only a signed decision counts (lib/approvals.ts postAnswer).
 */
async function request(
  $: EngineInterface,
  gate: Gate,
  url: string,
  init: LinkInit,
  ms: number,
  aborted: Promise<'aborted'>,
): Promise<HttpReply | 'aborted'> {
  const pending = signedFetch($, gate.token(), url, init).then(
    (r): HttpReply => ({ status: r.status, text: r.text, isVerified: r.isVerified }),
    (error: unknown): HttpReply => ({ error }),
  )
  const reply = await bounded($, pending, ms, aborted)
  return reply === 'timeout' ? { error: 'timeout' } : reply
}

/** Waits `ms` on `$.clock`, or less when the dispatch is abandoned. */
async function pause($: EngineInterface, ms: number, aborted: Promise<'aborted'>): Promise<'aborted' | undefined> {
  if (ms <= 0) return undefined
  // a fresh never-settling promise per wait, so no race's reactions pile up on a shared one
  const ended = await bounded($, new Promise<never>(() => undefined), ms, aborted)
  return ended === 'aborted' ? 'aborted' : undefined
}

/** Sets (or, with undefined, clears) the plugin's status line; a refused one is skipped. */
function showStatus($: EngineInterface, text: string | undefined): void {
  try {
    $.ui.status(text)
  } catch {
    // a status line is a courtesy
  }
}

/** A line under the call's dialog; refused when no dialog is open for it, which is fine. */
function showNotice($: EngineInterface, toolUseId: string, text: string): void {
  try {
    $.ui.notice(toolUseId, text)
  } catch {
    // the dialog says the rest
  }
}

/**
 * Tells the daemon the mod gave up on request `id` (DELETE), so a dashboard
 * decision no longer lands on a row no hook reads. Fire-and-forget: raced
 * against a 2 s `$.clock` timer and never awaited, so the hook's return never
 * waits on it; a lost DELETE only leaves the daemon's lease to lapse.
 */
function cancelRequest($: EngineInterface, gate: Gate, id: string): void {
  try {
    const sent = signedFetch($, gate.token(), permissionCancelUrl(gate.origin, id, gate.sessionId), permissionCancel()).catch(() => undefined)
    // the dispatch is already abandoned, so the race runs against a wait that never ends rather than its abort
    bounded($, sent, PERMISSION_CANCEL_TIMEOUT_MS, new Promise<never>(() => undefined)).catch(() => undefined)
  } catch {
    // the lease lapsing says the same, later
  }
}

/**
 * Polls a pending request until the dashboard decides, the deadline passes,
 * the dispatch is abandoned (`'aborted'`) or the daemon fails (`pass`).
 */
async function pollDecision(
  $: EngineInterface,
  gate: Gate,
  id: string,
  deadline: number,
  signal: AbortSignal,
  aborted: Promise<'aborted'>,
  hasRetriedAuth: boolean,
): Promise<Answer | 'aborted'> {
  let failures = 0
  let isRetried = hasRetriedAuth
  for (;;) {
    const startedAt = await $.clock.now()
    const bound = pollBound(deadline, startedAt)
    if (signal.aborted) return 'aborted'
    if (bound <= 0) return PASS
    const reply = await request($, gate, permissionPollUrl(gate.origin, id, gate.sessionId), permissionPoll(), bound, aborted)
    if (reply === 'aborted' || signal.aborted) return 'aborted'
    const answer = pollAnswer(reply)
    switch (answer.kind) {
      case 'wait': {
        failures = 0
        if ((await pause($, pollGap((await $.clock.now()) - startedAt), aborted)) === 'aborted') return 'aborted'
        continue
      }
      case 'auth': {
        if (isRetried) return PASS
        isRetried = true
        await gate.reread()
        if ((await pause($, PERMISSION_AUTH_RETRY_MS, aborted)) === 'aborted') return 'aborted'
        continue
      }
      case 'fail': {
        failures += 1
        if (failures >= MAX_POLL_FAILURES) return PASS
        continue
      }
      default:
        return answer
    }
  }
}

/**
 * Waits on a pending request (pollDecision) with the status line up. An
 * abandoned dispatch gives the request up with a DELETE; every other end
 * (the deadline, failed polls, a 404, a second 401) leaves the row pending,
 * so the shim the engine fires next can attach to it by hash.
 */
async function waitForDecision(
  $: EngineInterface,
  gate: Gate,
  id: string,
  deadline: number,
  signal: AbortSignal,
  aborted: Promise<'aborted'>,
  hasRetriedAuth: boolean,
): Promise<Answer> {
  gate.waiting.count += 1
  if (gate.waiting.count === 1) showStatus($, WAITING_STATUS)
  try {
    const answer = await pollDecision($, gate, id, deadline, signal, aborted, hasRetriedAuth)
    if (answer !== 'aborted') return answer
    cancelRequest($, gate, id)
    return PASS
  } finally {
    gate.waiting.count -= 1
    if (gate.waiting.count === 0) showStatus($, undefined)
  }
}

/**
 * Puts one call to the daemon and waits for the decision. Resolves to the
 * daemon's answer; every failure resolves to `pass` (the caller's fallback
 * stands). Never rejects.
 */
async function askSwarmery($: EngineInterface, gate: Gate, permission: PermissionRequest, signal: AbortSignal): Promise<Answer> {
  try {
    const aborted = abortOf(signal)
    const body = permissionBody(permission)
    let isRetried = false
    for (;;) {
      if (signal.aborted) return PASS
      const reply = await request($, gate, permissionUrl(gate.origin), linkRequest(body), PERMISSION_POST_TIMEOUT_MS, aborted)
      if (reply === 'aborted' || signal.aborted) return PASS
      const answer = postAnswer(reply, await $.clock.now())
      if (answer.kind === 'auth') {
        if (isRetried) return PASS
        isRetried = true
        await gate.reread()
        if ((await pause($, PERMISSION_AUTH_RETRY_MS, aborted)) === 'aborted') return PASS
        continue
      }
      if (answer.kind !== 'pending') return answer
      return await waitForDecision($, gate, answer.id, answer.deadline, signal, aborted, isRetried)
    }
  } catch {
    return PASS
  }
}

/**
 * tool.check: the engine's `ask` for a real call goes to the dashboard first.
 * An allow or a deny the daemon signed answers; anything else (an excluded
 * project, a pass, a failure, an unsigned answer, the deadline, an interrupt)
 * returns the engine's verdict untouched, so the shim or the native dialog
 * takes over. A production deploy is confirmed in the terminal, and its
 * dialog says so: one the mod matches itself (lib/prod.ts) never goes to the
 * daemon at all, and one the daemon matches comes back `local`.
 */
export async function checkWithSwarmery(
  $: EngineInterface,
  gate: Gate,
  e: Args<'tool.check'>,
  verdict: ResultOf['tool.check'],
  signal: AbortSignal,
): Promise<ResultOf['tool.check']> {
  const toolUseId = e.tool_use_id
  if (!shouldAsk(verdict, e) || toolUseId === undefined) return verdict
  if (matchProd(e.tool, e.input) !== undefined) {
    showNotice($, toolUseId, LOCAL_NOTICE)
    return verdict
  }
  const answer = await askSwarmery(
    $,
    gate,
    {
      sessionId: gate.sessionId,
      cwd: gate.cwd,
      toolName: e.tool,
      toolInput: e.input,
      toolUseId,
      ...(e.agentId !== undefined ? { agentId: e.agentId } : {}),
    },
    signal,
  )
  if (answer.kind === 'local') showNotice($, toolUseId, LOCAL_NOTICE)
  return checkResult(answer, verdict)
}

/**
 * tool.call of AskUserQuestion: the question goes to the dashboard first. The
 * person's answer there is returned as `{ result: { questions, answers } }`, a
 * dashboard deny as the call's refusal `{ deny }` (handing the call on would
 * open the native dialog and fire the shim, and the deny would be lost);
 * undefined on anything else (an allow without answers, a pass, a failure, the
 * deadline, an interrupt), so the caller hands the call on to the native
 * dialog.
 */
export async function answerFromSwarmery(
  $: EngineInterface,
  gate: Gate,
  fields: Readonly<Record<string, unknown>>,
  signal: AbortSignal,
): Promise<QuestionOutcome | undefined> {
  const toolUseId = fields.tool_use_id
  if (typeof toolUseId !== 'string' || toolUseId === '') return undefined
  const agentId = fields.agentId
  const answer = await askSwarmery(
    $,
    gate,
    {
      sessionId: gate.sessionId,
      cwd: gate.cwd,
      toolName: 'AskUserQuestion',
      toolInput: toolInput(fields),
      toolUseId,
      ...(typeof agentId === 'string' ? { agentId } : {}),
    },
    signal,
  )
  return questionOutcome(answer, fields.questions)
}
