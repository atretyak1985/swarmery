// Signed link requests and answers. Pure: SHA-256, HMAC-SHA256 and the
// strings they cover. The daemon's side is tools/swarmery/internal/linksig;
// contract: tools/swarmery/docs/mods-link-protocol.md "Auth".
//
// A request carries `Authorization: Swarmery-HMAC ts=…,nonce=…,sig=…`, so the
// token itself never goes on the wire, and the mod acts on an answer only
// when its `x-swarmery-sig` verifies: a listener on the daemon's port that
// does not hold the token can neither learn it nor speak for the daemon.
//
// The runtime's `crypto.subtle` offers `digest` alone (no importKey/sign, the
// phase-6 probe), so HMAC is built here, over a vendored SHA-256 checked
// against FIPS 180-2 and RFC 4231 (sig.test.ts). Synchronous on purpose: a
// request is signed in the same tick it is built.

export const SCHEME = 'Swarmery-HMAC'
/** The answer's signature header, as `$.http.fetch` names it (lower-cased). */
export const SIG_HEADER = 'x-swarmery-sig'
/** Random bytes in a nonce (hex-encoded: 32 characters). */
export const NONCE_BYTES = 16

const REQUEST_TAG = 'swarmery-hmac-v1'
const RESPONSE_TAG = 'swarmery-hmac-v1-response'

const K = new Uint32Array([
  0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5, 0xd807aa98, 0x12835b01, 0x243185be,
  0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174, 0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa,
  0x5cb0a9dc, 0x76f988da, 0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967, 0x27b70a85,
  0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85, 0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3,
  0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070, 0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f,
  0x682e6ff3, 0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
])

const INITIAL = [0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19] as const

const rotr = (x: number, n: number): number => (x >>> n) | (x << (32 - n))
const at = (a: Uint32Array, i: number): number => a[i] ?? 0

/** SHA-256 of `data` (FIPS 180-4). */
export const sha256 = (data: Uint8Array): Uint8Array => {
  const blocks = Math.ceil((data.length + 9) / 64)
  const padded = new Uint8Array(blocks * 64)
  padded.set(data)
  padded[data.length] = 0x80
  const view = new DataView(padded.buffer)
  // the bit length, 64-bit big-endian: high word first (length / 2^29 = bits / 2^32)
  view.setUint32(padded.length - 8, Math.floor(data.length / 0x20000000))
  view.setUint32(padded.length - 4, (data.length * 8) >>> 0)
  const h = Uint32Array.from(INITIAL)
  const w = new Uint32Array(64)
  for (let off = 0; off < padded.length; off += 64) {
    for (let i = 0; i < 16; i++) w[i] = view.getUint32(off + i * 4)
    for (let i = 16; i < 64; i++) {
      const x = at(w, i - 15)
      const y = at(w, i - 2)
      const s0 = rotr(x, 7) ^ rotr(x, 18) ^ (x >>> 3)
      const s1 = rotr(y, 17) ^ rotr(y, 19) ^ (y >>> 10)
      w[i] = (at(w, i - 16) + s0 + at(w, i - 7) + s1) >>> 0
    }
    let a = at(h, 0)
    let b = at(h, 1)
    let c = at(h, 2)
    let d = at(h, 3)
    let e = at(h, 4)
    let f = at(h, 5)
    let g = at(h, 6)
    let k = at(h, 7)
    for (let i = 0; i < 64; i++) {
      const t1 = (k + (rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25)) + ((e & f) ^ (~e & g)) + at(K, i) + at(w, i)) >>> 0
      const t2 = ((rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22)) + ((a & b) ^ (a & c) ^ (b & c))) >>> 0
      k = g
      g = f
      f = e
      e = (d + t1) >>> 0
      d = c
      c = b
      b = a
      a = (t1 + t2) >>> 0
    }
    h[0] = (at(h, 0) + a) >>> 0
    h[1] = (at(h, 1) + b) >>> 0
    h[2] = (at(h, 2) + c) >>> 0
    h[3] = (at(h, 3) + d) >>> 0
    h[4] = (at(h, 4) + e) >>> 0
    h[5] = (at(h, 5) + f) >>> 0
    h[6] = (at(h, 6) + g) >>> 0
    h[7] = (at(h, 7) + k) >>> 0
  }
  const out = new Uint8Array(32)
  const outView = new DataView(out.buffer)
  for (let i = 0; i < 8; i++) outView.setUint32(i * 4, at(h, i))
  return out
}

const BLOCK = 64

/** HMAC-SHA256 (RFC 2104) of `message` under `key`. */
export const hmacSha256 = (key: Uint8Array, message: Uint8Array): Uint8Array => {
  const k = key.length > BLOCK ? sha256(key) : key
  const inner = new Uint8Array(BLOCK + message.length)
  const outer = new Uint8Array(BLOCK + 32)
  for (let i = 0; i < BLOCK; i++) {
    const byte = k[i] ?? 0
    inner[i] = byte ^ 0x36
    outer[i] = byte ^ 0x5c
  }
  inner.set(message, BLOCK)
  outer.set(sha256(inner), BLOCK)
  return sha256(outer)
}

export const toHex = (bytes: Uint8Array): string => Array.from(bytes, b => b.toString(16).padStart(2, '0')).join('')

const utf8 = (text: string): Uint8Array => new TextEncoder().encode(text)

/** hex(SHA-256) of `text` as UTF-8; the empty string for a request with no body. */
export const sha256Hex = (text: string): string => toHex(sha256(utf8(text)))

const hmacHex = (key: string, text: string): string => toHex(hmacSha256(utf8(key), utf8(text)))

export const requestString = (method: string, target: string, ts: number, nonce: string, bodySha256: string): string =>
  [REQUEST_TAG, method.toUpperCase(), target, String(ts), nonce, bodySha256].join('\n')

export const responseString = (nonce: string, status: number, bodySha256: string): string =>
  [RESPONSE_TAG, nonce, String(status), bodySha256].join('\n')

/** The request-target a signature covers: the URL's path and query, as on the request line. */
export const requestTarget = (url: string): string => {
  const u = new URL(url)
  return `${u.pathname}${u.search}`
}

/** The signature of one request (hex). */
export const signRequest = (key: string, method: string, target: string, ts: number, nonce: string, body: string): string =>
  hmacHex(key, requestString(method, target, ts, nonce, sha256Hex(body)))

/** The Authorization header value of one request: the key never appears in it. */
export const authorization = (key: string, method: string, url: string, ts: number, nonce: string, body: string): string =>
  `${SCHEME} ts=${ts},nonce=${nonce},sig=${signRequest(key, method, requestTarget(url), ts, nonce, body)}`

/** The daemon's signature of an answer (hex): what `x-swarmery-sig` must hold. */
export const signResponse = (key: string, nonce: string, status: number, body: string): string =>
  hmacHex(key, responseString(nonce, status, sha256Hex(body)))

/** Equal strings, compared without stopping at the first difference. */
const isSameText = (a: string, b: string): boolean => {
  if (a.length !== b.length) return false
  let diff = 0
  for (let i = 0; i < a.length; i++) diff |= a.charCodeAt(i) ^ b.charCodeAt(i)
  return diff === 0
}

/**
 * Whether `sig` is the daemon's signature of this answer to the request that
 * carried `nonce`. No key, no nonce or no signature never verifies.
 */
export const verifyResponse = (key: string, nonce: string, status: number, body: string, sig: unknown): boolean => {
  if (key === '' || nonce === '' || typeof sig !== 'string' || sig === '') return false
  return isSameText(signResponse(key, nonce, status, body), sig.trim().toLowerCase())
}

/** A nonce from `bytes` random bytes, or, where the runtime has no `crypto`, from the clock and a counter. */
let fallbackCount = 0
export const nonceOf = (bytes: Uint8Array | undefined, now: number): string => {
  if (bytes !== undefined && bytes.length >= NONCE_BYTES) return toHex(bytes.subarray(0, NONCE_BYTES))
  fallbackCount += 1
  return `${Math.floor(now).toString(16)}-${fallbackCount.toString(16)}-${Math.random().toString(16).slice(2)}`.slice(0, 64).padEnd(16, '0')
}
