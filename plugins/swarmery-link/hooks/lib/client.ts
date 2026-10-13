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

/** The events endpoint under the configured origin; the default origin when the value is unusable. */
export const eventsUrl = (daemonUrl: unknown): string => {
  const raw = typeof daemonUrl === 'string' ? daemonUrl.trim() : ''
  const base = /^https?:\/\/[^\s/]+/i.test(raw) ? raw.replace(/\/+$/, '') : DEFAULT_DAEMON_URL
  return `${base}${EVENTS_PATH}`
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
