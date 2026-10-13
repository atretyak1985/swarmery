// swarmery-link 0.1: a sensor. It reports this session's live state to a local
// swarmery daemon over POST /api/link/events
// (tools/swarmery/docs/mods-link-protocol.md) and changes nothing: every hook
// passes its event on, a failure is logged and swallowed, and with no token
// file the whole mod stays silent for the session and makes no request.
//
// Every `$` call is spelled in this module, as the engine requires: `$` is
// handed only to the functions declared at the top of this file, and ./lib/*
// is pure (data in, data out).

import type { EngineInterface, Register, Timer } from 'claude-code'
import {
  AUTH_RETRY_MS,
  END_FLUSH_TIMEOUT_MS,
  FLUSH_EVERY_MS,
  REQUEST_TIMEOUT_MS,
  describeFailure,
  eventsUrl,
  linkRequest,
  outcome,
  parseToken,
} from './lib/client'
import { inputHash, toolInput } from './lib/hash'
import { type LinkEvent, type LinkKind, type Stamps, drop, enqueue, stamp, takeBatch } from './lib/queue'
import { measurePayload } from './lib/usage'

const NAME = 'swarmery-link'

/** Options reserved for later releases: each logs once when switched on, and hooks nothing else. */
const RESERVED = ['control', 'planBand', 'swarm', 'forecast'] as const

type Reply = { readonly status?: number; readonly error?: unknown }

/** The link's state for one load of the module (a reload starts it over). */
type Link = {
  readonly url: string
  token: string | undefined
  sessionId: string
  cwd: string
  /** Off for the rest of this load: 503, or a 401 that survived a re-read. */
  isOff: boolean
  /** The last request got no answer or a 5xx: turn hooks stop waiting on the daemon until one succeeds. */
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

/** One POST of `body`, raced against a `$.clock` timer: HttpInit has no timeout or signal. */
async function post($: EngineInterface, link: Link, body: string, timeoutMs: number): Promise<Reply> {
  let guard: Timer | undefined
  const timeout = new Promise<Reply>(resolve => {
    try {
      guard = $.clock.after(timeoutMs, () => resolve({ error: 'timeout' }))
    } catch {
      // no timer: the request alone decides
    }
  })
  const request = $.http.fetch(link.url, linkRequest(link.token ?? '', body)).then(
    (r): Reply => ({ status: r.status }),
    (error: unknown): Reply => ({ error }),
  )
  const reply = await Promise.race([request, timeout])
  guard?.cancel()
  return reply
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
  if (!isActive(link)) return
  const stamped = stamp(link.stamps, kind, await $.clock.now())
  link.stamps = stamped.stamps
  const event: LinkEvent = { sessionId: link.sessionId, cwd: link.cwd, kind, at: stamped.at, ...(payload !== undefined ? { payload } : {}) }
  link.queue = enqueue(link.queue, event)
  arm($, link)
}

/** At a turn boundary: send now while the daemon answers, else leave it to the timer. */
async function flushAtTurn($: EngineInterface, link: Link): Promise<void> {
  if (isActive(link) && !link.isFailing) await flush($, link, REQUEST_TIMEOUT_MS)
}

export const register: Register = (on, options) => {
  const reserved = RESERVED.filter(feature => options[feature] === true)
  const noted = new Set<string>()
  const link: Link = {
    url: eventsUrl(options.daemonUrl),
    token: undefined,
    sessionId: '',
    cwd: '',
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
    for (const feature of reserved) {
      if (noted.has(feature)) continue
      noted.add(feature)
      log($, `${feature}: not implemented in 0.1`)
    }
    try {
      link.token = await readToken($)
      if (link.token === undefined) {
        log($, 'no token, sensor off')
        return result
      }
      link.sessionId = await $.session.id()
      link.cwd = (await $.session.cwd()) || e.cwd
      await push($, link, 'session.start', { surface: e.surface, isInteractive: e.isInteractive })
    } catch {
      // the sensor stays off; the session goes on
    }
    return result
  })

  on('turn.start', async ($, e, next) => {
    try {
      await push($, link, 'turn.start')
      await flushAtTurn($, link)
    } catch {
      // never fail a turn over the sensor
    }
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
    return next(e)
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
    if (e.agentId !== undefined || !isActive(link)) return next(e)
    try {
      await push($, link, 'turn.complete', { durationMs: e.durationMs, stopReason: e.reason })
      const snapshot = measurePayload(await $.session.usage())
      if (snapshot !== undefined) await push($, link, 'session.measure', snapshot)
      await flushAtTurn($, link)
    } catch {
      // unreported
    }
    return next(e)
  })

  on('session.end', async ($, e, next) => {
    if (!isActive(link)) return next(e)
    try {
      await push($, link, 'session.end', { reason: e.reason })
      link.timer?.cancel()
      link.timer = undefined
      await flush($, link, END_FLUSH_TIMEOUT_MS)
    } catch {
      // best effort inside session.end's 1.5 s bound
    }
    return next(e)
  })
}
