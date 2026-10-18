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
// Every `$` call is spelled in this module, as the engine requires: `$` is
// handed only to the functions declared at the top of this file, and ./lib/*
// is pure (data in, data out).

import type { EngineInterface, PromptSubmitArgs, Register, Timer } from 'claude-code'
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
  remember,
  shortError,
  submitFailure,
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
import { measurePayload } from './lib/usage'

const NAME = 'swarmery-link'

/** Options reserved for later releases: each logs once when switched on, and hooks nothing else. */
const RESERVED = ['planBand', 'swarm'] as const

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
}

type PollReply = { readonly status?: number; readonly text?: string; readonly error?: unknown }

const newControl = (origin: string, sessionId: string, previous: Control | undefined): Control => {
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
  const action = decide(command, control.executed, control.turnId)
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
function openControl($: EngineInterface, link: Link, origin: string, previous: Control | undefined): Control {
  if (previous !== undefined) stopControl($, previous, undefined)
  const control = newControl(origin, link.sessionId, previous)
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

/** How a tool call ended, from what `next(e)` resolved to. */
const toolOutcome = (result: unknown): ToolOutcome => {
  const r = result as { readonly deny?: unknown; readonly isError?: unknown } | undefined
  if (r?.deny !== undefined) return 'denied'
  return r?.isError === true ? 'error' : 'ok'
}

export const register: Register = (on, options) => {
  const reserved = RESERVED.filter(feature => options[feature] === true)
  const isControlOn = options.control === true
  const endpoint = daemonEndpoint(options.daemonUrl)
  /** The command loop; only ever set with `control` on. */
  let control: Control | undefined
  /** The forecast loop; undefined with `forecast` off, so none of it runs. */
  const forecaster: Forecaster | undefined = options.forecast === true ? { state: INITIAL_FORECAST, isResumed: false } : undefined
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
      log($, `${feature}: not implemented in 0.3`)
    }
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
        control = openControl($, link, endpoint.origin, control)
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
        if (isControlOn && !link.isOff) control = openControl($, link, endpoint.origin, control)
      } catch {
        // the id is read again at the next turn.start
      }
    }
    if (control !== undefined) control.turnId = e.turnId
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
    if (forecaster === undefined || e.agentId !== undefined || forecaster.state.turnId === undefined) return next(e)
    const result = await next(e)
    try {
      const facts = { tool: String(e.tool), agentId: e.agentId, fields: e as unknown as Readonly<Record<string, unknown>> }
      forecaster.state = noteToolCall(forecaster.state, facts, toolOutcome(result), link.cwd)
    } catch {
      // the journal misses one call; the call's result stands
    }
    return result
  }).catch(($, e, next) => next(e))

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
    try {
      await push($, link, 'turn.complete', { durationMs: e.durationMs, stopReason: e.reason })
      if (forecaster !== undefined) await finishForecast($, link, forecaster, e.turnId)
      const snapshot = measurePayload(await $.session.usage())
      if (snapshot !== undefined) await push($, link, 'session.measure', snapshot)
      flushAtTurn($, link)
    } catch {
      // unreported
    }
    return next(e)
  })

  on('session.end', async ($, e, next) => {
    if (control !== undefined) stopControl($, control, undefined)
    // a /clear goes on in this process with no session.start: the turn count starts over here;
    // an in-process resume starts it over at the next turn.start's resync
    if (forecaster !== undefined && e.reason === 'clear') await newRun($, forecaster)
    if (forecaster !== undefined && e.reason === 'resume') forecaster.isResumed = true
    if (!isActive(link)) return next(e)
    try {
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
}
