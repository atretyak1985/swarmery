// swarmery-link 0.2: a sensor, plus an opt-in command channel. It reports this
// session's live state to a local swarmery daemon over POST /api/link/events
// (tools/swarmery/docs/mods-link-protocol.md) and changes nothing: every hook
// passes its event on, a failure is logged and swallowed, and with no token
// file the whole mod stays silent for the session and makes no request.
//
// With `control` on, one background loop long-polls GET /api/link/commands
// and runs what the dashboard queued for this session: `stop` aborts the
// running turn, `prompt` submits a prompt once the session is idle. Commands
// run only from that loop, never from inside a hook. With `control` off none
// of it is created and no request goes to the command endpoints.
//
// With `forecast` on, each main-loop turn that starts with the person's text
// asks a small model, without waiting on it, what the turn will touch; the
// turn's own tool calls make the actual. Both go to the daemon as
// `turn.forecast` and `turn.actual` (paths and numbers only). With `forecast`
// off no model call is made and neither event is sent.
//
// With `approvals` on, a permission `ask` the engine reached for a real call
// goes to the daemon's approvals queue first (tool.check), and an
// AskUserQuestion is put to the dashboard (tool.call); every failure leaves
// the engine's verdict or the question's dialog (./approvals.ts). With
// `approvals` off neither hook is registered and no request goes to the
// permission endpoint.
//
// With `swarm` on, messages this conversation sends to or receives from other
// sessions go to the daemon as `session.send` / `session.receive`, each file
// the session starts editing is claimed in a registry shared through
// `$.store` (a `claim`, and a `conflict` when another live session holds the
// same path, shown as a line beneath the answer), claims are released at each
// turn's end, and with `control` on too the dashboard's `message` command is
// sent through `$.session.send`. With `swarm` off none of it runs and the
// send/receive hooks are not registered. With `claimConflicts` set (`ask` or
// `deny`, `swarm` on), a tool.check beneath the approvals channel's asks or
// refuses a write whose path another live session holds (`ask` turns into a
// refusal in a permission mode where no person sees the ask, read off the
// classic events), and the edit is claimed after the call instead of ahead of it.
//
// With `planBand` on, the session's running plan phase is drawn in a band
// above the prompt from a cache the daemon fills (GET /api/link/plan), `/plan`
// shows its checklist, an interview-style AskUserQuestion gets a card above
// the dialog, and a main-loop turn that edited files while a phase runs is
// classified once; an `off-phase` turn gets a line beneath its answer. With
// `planBand` off none of those hooks, the command, the timer or the
// classification exists, and no plan request is made.
//
// `$` is handed only to top-level function declarations, as the engine
// requires: the ones of this file, ./approvals.ts's, ./swarm.ts's, and the
// band's in ./plan.ts, ./drift.ts and ./ask-render.ts. ./lib/* is pure (data
// in, data out).

import type { EngineInterface, PromptSubmitArgs, Register, SessionSendInput, SessionSendResult, Timer } from 'claude-code'
import { type Gate, answerFromSwarmery, checkWithSwarmery } from './approvals'
import { drawInterview } from './ask-render'
import { checkDrift } from './drift'
import { interviewsOf } from './lib/interview'
import { PLAN_PANE_ID, editedPath, isEditTool, noteEdit } from './lib/plan'
import {
  type Planner,
  drawBand,
  drawPane,
  newPlanner,
  refreshPlan,
  registerPlanCommand,
  runPlanCommand,
  setView,
  startPlanTimer,
  stopPlanTimer,
} from './plan'
import {
  AUTH_RETRY_MS,
  DEFAULT_DAEMON_URL,
  END_FLUSH_TIMEOUT_MS,
  FLUSH_EVERY_MS,
  REQUEST_TIMEOUT_MS,
  daemonEndpoint,
  describeFailure,
  linkRequest,
  outcome,
  parseToken,
} from './lib/client'
import {
  type Ack,
  type Command,
  type Executed,
  POLL_TIMEOUT_MS,
  PROMPT_START_TIMEOUT_MS,
  ackBody,
  ackUrl,
  commandsUrl,
  decide,
  done,
  dropAck,
  failed,
  nextPollDelay,
  parseCommand,
  pollOutcome,
  pollRequest,
  SWARM_OFF,
  remember,
  shortError,
  submitFailure,
  undeliverable,
} from './lib/control'
import {
  FORECAST_MAX_TOKENS,
  FORECAST_MODEL,
  FORECAST_TIMEOUT_MS,
  type ForecastState,
  INITIAL_FORECAST,
  NO_CLAIMS,
  RUN_ID_RANDOM,
  type ToolOutcome,
  closeForecast,
  endTurn,
  forecastPayload,
  forecastRequest,
  noteToolCall,
  parseForecast,
  resetForecast,
  runIdOf,
  startTurn,
  usageTokens,
} from './lib/forecast'
import { inputHash, toolInput } from './lib/hash'
import { type LinkEvent, type LinkKind, type Stamps, drop, enqueue, stamp, takeBatch } from './lib/queue'
import {
  type ClaimConflicts,
  type ConflictVerdict,
  EMPTY_TURN,
  type ModeBook,
  type PendingSend,
  type SwarmTurn,
  claimConflictsOf,
  claimPath,
  conflictLines,
  conflictVerdict,
  isAmbiguousSend,
  modeOf,
  noteMode,
  pendingFor,
  receiveLine,
  receivePayload,
  releasePayload,
  sendPayload,
  withBeneath,
} from './lib/swarm'
import { measurePayload } from './lib/usage'
import { claimFile, heldElsewhere, releaseClaims, sendToSession } from './swarm'

const NAME = 'swarmery-link'

/** Options reserved for later releases: each logs once when switched on, and hooks nothing else. */
const RESERVED: readonly string[] = []

/** A dashboard message's `$.session.send` resolves once queued at the recipient; past this it is reported undeliverable. */
const MESSAGE_TIMEOUT_MS = 10_000

type Reply = { readonly status?: number; readonly error?: unknown }

/** The link's state for one load of the module (a reload starts it over). */
type Link = {
  readonly url: string
  token: string | undefined
  sessionId: string
  cwd: string
  /** The real session.start's `{ surface, isInteractive }`, sent again for the session a `/clear` starts. */
  start: Readonly<Record<string, unknown>> | undefined
  /**
   * A `/clear` ended the session and the process goes on under a new id with
   * no session.start: nothing is queued until the next turn.start re-reads it.
   */
  isClearing: boolean
  /** Off for the rest of this load: 503, or a 401 that survived a re-read. */
  isOff: boolean
  /** The last request got no answer or a 5xx: turn boundaries leave sending to the timer until one succeeds. */
  isFailing: boolean
  /** A 401 was answered with a token re-read; the one retry is pending. */
  isAuthWait: boolean
  hasRetriedAuth: boolean
  queue: readonly LinkEvent[]
  stamps: Stamps
  timer: Timer | undefined
  flushing: Promise<void> | undefined
  readonly logged: Set<string>
}

const isActive = (link: Link): boolean => link.token !== undefined && !link.isOff && link.sessionId !== ''

function log($: EngineInterface, text: string, toDebug = false): void {
  try {
    $.ui.log(`${NAME}: ${text}`, toDebug ? { to: 'debug' } : undefined)
  } catch {
    // a log line is a courtesy
  }
}

/** Logs `text` to the transcript the first time `key` is seen, to the debug log after. */
function logOnce($: EngineInterface, link: Link, key: string, text: string): void {
  log($, text, link.logged.has(key))
  link.logged.add(key)
}

/** The daemon's token (M7: `~` is not expanded, so HOME is read and spelled out). */
async function readToken($: EngineInterface): Promise<string | undefined> {
  try {
    const home = await $.env.get('HOME')
    return parseToken(await $.fs.read(`${home}/.swarmery/link-token`))
  } catch {
    return undefined
  }
}

function turnOff($: EngineInterface, link: Link, why: string): void {
  link.isOff = true
  link.queue = []
  link.timer?.cancel()
  link.timer = undefined
  log($, `link off for this session: ${why}`)
}

/** `pending`, or `'timeout'` once `ms` pass on `$.clock`, whichever comes first. */
async function within<T>($: EngineInterface, pending: Promise<T>, ms: number): Promise<T | 'timeout'> {
  let guard: Timer | undefined
  const timeout = new Promise<'timeout'>(resolve => {
    try {
      guard = $.clock.after(ms, () => resolve('timeout'))
    } catch {
      // no timer: the pending work alone decides
    }
  })
  try {
    return await Promise.race([pending, timeout])
  } finally {
    guard?.cancel()
  }
}

/** One POST of `body`, raced against a `$.clock` timer: HttpInit has no timeout or signal. */
async function post($: EngineInterface, link: Link, body: string, timeoutMs: number): Promise<Reply> {
  const request = $.http.fetch(link.url, linkRequest(link.token ?? '', body)).then(
    (r): Reply => ({ status: r.status }),
    (error: unknown): Reply => ({ error }),
  )
  const reply = await within($, request, timeoutMs)
  return reply === 'timeout' ? { error: 'timeout' } : reply
}

async function send($: EngineInterface, link: Link, timeoutMs: number): Promise<void> {
  while (isActive(link) && !link.isAuthWait && link.queue.length > 0) {
    const { batch, body, oversized } = takeBatch(link.queue)
    if (oversized.length > 0) link.queue = drop(link.queue, oversized)
    if (batch.length === 0) return
    const reply = await post($, link, body, timeoutMs)
    switch (outcome(reply.status)) {
      case 'sent':
        link.queue = drop(link.queue, batch)
        link.isFailing = false
        link.hasRetriedAuth = false
        continue
      case 'discard':
        link.queue = drop(link.queue, batch)
        logOnce($, link, `discard-${reply.status}`, `the daemon refused a batch (${describeFailure(reply.status, undefined)}); dropped`)
        continue
      case 'off':
        turnOff($, link, 'the daemon has the link disabled (503)')
        return
      case 'auth':
        if (link.hasRetriedAuth) {
          turnOff($, link, 'the daemon refused the token after a re-read (401)')
          return
        }
        link.hasRetriedAuth = true
        link.isAuthWait = true
        link.token = (await readToken($)) ?? link.token
        log($, 'token refused (401): re-read it, retrying once', true)
        try {
          $.clock.after(AUTH_RETRY_MS, () => {
            link.isAuthWait = false
            void flush($, link, REQUEST_TIMEOUT_MS)
          })
        } catch {
          link.isAuthWait = false
        }
        return
      case 'retry':
        link.isFailing = true
        logOnce($, link, 'unreachable', `daemon unreachable (${describeFailure(reply.status, reply.error)}); keeping up to 200 events`)
        return
    }
  }
}

/** Sends what is queued, one flush at a time. Never rejects. */
function flush($: EngineInterface, link: Link, timeoutMs: number): Promise<void> {
  if (link.flushing !== undefined) return link.flushing
  link.flushing = send($, link, timeoutMs)
    .catch(() => undefined)
    .finally(() => {
      link.flushing = undefined
    })
  return link.flushing
}

/** Arms the periodic flush once; it re-arms while events remain. */
function arm($: EngineInterface, link: Link): void {
  if (link.timer !== undefined || !isActive(link)) return
  try {
    link.timer = $.clock.after(FLUSH_EVERY_MS, () => {
      link.timer = undefined
      void flush($, link, REQUEST_TIMEOUT_MS).then(() => {
        if (link.queue.length > 0) arm($, link)
      })
    })
  } catch {
    link.timer = undefined
  }
}

async function push($: EngineInterface, link: Link, kind: LinkKind, payload?: Readonly<Record<string, unknown>>): Promise<void> {
  if (!isActive(link) || link.isClearing) return
  const stamped = stamp(link.stamps, kind, await $.clock.now())
  link.stamps = stamped.stamps
  const event: LinkEvent = { sessionId: link.sessionId, cwd: link.cwd, kind, at: stamped.at, ...(payload !== undefined ? { payload } : {}) }
  link.queue = enqueue(link.queue, event)
  arm($, link)
}

/**
 * At a turn boundary: start a send now while the daemon answers, else leave it
 * to the timer. Never waited on: a hook never blocks on the daemon.
 */
function flushAtTurn($: EngineInterface, link: Link): void {
  if (isActive(link) && !link.isFailing) void flush($, link, REQUEST_TIMEOUT_MS)
}

/**
 * session.end's last send: joins a flush already in flight (which may have
 * started under the 5 s request timeout), then sends what that one left. The
 * caller bounds the whole wait.
 */
async function flushForEnd($: EngineInterface, link: Link): Promise<void> {
  await flush($, link, END_FLUSH_TIMEOUT_MS)
  if (link.queue.length > 0 && !link.isFailing) await flush($, link, END_FLUSH_TIMEOUT_MS)
}

/**
 * After a `/clear`, at the first main-loop turn.start: the process goes on
 * under a new session id and no session.start fires for it (SessionEndInput),
 * so the id is read again here and, when it moved, the new session is
 * announced with the real session.start's payload.
 */
async function resync($: EngineInterface, link: Link): Promise<void> {
  const sessionId = await $.session.id()
  const cwd = (await $.session.cwd()) || link.cwd
  link.isClearing = false
  if (sessionId === link.sessionId) return
  link.sessionId = sessionId
  link.cwd = cwd
  await push($, link, 'session.start', link.start)
}

/** The swarm's state for one load of the module. Exists only with `swarm` on. */
type Swarm = {
  turn: SwarmTurn
  /** Claims and releases run one at a time: parallel tool calls must not lose a claim. */
  lock: Promise<void>
  /**
   * Dashboard messages being sent, by command id, until their send settles —
   * past the 10 s ack timeout too: the `session.send` a late send raises still
   * carries the command's id.
   */
  readonly pending: Map<string, PendingSend>
}

/** Runs `work` after every claim or release queued before it. Rejects as `work` does. */
function serialize<T>(swarm: Swarm, work: () => Promise<T>): Promise<T> {
  const run = swarm.lock.then(work)
  swarm.lock = run.then(
    () => undefined,
    () => undefined,
  )
  return run
}

/** A tool call that writes a file claims its absolute path: `claim`, plus a `conflict` per other live session holding it. */
async function claimEdit($: EngineInterface, link: Link, swarm: Swarm, tool: string, fields: Readonly<Record<string, unknown>>): Promise<void> {
  const path = claimPath(tool, fields, link.cwd)
  if (path === undefined) return
  await serialize(swarm, async () => {
    const outcome = await claimFile($, swarm.turn, link.sessionId, link.cwd, path)
    swarm.turn = outcome.turn
    if (!outcome.isNew) return
    if (outcome.storeError !== undefined) {
      logOnce($, link, 'swarm-store', `swarm: the claims registry refused (${shortError(outcome.storeError)}); the claim is still reported`)
    }
    await push($, link, 'claim', { path })
    for (const conflict of outcome.conflicts) await push($, link, 'conflict', { path: conflict.path, otherSession: conflict.otherSession })
  })
}

/**
 * tool.check with `claimConflicts` set: the verdict for a write whose path
 * another live session holds (ask, or deny where no person would see the
 * ask); undefined for any other call, which keeps the verdict beneath.
 */
async function checkClaims(
  $: EngineInterface,
  link: Link,
  setting: ClaimConflicts,
  modes: ModeBook,
  call: { readonly tool: string; readonly input: unknown; readonly agentId?: string | undefined },
): Promise<ConflictVerdict | undefined> {
  if (typeof call.input !== 'object' || call.input === null) return undefined
  const path = claimPath(call.tool, call.input as Readonly<Record<string, unknown>>, link.cwd)
  if (path === undefined) return undefined
  return conflictVerdict(setting, modeOf(modes, call.agentId), await heldElsewhere($, link.sessionId, path))
}

/** The main loop's turn ended: its claims are released; returns the lines to show beneath the answer. */
async function endSwarmTurn($: EngineInterface, link: Link, swarm: Swarm): Promise<readonly string[]> {
  return serialize(swarm, async () => {
    const { claimed, conflicts, entry } = swarm.turn
    swarm.turn = EMPTY_TURN
    if (entry !== undefined) {
      try {
        await releaseClaims($, link.sessionId)
      } catch (error) {
        logOnce($, link, 'swarm-store', `swarm: the claims registry refused (${shortError(error)}); the claim lapses in 30 min`)
      }
    }
    if (claimed.length > 0) await push($, link, 'release', releasePayload(claimed))
    return conflictLines(conflicts)
  })
}

/** The session ends: every claim it holds is released. */
async function endSwarmSession($: EngineInterface, link: Link, swarm: Swarm): Promise<void> {
  await serialize(swarm, async () => {
    swarm.turn = EMPTY_TURN
    try {
      await releaseClaims($, link.sessionId)
    } catch {
      // the entry lapses with its TTL
    }
    await push($, link, 'release', { all: true })
  })
}

/**
 * A message this conversation's main loop sent: `session.send`, with the
 * command's id when the dashboard asked for it. One of this plugin's sends that
 * matches several pending dashboard messages is not reported here: each
 * message's own path (deliverMessage) reports it with its own id.
 */
async function noteSend($: EngineInterface, link: Link, swarm: Swarm, e: SessionSendInput, result: SessionSendResult): Promise<void> {
  const name = e.origin.kind === 'plugin' ? e.origin.name : undefined
  const isOwn = name === NAME || name?.startsWith(`${NAME}@`) === true
  if (isOwn && isAmbiguousSend(swarm.pending, e.to, e.text)) return
  const pending = isOwn ? pendingFor(swarm.pending, e.to, e.text) : undefined
  if (pending !== undefined) pending.isLogged = true
  await push(
    $,
    link,
    'session.send',
    sendPayload({ to: e.to, text: e.text, origin: e.origin.kind, isDelivered: result.isDelivered, reason: result.reason, commandId: pending?.commandId }),
  )
  flushAtTurn($, link)
}

/** A delivery to the main conversation: `session.receive`, and one line naming where it came from. */
async function noteReceive($: EngineInterface, link: Link, text: string, origin: unknown): Promise<void> {
  if (!isActive(link) || link.isClearing) return
  await push($, link, 'session.receive', receivePayload(text, origin))
  log($, receiveLine(text, origin))
  flushAtTurn($, link)
}

/**
 * The dashboard's `message` command: `$.session.send` to the session it names.
 * `done` once queued at the recipient, else `failed` + `undeliverable: <reason>`.
 * The send's `session.send` event carries the command's id; when the send hook
 * did not report it, it is reported here. Both happen once the send settles,
 * also when that is after the 10 s timeout acked the command failed: the
 * daemon then takes the send's verdict over the ack's.
 */
async function deliverMessage($: EngineInterface, link: Link, swarm: Swarm, commandId: string, to: string, text: string): Promise<Ack> {
  const pending: PendingSend = { commandId, to, text, isLogged: false }
  swarm.pending.set(commandId, pending)
  const sending = sendToSession($, to, text).then(
    (result): SessionSendResult => result,
    (error: unknown): SessionSendResult => ({ isDelivered: false, reason: shortError(error) }),
  )
  const reported = sending
    .then(async result => {
      if (pending.isLogged) return
      await push($, link, 'session.send', sendPayload({ to, text, origin: 'plugin', isDelivered: result.isDelivered, reason: result.reason, commandId }))
      flushAtTurn($, link)
    })
    .catch(() => undefined) // unreported
    .finally(() => {
      if (swarm.pending.get(commandId) === pending) swarm.pending.delete(commandId)
    })
  const reply = await within($, sending, MESSAGE_TIMEOUT_MS)
  if (reply === 'timeout') return undeliverable(`no answer in ${MESSAGE_TIMEOUT_MS / 1000}s`)
  // on time: the report is queued before the ack goes out
  await reported
  return reply.isDelivered ? done() : undeliverable(reply.reason)
}

/** The command channel's state for one session's loop. Exists only with `control` on. */
type Control = {
  readonly origin: string
  readonly sessionId: string
  /** Set for good by session.end, a 503, or a 401 that survived a re-read. */
  isStopped: boolean
  /** The main loop's running turn: `turn.start`'s id until its `turn.complete`. */
  turnId: string | undefined
  executed: Executed
  hasRetriedAuth: boolean
  /** The pending wait between polls, cancelled on stop. */
  pause: Timer | undefined
  /** Settles every race of the loop the moment it stops. */
  readonly stopped: Promise<'stopped'>
  readonly settleStopped: () => void
  /** The swarm's state, for the `message` command; undefined with `swarm` off. */
  readonly swarm: Swarm | undefined
}

type PollReply = { readonly status?: number; readonly text?: string; readonly error?: unknown }

const newControl = (origin: string, sessionId: string, previous: Control | undefined, swarm: Swarm | undefined): Control => {
  let settleStopped: () => void = () => undefined
  const stopped = new Promise<'stopped'>(resolve => {
    settleStopped = () => resolve('stopped')
  })
  return {
    origin,
    sessionId,
    isStopped: false,
    turnId: previous?.turnId,
    // ids are the daemon's, unique across sessions: a new loop keeps what ran
    executed: previous?.executed ?? new Map(),
    hasRetriedAuth: false,
    pause: undefined,
    stopped,
    settleStopped,
    swarm,
  }
}

function stopControl($: EngineInterface, control: Control, why: string | undefined): void {
  if (control.isStopped) return
  control.isStopped = true
  control.pause?.cancel()
  control.pause = undefined
  control.settleStopped()
  if (why !== undefined) log($, `control off for this session: ${why}`)
}

/** Waits `ms` on `$.clock` between polls, or less when the loop stops. A clock that refuses stops the loop. */
async function pause($: EngineInterface, control: Control, ms: number): Promise<void> {
  if (ms <= 0 || control.isStopped) return
  const waited = new Promise<void>(resolve => {
    try {
      control.pause = $.clock.after(ms, () => {
        control.pause = undefined
        resolve()
      })
    } catch {
      stopControl($, control, 'no timer for the poll loop')
      resolve()
    }
  })
  await Promise.race([waited, control.stopped])
}

/** One long poll, raced against its 28 s timer and the loop's stop. */
async function fetchCommand($: EngineInterface, link: Link, control: Control): Promise<PollReply | 'stopped'> {
  const request = $.http.fetch(commandsUrl(control.origin, control.sessionId), pollRequest(link.token ?? '')).then(
    (r): PollReply => ({ status: r.status, text: r.text }),
    (error: unknown): PollReply => ({ error }),
  )
  const reply = await Promise.race([within($, request, POLL_TIMEOUT_MS), control.stopped])
  return reply === 'timeout' ? { error: 'timeout' } : reply
}

/** Acks a command. A lost ack is not retried: the daemon delivers the command again and the executed set answers it. */
async function sendAck($: EngineInterface, link: Link, control: Control, id: string, ack: Ack): Promise<void> {
  try {
    const request = $.http.fetch(ackUrl(control.origin, id), linkRequest(link.token ?? '', ackBody(ack))).then(
      (r): Reply => ({ status: r.status }),
      (error: unknown): Reply => ({ error }),
    )
    const reply = await within($, request, REQUEST_TIMEOUT_MS)
    const status = reply === 'timeout' ? undefined : reply.status
    if (status === undefined || status < 200 || status >= 300) {
      const error = reply === 'timeout' ? 'timeout' : reply.error
      logOnce($, link, 'ack-failed', `command ${id}: ack not taken (${describeFailure(status, error)}); the daemon delivers it again`)
    }
  } catch {
    // the daemon re-delivers an unacked command after 30 s
  }
}

async function abortTurn($: EngineInterface, turnId: string): Promise<Ack> {
  try {
    await $.turn.abort({ turnId })
    return done()
  } catch (error) {
    return failed(shortError(error))
  }
}

/** Submits the prompt; it resolves as its turn starts, so a session that stays busy past the bound leaves it queued. */
async function submitPrompt($: EngineInterface, text: string, asUser: boolean): Promise<Ack> {
  const args: PromptSubmitArgs = asUser ? { text, asUser: true } : { text }
  try {
    const result = await within($, $.prompt.submit(args), PROMPT_START_TIMEOUT_MS)
    if (result === 'timeout') return done('queued')
    if (result.drop !== undefined) return dropAck(result.drop)
    return done()
  } catch (error) {
    return submitFailure(error)
  }
}

/** Runs one delivered command (or answers a re-delivered one from the executed set) and acks it. */
async function execute($: EngineInterface, link: Link, control: Control, command: Command): Promise<void> {
  const action = decide(command, control.executed, control.turnId, control.swarm !== undefined)
  let ack: Ack
  switch (action.do) {
    case 'ack':
      ack = action.ack
      break
    case 'abort':
      ack = await abortTurn($, action.turnId)
      break
    case 'submit':
      ack = await submitPrompt($, action.text, action.asUser)
      break
    case 'message':
      ack = control.swarm === undefined ? failed(SWARM_OFF) : await deliverMessage($, link, control.swarm, command.id, action.to, action.text)
      break
  }
  // recorded before the ack goes out: a lost ack must not run it twice
  control.executed = remember(control.executed, command.id, ack)
  if (!control.isStopped) await sendAck($, link, control, command.id, ack)
}

/** The command loop: poll, execute, ack, poll again, until it stops. Never rejects. */
async function poll($: EngineInterface, link: Link, control: Control): Promise<void> {
  try {
    while (!control.isStopped && !link.isOff) {
      const startedAt = await $.clock.now()
      const reply = await fetchCommand($, link, control)
      if (reply === 'stopped' || control.isStopped || link.isOff) return
      switch (pollOutcome(reply.status)) {
        case 'command': {
          const command = parseCommand(reply.text)
          control.hasRetriedAuth = false
          if (command === undefined) {
            logOnce($, link, 'command-unreadable', 'control: a command reply named no id; skipped')
            await pause($, control, nextPollDelay('retry', 0))
            continue
          }
          await execute($, link, control, command)
          continue
        }
        case 'idle':
          control.hasRetriedAuth = false
          await pause($, control, nextPollDelay('idle', (await $.clock.now()) - startedAt))
          continue
        case 'auth':
          if (control.hasRetriedAuth) {
            stopControl($, control, 'the daemon refused the token after a re-read (401)')
            return
          }
          control.hasRetriedAuth = true
          link.token = (await readToken($)) ?? link.token
          log($, 'control: token refused (401): re-read it, retrying once', true)
          await pause($, control, AUTH_RETRY_MS)
          continue
        case 'off':
          stopControl($, control, undefined)
          turnOff($, link, 'the daemon has the link disabled (503)')
          return
        case 'retry':
          logOnce($, link, 'control-unreachable', `control: command poll failed (${describeFailure(reply.status, reply.error)}); retrying every 3s`)
          await pause($, control, nextPollDelay('retry', 0))
          continue
      }
    }
  } catch {
    stopControl($, control, 'the poll loop failed')
  }
}

/** Starts the session's one command loop, detached from the hook that starts it. */
function startControl($: EngineInterface, link: Link, control: Control): void {
  try {
    $.clock.after(0, () => {
      void poll($, link, control)
    })
    log($, 'control: polling the daemon for commands', true)
  } catch {
    stopControl($, control, 'no timer for the poll loop')
  }
}

/** Replaces `previous` (stopped first) with a loop for the link's current session id, and starts it. */
function openControl($: EngineInterface, link: Link, origin: string, previous: Control | undefined, swarm: Swarm | undefined): Control {
  if (previous !== undefined) stopControl($, previous, undefined)
  const control = newControl(origin, link.sessionId, previous, swarm)
  startControl($, link, control)
  return control
}

/** The forecast loop's state for one load of the module. Exists only with `forecast` on. */
type Forecaster = {
  state: ForecastState
  /** An in-process resume ended the session: the next turn.start starts a new run (resync). */
  isResumed: boolean
}

/** RUN_ID_RANDOM random bytes from the runtime's `crypto`, or undefined where it has none. */
const randomBytes = (): Uint8Array | undefined => {
  try {
    if (typeof crypto === 'undefined' || typeof crypto.getRandomValues !== 'function') return undefined
    return crypto.getRandomValues(new Uint8Array(RUN_ID_RANDOM))
  } catch {
    return undefined
  }
}

/** Starts the turn counter over under a fresh run id: `<ms of $.clock.now()>-<random base36>`. */
async function newRun($: EngineInterface, forecaster: Forecaster): Promise<void> {
  let now = 0
  try {
    now = await $.clock.now()
  } catch {
    // the random part alone tells the runs apart
  }
  forecaster.state = resetForecast(forecaster.state, runIdOf(now, randomBytes()))
}

/**
 * Asks the model for turn `turnSeq`'s forecast and queues it as `turn.forecast`,
 * however the call ended (no readable claim: all-null claims, tokens still
 * counted). A reset while the call was in flight (`generation` moved) drops
 * it: the session id and the run it was asked under are gone. Never rejects;
 * nothing waits on it.
 */
async function runForecast(
  $: EngineInterface,
  link: Link,
  forecaster: Forecaster,
  runId: string,
  turnSeq: number,
  generation: number,
  ask: { readonly system: string; readonly prompt: string },
): Promise<void> {
  let claims = NO_CLAIMS
  let tokens: number | null = null
  try {
    const reply = await $.model.complete({
      model: FORECAST_MODEL,
      system: ask.system,
      prompt: ask.prompt,
      maxTokens: FORECAST_MAX_TOKENS,
      timeoutMs: FORECAST_TIMEOUT_MS,
    })
    tokens = usageTokens(reply.usage)
    if (reply.isAnswered) claims = parseForecast(reply.text, link.cwd)
    else logOnce($, link, `forecast-${reply.reason}`, `forecast: no reply (${reply.reason}); sent without claims`)
  } catch {
    logOnce($, link, 'forecast-refused', 'forecast: the model call was refused; sent without claims')
  } finally {
    forecaster.state = closeForecast(forecaster.state, generation)
  }
  if (forecaster.state.generation !== generation) return
  try {
    await push($, link, 'turn.forecast', forecastPayload(runId, turnSeq, claims, FORECAST_MODEL, tokens))
    flushAtTurn($, link)
  } catch {
    // unreported
  }
}

/**
 * A main-loop turn starts: count it, and when it carries the person's text and
 * no forecast is still open, start one detached from the hook, so the turn
 * never waits on the model.
 */
function beginForecast($: EngineInterface, link: Link, forecaster: Forecaster, turnId: string, text: string): void {
  const { state, shouldAsk } = startTurn(forecaster.state, turnId, text, isActive(link))
  forecaster.state = state
  if (!shouldAsk) return
  const run = (): void => {
    void runForecast($, link, forecaster, state.runId, state.turnSeq, state.generation, forecastRequest(text, state.history))
  }
  try {
    $.clock.after(0, run)
  } catch {
    run()
  }
}

/** A main-loop turn ends: queue its actual when the turn was forecast. */
async function finishForecast($: EngineInterface, link: Link, forecaster: Forecaster, turnId: string): Promise<void> {
  const { state, actual } = endTurn(forecaster.state, turnId)
  forecaster.state = state
  if (actual !== undefined) await push($, link, 'turn.actual', actual)
}

/** The approvals channel's view of the link for one call: its session as of now, its token, the token re-read. */
function gateOf($: EngineInterface, link: Link, origin: string, waiting: { count: number }): Gate {
  return {
    origin,
    sessionId: link.sessionId,
    cwd: link.cwd,
    token: () => link.token ?? '',
    reread: async () => {
      link.token = (await readToken($)) ?? link.token
    },
    waiting,
  }
}

/** How a tool call ended, from what `next(e)` resolved to. */
const toolOutcome = (result: unknown): ToolOutcome => {
  const r = result as { readonly deny?: unknown; readonly isError?: unknown } | undefined
  if (r?.deny !== undefined) return 'denied'
  return r?.isError === true ? 'error' : 'ok'
}

export const register: Register = (on, options) => {
  const reserved = RESERVED.filter(feature => options[feature] === true)
  const isControlOn = options.control === true
  const isApprovalsOn = options.approvals === true
  const endpoint = daemonEndpoint(options.daemonUrl)
  /** Calls waiting on the dashboard right now; the status line shows while any does. */
  const waiting = { count: 0 }
  /** The command loop; only ever set with `control` on. */
  let control: Control | undefined
  /** The forecast loop; undefined with `forecast` off, so none of it runs. */
  const forecaster: Forecaster | undefined = options.forecast === true ? { state: INITIAL_FORECAST, isResumed: false } : undefined
  /** The swarm; undefined with `swarm` off, so no store access, no swarm event and no send/receive hook. */
  const swarm: Swarm | undefined = options.swarm === true ? { turn: EMPTY_TURN, lock: Promise.resolve(), pending: new Map() } : undefined
  /** How a write another live session holds is met; the registry exists with `swarm` on alone, so `off` without it. */
  const claimConflicts: ClaimConflicts = swarm !== undefined ? claimConflictsOf(options.claimConflicts) : 'off'
  /** `claimConflicts` set without `swarm`: said once, at the first session.start. */
  const isClaimConflictsIdle = swarm === undefined && claimConflictsOf(options.claimConflicts) !== 'off'
  /** The permission mode per loop, read off the classic events; kept with `claimConflicts: ask` alone. */
  const modes: ModeBook = new Map()
  /** The plan band; undefined with `planBand` off, so no band hook, command, timer or plan request exists. */
  const planner: Planner | undefined = options.planBand === true ? newPlanner(endpoint.origin) : undefined
  const noted = new Set<string>()
  const link: Link = {
    url: endpoint.url,
    token: undefined,
    sessionId: '',
    cwd: '',
    start: undefined,
    isClearing: false,
    isOff: false,
    isFailing: false,
    isAuthWait: false,
    hasRetriedAuth: false,
    queue: [],
    stamps: {},
    timer: undefined,
    flushing: undefined,
    logged: new Set<string>(),
  }

  on('session.start', async ($, e, next) => {
    const result = await next(e)
    // also on `--resume`, which keeps the session id: a new run, so turn 1 is a new row
    if (forecaster !== undefined) {
      forecaster.isResumed = false
      await newRun($, forecaster)
    }
    for (const feature of reserved) {
      if (noted.has(feature)) continue
      noted.add(feature)
      log($, `${feature}: not implemented yet`)
    }
    if (isClaimConflictsIdle && !noted.has('claimConflicts')) {
      noted.add('claimConflicts')
      log($, 'claimConflicts needs swarm on: no write is held')
    }
    // the command answers with or without a token: it says what is missing
    if (planner !== undefined) await registerPlanCommand($)
    if (endpoint.isRefused && !noted.has('daemonUrl')) {
      // the configured value is not echoed: it may carry credentials
      noted.add('daemonUrl')
      log($, `daemonUrl refused: not a loopback http origin (127.0.0.1, [::1], localhost); using ${DEFAULT_DAEMON_URL}`)
    }
    try {
      link.token = await readToken($)
      if (link.token === undefined) {
        log($, 'no token, sensor off')
        return result
      }
      link.sessionId = await $.session.id()
      link.cwd = (await $.session.cwd()) || e.cwd
      link.start = { surface: e.surface, isInteractive: e.isInteractive }
      link.isClearing = false
      await push($, link, 'session.start', link.start)
      // one loop per session: a second session.start of the same session keeps it
      if (isControlOn && !link.isOff && (control === undefined || control.isStopped || control.sessionId !== link.sessionId)) {
        control = openControl($, link, endpoint.origin, control, swarm)
      }
      if (planner !== undefined) {
        startPlanTimer($, planner, link)
        void refreshPlan($, planner, link)
      }
    } catch {
      // the sensor stays off; the session goes on
    }
    return result
  })

  // turn.start is the main loop's alone (TurnCompleteFields.turnId: "a subagent's run raises no turn.start").
  on('turn.start', async ($, e, next) => {
    if (link.isClearing && isActive(link)) {
      try {
        await resync($, link)
        if (isControlOn && !link.isOff) control = openControl($, link, endpoint.origin, control, swarm)
        // the band follows the new session id: its plan is read again under it
        if (planner !== undefined) void refreshPlan($, planner, link)
      } catch {
        // the id is read again at the next turn.start
      }
    }
    if (control !== undefined) control.turnId = e.turnId
    if (planner !== undefined) {
      planner.turn = { turnId: e.turnId, edits: [], isClassified: false }
      startPlanTimer($, planner, link)
    }
    // the resync after an in-process resume: the counter starts over under a new run
    if (forecaster !== undefined && forecaster.isResumed) {
      forecaster.isResumed = false
      await newRun($, forecaster)
    }
    try {
      await push($, link, 'turn.start')
      flushAtTurn($, link)
    } catch {
      // never fail a turn over the sensor
    }
    // turn.start is the main loop's alone: a subagent's run raises none
    if (forecaster !== undefined) beginForecast($, link, forecaster, e.turnId, e.text)
    return next(e)
  })

  on('tool.call', async ($, e, next) => {
    if (isActive(link)) {
      try {
        const fields = e as unknown as Readonly<Record<string, unknown>>
        await push($, link, 'tool.call', {
          tool: String(e.tool),
          inputHash: await inputHash(toolInput(fields)),
          ...(e.agentId !== undefined ? { agentId: e.agentId } : {}),
        })
      } catch {
        // the call goes on unreported
      }
    }
    // With claimConflicts off the edit is claimed ahead of the call, as ever. On,
    // it is claimed once the call ran: a write the check held back (refused, or
    // declined at the dialog) must not leave a claim that holds the other session back.
    const claimsAfter = swarm !== undefined && claimConflicts !== 'off'
    if (swarm !== undefined && !claimsAfter && isActive(link) && !link.isClearing) {
      try {
        await claimEdit($, link, swarm, String(e.tool), e as unknown as Readonly<Record<string, unknown>>)
      } catch {
        // the edit goes on unclaimed
      }
    }
    const isMainLoop = e.agentId === undefined
    const isForecastTurn = forecaster !== undefined && isMainLoop && forecaster.state.turnId !== undefined
    // the drift check counts the main loop's edits alone
    const isPlanEdit = planner?.turn !== undefined && isMainLoop && isEditTool(String(e.tool))
    if (!isForecastTurn && !isPlanEdit && !claimsAfter) return next(e)
    const result = await next(e)
    const fields = e as unknown as Readonly<Record<string, unknown>>
    if (swarm !== undefined && claimsAfter && toolOutcome(result) === 'ok' && isActive(link) && !link.isClearing) {
      try {
        await claimEdit($, link, swarm, String(e.tool), fields)
      } catch {
        // the edit stands unclaimed
      }
    }
    try {
      if (forecaster !== undefined && isForecastTurn) {
        const facts = { tool: String(e.tool), agentId: e.agentId, fields }
        forecaster.state = noteToolCall(forecaster.state, facts, toolOutcome(result), link.cwd)
      }
      const path = isPlanEdit ? editedPath(String(e.tool), fields, link.cwd) : undefined
      if (planner !== undefined && path !== undefined && toolOutcome(result) !== 'denied') planner.turn = noteEdit(planner.turn, path)
    } catch {
      // the journal misses one call; the call's result stands
    }
    return result
  }).catch(($, e, next) => next(e))

  // One tool.check (the engine takes one unmatched hook per event) serves the
  // approvals channel and `claimConflicts`: the conflict verdict is reached
  // first, then the approvals channel sees it, so a write the conflict check
  // refuses never opens a dashboard approval (only an `ask` goes there).
  // Registered with either option on.
  if (isApprovalsOn || claimConflicts !== 'off') {
    on('tool.check', async ($, e, next) => {
      const below = await next(e)
      if (!isActive(link) || link.isClearing) return below
      let verdict = below
      if (claimConflicts !== 'off' && below.decision !== 'deny') {
        try {
          verdict = (await checkClaims($, link, claimConflicts, modes, { tool: String(e.tool), input: e.input, agentId: e.agentId })) ?? below
        } catch {
          // the registry could not be read: the verdict beneath stands
        }
      }
      if (!isApprovalsOn) return verdict
      return checkWithSwarmery($, gateOf($, link, endpoint.origin, waiting), e, verdict, next.signal)
    }).catch(($, e, next) => next(e))
  }

  // Registered after the sensor's tool.call, so the sensor sits above it and
  // still reports (and journals) an AskUserQuestion answered from the dashboard.
  if (isApprovalsOn) {
    on('tool.call', { tool: 'AskUserQuestion' }, async ($, e, next) => {
      if (!isActive(link) || link.isClearing) return next(e)
      const fields = e as unknown as Readonly<Record<string, unknown>>
      // an answer as the call's result, a dashboard deny as its refusal; anything else opens the native dialog
      const outcome = await answerFromSwarmery($, gateOf($, link, endpoint.origin, waiting), fields, next.signal)
      return outcome ?? next(e)
    }).catch(($, e, next) => next(e))
  }

  // tool.check carries no permission mode; these classic events do. Each only
  // records it and hands the event on unchanged. With `ask` alone: `deny`
  // refuses in every mode.
  if (swarm !== undefined && claimConflicts === 'ask') {
    const record = (e: unknown): void => {
      try {
        noteMode(modes, e)
      } catch {
        // an unread mode keeps the ask
      }
    }
    on('classic.SessionStart', async ($, e, next) => {
      record(e)
      return next(e)
    }).catch(($, e, next) => next(e))
    on('classic.UserPromptSubmit', async ($, e, next) => {
      record(e)
      return next(e)
    }).catch(($, e, next) => next(e))
    on('classic.PostToolUse', async ($, e, next) => {
      record(e)
      return next(e)
    }).catch(($, e, next) => next(e))
    on('classic.PostToolUseFailure', async ($, e, next) => {
      record(e)
      return next(e)
    }).catch(($, e, next) => next(e))
  }

  on('session.measure', async ($, e, next) => {
    try {
      const payload = measurePayload(e)
      if (payload !== undefined) await push($, link, 'session.measure', payload)
    } catch {
      // unreported
    }
    return next(e)
  })

  on('turn.complete', async ($, e, next) => {
    // A subagent's run ends in a turn.complete too; only the main loop's means the session waits.
    if (control !== undefined && e.agentId === undefined && control.turnId === e.turnId) control.turnId = undefined
    if (e.agentId !== undefined || !isActive(link)) return next(e)
    /** Lines shown beneath the answer: each feature that has one adds it here. */
    const beneath: string[] = []
    try {
      await push($, link, 'turn.complete', { durationMs: e.durationMs, stopReason: e.reason })
      if (forecaster !== undefined) await finishForecast($, link, forecaster, e.turnId)
      if (swarm !== undefined) beneath.push(...(await endSwarmTurn($, link, swarm)))
      const snapshot = measurePayload(await $.session.usage())
      if (snapshot !== undefined) await push($, link, 'session.measure', snapshot)
      flushAtTurn($, link)
    } catch {
      // unreported
    }
    const result = await next(e)
    if (planner !== undefined) {
      try {
        const drift = await checkDrift($, planner, link, e.turnId, e.answer)
        if (drift !== undefined) beneath.push(drift)
      } catch {
        // no line; the answer stands
      }
      void refreshPlan($, planner, link)
    }
    // each feature added its own line; they are joined once, beneath the answer
    return withBeneath(result, e.answer, beneath)
  })

  on('session.end', async ($, e, next) => {
    if (control !== undefined) stopControl($, control, undefined)
    if (planner !== undefined) {
      stopPlanTimer(planner)
      planner.turn = undefined
      // `/clear` or an in-process resume: the old session's plan is not the next one's
      if (e.reason === 'clear' || e.reason === 'resume') setView($, planner, undefined)
    }
    // a /clear goes on in this process with no session.start: the turn count starts over here;
    // an in-process resume starts it over at the next turn.start's resync
    if (forecaster !== undefined && e.reason === 'clear') await newRun($, forecaster)
    if (forecaster !== undefined && e.reason === 'resume') forecaster.isResumed = true
    if (!isActive(link)) return next(e)
    try {
      if (swarm !== undefined) await endSwarmSession($, link, swarm)
      await push($, link, 'session.end', { reason: e.reason })
      link.timer?.cancel()
      link.timer = undefined
      // bounded by its own timer: a flush in flight may have a 5 s request timeout
      await within($, flushForEnd($, link), END_FLUSH_TIMEOUT_MS)
    } catch {
      // best effort inside session.end's 1.5 s bound
    }
    // `/clear` or an in-process resume: the process goes on under another id with no
    // session.start (SessionEndInput.sessionId); the next turn.start picks it up
    if (e.reason === 'clear' || e.reason === 'resume') link.isClearing = true
    return next(e)
  })

  // Registered with `swarm` on alone: off, no message hook runs at all.
  if (swarm !== undefined) {
    // The main conversation's messages to other sessions or agents; a subagent's are left out.
    on('session.send', async ($, e, next) => {
      const result = await next(e)
      if (e.agentId !== undefined || !isActive(link)) return result
      try {
        await noteSend($, link, swarm, e, result)
      } catch {
        // unreported; the send stands
      }
      return result
    }).catch(($, e, next) => next(e))

    // A delivery to the main conversation; one for a subagent is left out. Passed on as received.
    on('session.receive', async ($, e, next) => {
      if (e.agentId === undefined) {
        try {
          await noteReceive($, link, e.text, e.origin)
        } catch {
          // unreported
        }
      }
      return next(e)
    }).catch(($, e, next) => next(e))
  }

  if (planner === undefined) return

  // The band draws from the cache alone: a render never fetches.
  on('ui.render', { component: 'AbovePrompt' }, async ($, e, next) => drawBand($, planner, link, e) ?? next(e))

  on('ui.render', { component: 'Pane', requestId: PLAN_PANE_ID }, async ($, e) => drawPane($, planner, e))

  // An interview-style question gets a card above the engine's own dialog, which still answers it.
  on('ui.render', { component: 'AskUserQuestion' }, async ($, e, next) => {
    const interviews = interviewsOf(e.props.questions)
    if (interviews.length === 0) return next(e)
    return drawInterview($, e, interviews, await next(e))
  })

  // `/plan [task-id]`: answered here, so no model turn runs.
  on('command.run', { command: 'plan' }, async ($, e) => runPlanCommand($, planner, link, e.args)).catch(() => ({
    text: 'swarmery-link: /plan failed; the band shows the cached phase',
  }))
}
