// The approvals channel's decisions. Pure: which verdicts go to the daemon,
// the permission request and its poll URL, what a reply means, the deadline,
// and what the hook answers. Every `$` call (fetch, clock, status line,
// notice) is made in hooks/approvals.ts.
// Contract: POST /api/link/permission and GET /api/link/permission/{id}
// (tools/swarmery/docs/mods-link-protocol.md); plan decision D3: the mod asks
// first, and every failure falls back to the engine's verdict, so the shim or
// the native dialog takes over.

export const PERMISSION_PATH = '/api/link/permission'
/** The POST's own bound: HttpInit has no timeout. */
export const PERMISSION_POST_TIMEOUT_MS = 5_000
/** How long the daemon may hold a poll open. */
export const PERMISSION_WAIT_S = 25
/** A poll's own bound, past the daemon's 25 s hold. */
export const PERMISSION_POLL_TIMEOUT_MS = 28_000
/** Past `expiresAt`, how long the mod keeps polling for a decision made at the last moment. */
export const DEADLINE_GRACE_MS = 5_000
/** A pending request with no readable `expiresAt` is waited on this long (the daemon's approval window). */
export const DEFAULT_WINDOW_MS = 600_000
/** Two polls that came back empty start at least this far apart. */
export const MIN_POLL_GAP_MS = 1_000
/** Consecutive failed polls (no answer, 5xx, unreadable) after which the engine's verdict stands. */
export const MAX_POLL_FAILURES = 3
/** After a 401, the one retry waits past the daemon's 1 s token re-read throttle. */
export const PERMISSION_AUTH_RETRY_MS = 1_100
/** The fire-and-forget DELETE's own bound: nothing waits on it past this. */
export const PERMISSION_CANCEL_TIMEOUT_MS = 2_000

export const WAITING_STATUS = 'waiting for Swarmery approval…'
export const LOCAL_NOTICE = 'production deploy: confirm it here, not in Swarmery'
export const APPROVED_REASON = 'approved in Swarmery'
export const DENIED_REASON = 'denied in Swarmery'

/** Tools that need the person: a hook's allow does not dismiss their dialog, so they never go to tool.check's channel. */
export const PERSON_TOOLS: ReadonlySet<string> = new Set(['AskUserQuestion', 'ExitPlanMode'])

export type Verdict = { readonly decision: 'allow' | 'ask' | 'deny' }

/** What tool.check is deciding, as far as the routing reads it. */
export type CheckFacts = {
  readonly tool: string
  readonly tool_use_id?: string
  readonly ceiling?: 'allow' | 'ask' | 'deny'
}

/**
 * Whether the engine's verdict goes to the daemon: only an `ask`, only on a
 * real call (a `$.tool.check` query has no id), never for a tool that needs
 * the person, and never where the organization's ceiling keeps the call at
 * `ask` or below (an allow from Swarmery could not stand there).
 */
export const shouldAsk = (verdict: Verdict, e: CheckFacts): boolean =>
  verdict.decision === 'ask' &&
  e.tool_use_id !== undefined &&
  e.tool_use_id !== '' &&
  !PERSON_TOOLS.has(e.tool) &&
  e.ceiling !== 'ask' &&
  e.ceiling !== 'deny'

export type PermissionRequest = {
  readonly sessionId: string
  readonly cwd: string
  readonly toolName: string
  readonly toolInput: unknown
  readonly toolUseId: string
  readonly agentId?: string
}

export const permissionBody = (request: PermissionRequest): string =>
  JSON.stringify({
    sessionId: request.sessionId,
    cwd: request.cwd,
    toolName: request.toolName,
    toolInput: request.toolInput ?? null,
    toolUseId: request.toolUseId,
    ...(request.agentId !== undefined ? { agentId: request.agentId } : {}),
  })

export const permissionUrl = (origin: string): string => `${origin}${PERMISSION_PATH}`

export const permissionPollUrl = (origin: string, id: string, sessionId: string): string =>
  `${origin}${PERMISSION_PATH}/${encodeURIComponent(id)}?session=${encodeURIComponent(sessionId)}&wait=${PERMISSION_WAIT_S}`

export type PermissionPoll = {
  readonly method: 'GET'
}

/** A poll; signedFetch (../signed.ts) adds its signature. */
export const permissionPoll = (): PermissionPoll => ({ method: 'GET' })

/** The DELETE that gives up on request `id` (the dispatch was abandoned mid-wait). */
export const permissionCancelUrl = (origin: string, id: string, sessionId: string): string =>
  `${origin}${PERMISSION_PATH}/${encodeURIComponent(id)}?session=${encodeURIComponent(sessionId)}`

export type PermissionCancel = {
  readonly method: 'DELETE'
}

/** The give-up; signedFetch (../signed.ts) adds its signature. */
export const permissionCancel = (): PermissionCancel => ({ method: 'DELETE' })

/**
 * One request's end: a status and body, or an error (`'timeout'` for the
 * mod's own bound). `isVerified`: the answer carries the daemon's signature
 * over this request (../signed.ts); absent counts as not signed.
 */
export type HttpReply = { readonly status?: number; readonly text?: string; readonly error?: unknown; readonly isVerified?: boolean }

/**
 * A 200 or 202 that the daemon did not sign is not the daemon's: whatever it
 * says (allow, deny, local, a request to wait on), the engine's verdict stands.
 */
const isUnsigned = (reply: HttpReply): boolean => (reply.status === 200 || reply.status === 202) && reply.isVerified !== true

/**
 * What a reply means:
 * - `allow` / `deny` / `local` / `pass`: the daemon's decision (`answer` is a question's, when it carries one).
 * - `pending`: 202, a person decides in the dashboard; poll `id` until `deadline`.
 * - `wait`: a poll's 204, nothing decided yet.
 * - `skip`: the POST's 204 (an excluded project) or a poll's 404 (the request is gone).
 * - `auth`: 401.
 * - `fail`: no answer, a timeout, 429, 5xx, any other status or an unreadable body.
 */
export type Answer =
  | { readonly kind: 'allow'; readonly answer?: unknown }
  | { readonly kind: 'deny'; readonly message: string }
  | { readonly kind: 'local' }
  | { readonly kind: 'pass' }
  | { readonly kind: 'pending'; readonly id: string; readonly deadline: number }
  | { readonly kind: 'wait' }
  | { readonly kind: 'skip' }
  | { readonly kind: 'auth' }
  | { readonly kind: 'fail' }

const isRecord = (value: unknown): value is Readonly<Record<string, unknown>> =>
  typeof value === 'object' && value !== null && !Array.isArray(value)

const parseJson = (text: unknown): Readonly<Record<string, unknown>> | undefined => {
  if (typeof text !== 'string') return undefined
  try {
    const body: unknown = JSON.parse(text)
    return isRecord(body) ? body : undefined
  } catch {
    return undefined
  }
}

const requestId = (value: unknown): string | undefined => {
  if (typeof value === 'number' && Number.isSafeInteger(value) && value > 0) return String(value)
  if (typeof value === 'string' && /^[1-9]\d{0,18}$/.test(value)) return value
  return undefined
}

/** A 200's decision. */
const decisionOf = (text: unknown): Answer => {
  const body = parseJson(text)
  switch (body?.decision) {
    case 'allow':
      return body.answer === undefined ? { kind: 'allow' } : { kind: 'allow', answer: body.answer }
    case 'deny':
      return { kind: 'deny', message: typeof body.message === 'string' ? body.message.trim() : '' }
    case 'local':
      return { kind: 'local' }
    case 'pass':
      return { kind: 'pass' }
    default:
      return { kind: 'fail' }
  }
}

/** When a pending request stops being waited on: its `expiresAt` plus the grace, or the default window from `now`. */
export const deadlineOf = (expiresAt: unknown, now: number): number => {
  const at = typeof expiresAt === 'string' ? Date.parse(expiresAt) : Number.NaN
  return Number.isFinite(at) ? at + DEADLINE_GRACE_MS : now + DEFAULT_WINDOW_MS + DEADLINE_GRACE_MS
}

/** What the POST's reply means; an unsigned 200/202 is a `pass`. */
export const postAnswer = (reply: HttpReply, now: number): Answer => {
  if (isUnsigned(reply)) return { kind: 'pass' }
  switch (reply.status) {
    case 200:
      return decisionOf(reply.text)
    case 202: {
      const body = parseJson(reply.text)
      const id = requestId(body?.id)
      return id === undefined ? { kind: 'fail' } : { kind: 'pending', id, deadline: deadlineOf(body?.expiresAt, now) }
    }
    case 204:
      return { kind: 'skip' }
    case 401:
      return { kind: 'auth' }
    default:
      return { kind: 'fail' }
  }
}

/** What a poll's reply means; an unsigned 200 is a `pass`. */
export const pollAnswer = (reply: HttpReply): Answer => {
  if (isUnsigned(reply)) return { kind: 'pass' }
  switch (reply.status) {
    case 200:
      return decisionOf(reply.text)
    case 204:
      return { kind: 'wait' }
    case 404:
      return { kind: 'skip' }
    case 401:
      return { kind: 'auth' }
    default:
      return { kind: 'fail' }
  }
}

/** How long the next poll may run: its own bound, cut at the deadline. */
export const pollBound = (deadline: number, now: number): number => Math.max(0, Math.min(PERMISSION_POLL_TIMEOUT_MS, deadline - now))

/** How long to wait before polling again after an empty reply that took `elapsedMs`. */
export const pollGap = (elapsedMs: number): number => Math.max(0, MIN_POLL_GAP_MS - Math.max(0, elapsedMs))

/**
 * tool.check's answer for what the daemon decided. Anything that is not an
 * allow or a deny leaves the engine's verdict: the mod never answers `ask` of
 * its own and never refuses on a failure.
 */
export const checkResult = <V extends Verdict>(answer: Answer, verdict: V): V | { decision: 'allow' | 'deny'; reason: string } => {
  if (answer.kind === 'allow') return { decision: 'allow', reason: APPROVED_REASON }
  if (answer.kind === 'deny') return { decision: 'deny', reason: answer.message === '' ? DENIED_REASON : answer.message }
  return verdict
}

export type QuestionAnswer = { readonly questions: readonly unknown[]; readonly answers: Readonly<Record<string, unknown>> }

/** A multi-select's labels (the dashboard sends them as a list) comma-joined, the form M4 verified; any other answer as is. */
const answerText = (value: unknown): unknown => (Array.isArray(value) ? value.join(', ') : value)

/**
 * The AskUserQuestion result an `allow` carries: `{ questions, answers }`,
 * the questions as asked when the answer leaves them out, a multi-select's
 * labels comma-joined. Undefined when it carries no answers map, so the
 * native dialog takes over.
 */
export const questionAnswer = (answer: Answer, asked: unknown): QuestionAnswer | undefined => {
  if (answer.kind !== 'allow' || !isRecord(answer.answer)) return undefined
  const { questions, answers } = answer.answer
  if (!isRecord(answers)) return undefined
  const shown = Array.isArray(questions) ? questions : Array.isArray(asked) ? asked : undefined
  if (shown === undefined) return undefined
  const joined = Object.fromEntries(Object.entries(answers).map(([question, value]) => [question, answerText(value)]))
  return { questions: shown, answers: joined }
}

/** What the AskUserQuestion tool.call hook returns instead of running the call. */
export type QuestionOutcome = { readonly result: QuestionAnswer } | { readonly deny: string }

/**
 * The AskUserQuestion tool.call's answer for what the daemon decided: the
 * person's answer as `{ result }`, a dashboard deny as the call's refusal (its
 * message, or "denied in Swarmery"), so the operator's deny is not lost to the
 * native dialog. Undefined on anything else (an allow without answers, a pass,
 * a failure), so the native dialog takes over.
 */
export const questionOutcome = (answer: Answer, asked: unknown): QuestionOutcome | undefined => {
  if (answer.kind === 'deny') return { deny: answer.message === '' ? DENIED_REASON : answer.message }
  const result = questionAnswer(answer, asked)
  return result === undefined ? undefined : { result }
}
