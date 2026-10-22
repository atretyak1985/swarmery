// Every request the link makes goes through here: signed with the token
// (never carrying it) and answered with whether the daemon's signature on the
// answer verifies. ./lib/sig.ts holds the pure parts; contract:
// tools/swarmery/docs/mods-link-protocol.md "Auth".

import type { EngineInterface } from 'claude-code'
import { NONCE_BYTES, SIG_HEADER, authorization, nonceOf, verifyResponse } from './lib/sig'

/** A link request without its credential: the signature is added here. */
export type LinkInit = {
  readonly method: 'GET' | 'POST' | 'DELETE'
  readonly headers?: Readonly<Record<string, string>>
  readonly body?: string
}

/** `$.http.fetch`'s answer, plus whether the daemon signed it. */
export type SignedResponse = {
  readonly status: number
  readonly ok: boolean
  readonly text: string
  readonly headers: Readonly<Record<string, string>>
  /** The answer carries the daemon's signature over this request's nonce, its status and its body. */
  readonly isVerified: boolean
}

const randomBytes = (): Uint8Array | undefined => {
  try {
    if (typeof crypto === 'undefined' || typeof crypto.getRandomValues !== 'function') return undefined
    return crypto.getRandomValues(new Uint8Array(NONCE_BYTES))
  } catch {
    return undefined
  }
}

async function nowMs($: EngineInterface): Promise<number> {
  try {
    return await $.clock.now()
  } catch {
    return Date.now()
  }
}

/**
 * Sends one link request signed with `key` (the token) and verifies the
 * answer's signature. Rejects as `$.http.fetch` does (a failed connection),
 * so callers keep their own `.then(ok, failed)` handling.
 */
export async function signedFetch($: EngineInterface, key: string, url: string, init: LinkInit): Promise<SignedResponse> {
  const now = await nowMs($)
  const ts = Math.floor(now / 1000)
  const nonce = nonceOf(randomBytes(), now)
  const body = init.body ?? ''
  const headers = { ...init.headers, authorization: authorization(key, init.method, url, ts, nonce, body) }
  const r = await $.http.fetch(url, { method: init.method, headers, ...(init.body !== undefined ? { body: init.body } : {}) })
  const text = typeof r.text === 'string' ? r.text : ''
  const received = r.headers ?? {}
  return {
    status: r.status,
    ok: r.ok,
    text,
    headers: received,
    isVerified: verifyResponse(key, nonce, r.status, text, headerOf(received, SIG_HEADER)),
  }
}

/** A response header by name, whatever its case (the engine documents lower-cased names). */
const headerOf = (headers: Readonly<Record<string, string>>, name: string): string | undefined => {
  const exact = headers[name]
  if (exact !== undefined) return exact
  const key = Object.keys(headers).find(k => k.toLowerCase() === name)
  return key === undefined ? undefined : headers[key]
}
