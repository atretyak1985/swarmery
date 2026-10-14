// The link client's decisions. Pure: builds the request and turns a response
// into what the queue does next. The `$.http.fetch` call itself is made in
// register.ts, where every `$` call is spelled.
// Contract: tools/swarmery/docs/mods-link-protocol.md.

export const DEFAULT_DAEMON_URL = 'http://127.0.0.1:7777'
export const EVENTS_PATH = '/api/link/events'

/** How long a request may run before the queue treats it as failed. */
export const REQUEST_TIMEOUT_MS = 5_000
/** session.end's whole wall-clock bound is 1.5 s; the final flush keeps inside it. */
export const END_FLUSH_TIMEOUT_MS = 1_200
/** The periodic flush. */
export const FLUSH_EVERY_MS = 2_000
/** After a 401, the one retry waits past the daemon's 1 s token re-read throttle. */
export const AUTH_RETRY_MS = 1_100

/**
 * A loopback http origin: scheme, one of the three loopback hosts, an optional
 * port, then nothing or a path/query/fragment (dropped). No userinfo, no other
 * host, no https: the bearer token goes nowhere else.
 */
const LOOPBACK_ORIGIN = /^http:\/\/(127\.0\.0\.1|\[::1\]|localhost)(?::(\d{1,5}))?(?:[/?#].*)?$/i

export type DaemonEndpoint = {
  /** The daemon's loopback origin, no path: the command channel's URLs hang off it. */
  readonly origin: string
  /** Where events are POSTed. */
  readonly url: string
  /** A daemonUrl was configured but is not a loopback http origin, so the default is used. */
  readonly isRefused: boolean
}

/** The events endpoint under the configured loopback origin; the default origin for anything else. */
export const daemonEndpoint = (daemonUrl: unknown): DaemonEndpoint => {
  const fallback: DaemonEndpoint = { origin: DEFAULT_DAEMON_URL, url: `${DEFAULT_DAEMON_URL}${EVENTS_PATH}`, isRefused: false }
  if (daemonUrl === undefined || daemonUrl === null) return fallback
  const raw = typeof daemonUrl === 'string' ? daemonUrl.trim() : undefined
  if (raw === '') return fallback
  const match = raw === undefined ? null : LOOPBACK_ORIGIN.exec(raw)
  const host = match?.[1]?.toLowerCase()
  const port = match?.[2] === undefined ? undefined : Number(match[2])
  if (host === undefined || (port !== undefined && (port < 1 || port > 65_535))) return { ...fallback, isRefused: true }
  const origin = `http://${host}${port === undefined ? '' : `:${port}`}`
  return { origin, url: `${origin}${EVENTS_PATH}`, isRefused: false }
}

export type LinkRequest = {
  readonly method: 'POST'
  readonly headers: Record<string, string>
  readonly body: string
}

export const linkRequest = (token: string, body: string): LinkRequest => ({
  method: 'POST',
  headers: { authorization: `Bearer ${token}`, 'content-type': 'application/json' },
  body,
})

/**
 * What a response means for the queue:
 * - `sent`: the batch was processed (202); drop it, rejected events included.
 * - `auth`: 401; re-read the token once and retry once, past the throttle.
 * - `off`: 503 link disabled; the link is off for this session.
 * - `discard`: a request the daemon will never take (400, 403, 413 and other
 *   4xx); drop the batch, log once.
 * - `retry`: 5xx, a timeout or no answer; keep the batch for the next flush.
 */
export type Outcome = 'sent' | 'auth' | 'off' | 'discard' | 'retry'

export const outcome = (status: number | undefined): Outcome => {
  if (status === undefined) return 'retry'
  if (status >= 200 && status < 300) return 'sent'
  if (status === 401) return 'auth'
  if (status === 503) return 'off'
  if (status >= 400 && status < 500) return 'discard'
  return 'retry'
}

/** The token file's text as a bearer credential, or undefined when it holds none. */
export const parseToken = (text: unknown): string | undefined => {
  if (typeof text !== 'string') return undefined
  const token = text.trim()
  return token === '' || /\s/.test(token) ? undefined : token
}

/** A one-line reason for the log, never the token or a request body. */
export const describeFailure = (status: number | undefined, error: unknown): string => {
  if (status !== undefined) return `HTTP ${status}`
  if (error === 'timeout') return `no answer in ${REQUEST_TIMEOUT_MS / 1000}s`
  const text = error instanceof Error ? error.message : String(error)
  return text.split('\n')[0]?.slice(0, 160) ?? 'unknown error'
}
