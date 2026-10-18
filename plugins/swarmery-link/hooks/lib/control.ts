// The command channel's decisions. Pure: builds the poll and ack requests,
// reads a command out of a reply, decides what to do with it, and keeps the
// set of commands already executed. Every `$` call (fetch, clock, turn.abort,
// prompt.submit) is made in register.ts.
// Contract: tools/swarmery/docs/mods-link-protocol.md, "Commands".

export const COMMANDS_PATH = '/api/link/commands'
/** How long the daemon may hold a poll open (it clamps anything above 25). */
export const POLL_WAIT_S = 25
/** A poll's own bound, past the daemon's 25 s hold: HttpInit has no timeout. */
export const POLL_TIMEOUT_MS = 28_000
/** After a failed poll (no answer, a 5xx, an unreadable reply). */
export const FAILURE_BACKOFF_MS = 3_000
/** Two polls that came back empty start at least this far apart. */
export const MIN_POLL_GAP_MS = 1_000
/** `$.prompt.submit` resolves as its turn starts; past this, the prompt is left queued. */
export const PROMPT_START_TIMEOUT_MS = 10_000
/** The exact ack message the daemon reads as "deliver again after the next turn.complete". */
export const TURN_IN_PROGRESS = 'turn in progress'
/** The daemon keeps an ack message up to 500 characters. */
export const MAX_ACK_MESSAGE = 500
/** Executed ids remembered for re-delivery, newest kept. */
export const MAX_REMEMBERED = 200

export const commandsUrl = (origin: string, sessionId: string): string =>
  `${origin}${COMMANDS_PATH}?session=${encodeURIComponent(sessionId)}&wait=${POLL_WAIT_S}`

export const ackUrl = (origin: string, id: string): string => `${origin}${COMMANDS_PATH}/${encodeURIComponent(id)}/ack`

export type PollRequest = {
  readonly method: 'GET'
  readonly headers: Record<string, string>
}

export const pollRequest = (token: string): PollRequest => ({
  method: 'GET',
  headers: { authorization: `Bearer ${token}` },
})

/**
 * What a poll's reply means for the loop:
 * - `command`: 200, a command to execute and ack, then poll again at once.
 * - `idle`: 204, nothing deliverable; poll again.
 * - `auth`: 401; re-read the token once, else stop.
 * - `off`: 503 link disabled; stop.
 * - `retry`: anything else, a timeout or no answer; back off, then poll again.
 */
export type PollOutcome = 'command' | 'idle' | 'auth' | 'off' | 'retry'

export const pollOutcome = (status: number | undefined): PollOutcome => {
  if (status === 200) return 'command'
  if (status === 204) return 'idle'
  if (status === 401) return 'auth'
  if (status === 503) return 'off'
  return 'retry'
}

/** How long to wait before the next poll, given how the last one ended and how long it took. */
export const nextPollDelay = (outcome: 'command' | 'idle' | 'retry', elapsedMs: number): number => {
  if (outcome === 'command') return 0
  if (outcome === 'retry') return FAILURE_BACKOFF_MS
  return Math.max(0, MIN_POLL_GAP_MS - Math.max(0, elapsedMs))
}

export type Command =
  | { readonly id: string; readonly kind: 'stop' }
  | { readonly id: string; readonly kind: 'prompt'; readonly text: string; readonly asUser: boolean }
  | { readonly id: string; readonly kind: 'message'; readonly to: string; readonly text: string }
  | { readonly id: string; readonly kind: 'invalid'; readonly reason: string }

const isRecord = (value: unknown): value is Readonly<Record<string, unknown>> =>
  typeof value === 'object' && value !== null && !Array.isArray(value)

const commandId = (value: unknown): string | undefined => {
  if (typeof value === 'number' && Number.isSafeInteger(value) && value > 0) return String(value)
  if (typeof value === 'string' && /^[1-9]\d{0,18}$/.test(value)) return value
  return undefined
}

/**
 * The command a 200 carries, or undefined when the reply names no id to ack
 * (the loop then backs off as on a failure). A command it cannot run comes
 * back `invalid`, to be acked failed.
 */
export const parseCommand = (text: unknown): Command | undefined => {
  if (typeof text !== 'string') return undefined
  let body: unknown
  try {
    body = JSON.parse(text)
  } catch {
    return undefined
  }
  if (!isRecord(body)) return undefined
  const id = commandId(body.id)
  if (id === undefined) return undefined
  const payload = isRecord(body.payload) ? body.payload : {}
  if (body.kind === 'stop') return { id, kind: 'stop' }
  if (body.kind === 'prompt') {
    const text = payload.text
    if (typeof text !== 'string' || text.trim() === '') return { id, kind: 'invalid', reason: 'prompt without text' }
    return { id, kind: 'prompt', text, asUser: payload.asUser === true }
  }
  if (body.kind === 'message') {
    const { to, text } = payload
    if (typeof to !== 'string' || to.trim() === '') return { id, kind: 'invalid', reason: 'message without a recipient' }
    if (typeof text !== 'string' || text.trim() === '') return { id, kind: 'invalid', reason: 'message without text' }
    return { id, kind: 'message', to: to.trim(), text }
  }
  return { id, kind: 'invalid', reason: `unknown command kind: ${String(body.kind).slice(0, 40)}` }
}

export type Ack = { readonly result: 'done' | 'failed'; readonly message?: string }

export const done = (message?: string): Ack => (message === undefined ? { result: 'done' } : { result: 'done', message })
export const failed = (message: string): Ack => ({ result: 'failed', message })

export const ackBody = (ack: Ack): string =>
  JSON.stringify(ack.message === undefined ? { result: ack.result } : { result: ack.result, message: ack.message.slice(0, MAX_ACK_MESSAGE) })

/** The executed commands by id, with the ack each got: a re-delivery is acked the same, never run again. */
export type Executed = ReadonlyMap<string, Ack>

/**
 * Records `ack` under `id`, keeping the newest MAX_REMEMBERED. A prompt
 * refused for a running turn is not recorded: the daemon delivers it again
 * after the turn, and then it runs.
 */
export const remember = (executed: Executed, id: string, ack: Ack): Executed => {
  if (ack.result === 'failed' && ack.message === TURN_IN_PROGRESS) return executed
  const next = new Map(executed)
  next.delete(id)
  next.set(id, ack)
  const extra = next.size - MAX_REMEMBERED
  if (extra > 0) for (const old of [...next.keys()].slice(0, extra)) next.delete(old)
  return next
}

export type Action =
  | { readonly do: 'ack'; readonly ack: Ack }
  | { readonly do: 'abort'; readonly turnId: string }
  | { readonly do: 'submit'; readonly text: string; readonly asUser: boolean }
  | { readonly do: 'message'; readonly to: string; readonly text: string }

/** The ack for a `message` command while the `swarm` option is off. */
export const SWARM_OFF = 'swarm option is off'

/**
 * What to do with a delivered command, given the commands already executed,
 * the main loop's running turn (`turn.start`'s id, until its
 * `turn.complete`), and whether the `swarm` option is on. A `message` is sent
 * whether or not a turn runs: it lands in the recipient's queue, not this one.
 */
export const decide = (command: Command, executed: Executed, runningTurnId: string | undefined, isSwarmOn = false): Action => {
  const previous = executed.get(command.id)
  if (previous !== undefined) return { do: 'ack', ack: previous }
  switch (command.kind) {
    case 'invalid':
      return { do: 'ack', ack: failed(command.reason) }
    case 'message':
      return isSwarmOn ? { do: 'message', to: command.to, text: command.text } : { do: 'ack', ack: failed(SWARM_OFF) }
    case 'stop':
      return runningTurnId === undefined ? { do: 'ack', ack: done('no turn running') } : { do: 'abort', turnId: runningTurnId }
    case 'prompt':
      return runningTurnId === undefined
        ? { do: 'submit', text: command.text, asUser: command.asUser }
        : { do: 'ack', ack: failed(TURN_IN_PROGRESS) }
  }
}

/** A one-line, bounded reason for an ack or a log line. */
export const shortError = (error: unknown): string => {
  const text = error instanceof Error ? error.message : String(error)
  return (text.split('\n')[0] ?? '').slice(0, 160) || 'unknown error'
}

/** Whether a rejected `$.prompt.submit` says a turn is running. */
export const isTurnRunning = (error: unknown): boolean =>
  /\bturn\b.*\b(running|in progress|active|busy)\b|\bnot idle\b|\bbusy\b/i.test(shortError(error))

/** The ack for a `$.prompt.submit` that rejected. */
export const submitFailure = (error: unknown): Ack => (isTurnRunning(error) ? failed(TURN_IN_PROGRESS) : failed(shortError(error)))

/** The ack for a `$.session.send` that delivered nothing. */
export const undeliverable = (reason: string): Ack => failed(`undeliverable: ${shortError(reason)}`)

/** The ack for a `$.prompt.submit` a hook answered with `{ drop }`: the same rule, the reason marked as a drop. */
export const dropAck = (reason: string): Ack => (isTurnRunning(reason) ? failed(TURN_IN_PROGRESS) : failed(`dropped: ${shortError(reason)}`))
