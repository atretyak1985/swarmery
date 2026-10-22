import { expect, test } from 'claude-code/testing'
import { authorization, hmacSha256, nonceOf, requestTarget, sha256, sha256Hex, signRequest, signResponse, toHex, verifyResponse } from './lib/sig'

const bytes = (text: string): Uint8Array => new TextEncoder().encode(text)
const repeat = (byte: number, n: number): Uint8Array => new Uint8Array(n).fill(byte)

test('SHA-256: the FIPS 180-2 vectors, a multi-block input and UTF-8 text', () => {
  expect(sha256Hex('')).toBe('e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855')
  expect(sha256Hex('abc')).toBe('ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad')
  expect(sha256Hex('abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq')).toBe(
    '248d6a61d20638b8e5c026930c3e6039a33ce45964ff2167f6ecedd419db06c1',
  )
  expect(toHex(sha256(repeat(0x61, 1_000_000)))).toBe('cdc76e5c9914fb9281a1c7e284d73e67f1809a48a497200e046d39ccc7112cd0')
  expect(sha256Hex('héllo ✓')).toBe('5657cdef8a85a584e0e961e6f8247cf5d3f8ed21496ed6fdbcfd43a761e94245')
})

test('HMAC-SHA256: the RFC 4231 vectors (cases 1-4, 6, 7)', () => {
  const cases: ReadonlyArray<readonly [Uint8Array, Uint8Array, string]> = [
    [repeat(0x0b, 20), bytes('Hi There'), 'b0344c61d8db38535ca8afceaf0bf12b881dc200c9833da726e9376c2e32cff7'],
    [bytes('Jefe'), bytes('what do ya want for nothing?'), '5bdcc146bf60754e6a042426089575c75a003f089d2739839dec58b964ec3843'],
    [repeat(0xaa, 20), repeat(0xdd, 50), '773ea91e36800e46854db8ebd09181a72959098b3ef8c122d9635514ced565fe'],
    [Uint8Array.from({ length: 25 }, (_, i) => i + 1), repeat(0xcd, 50), '82558a389a443c0ea4cc819899f2083a85f0faa3e578f8077a2e3ff46729665b'],
    [repeat(0xaa, 131), bytes('Test Using Larger Than Block-Size Key - Hash Key First'), '60e431591ee0b67f0d8a26aacbf5b77f8e0bc6213728c5140546040f0ee37f54'],
    [
      repeat(0xaa, 131),
      bytes('This is a test using a larger than block-size key and a larger than block-size data. The key needs to be hashed before being used by the HMAC algorithm.'),
      '9b09ffa71b942fcb27635fbcd5b0e944bfdc63644f0713938a7f51535c3a35e2',
    ],
  ]
  for (const [key, data, want] of cases) expect(toHex(hmacSha256(key, data))).toBe(want)
})

// The same vector as TestSigCrossLanguageVector (tools/swarmery/internal/linksig).
const KEY = 'k'.repeat(64)
const NONCE = '0123456789abcdef0123456789abcdef'

test('the daemon\'s vector: request and response signatures match internal/linksig', () => {
  expect(signRequest(KEY, 'get', '/api/link/permission/7?session=s1&wait=25', 1_791_920_751, NONCE, '')).toBe(
    '182f66fc97d1e1100ad18eab89718f01571529d8e904efaec47c41c0208ebbff',
  )
  expect(signResponse(KEY, NONCE, 200, '{"decision":"allow"}')).toBe('e9b45a06929b2cf77c1cb31a157fb41cd083027126c29e4f28f3d1cd04056fcd')
})

test('the Authorization header names the scheme, ts and nonce, and never carries the key', () => {
  const header = authorization(KEY, 'GET', 'http://127.0.0.1:7777/api/link/permission/7?session=s1&wait=25', 1_791_920_751, NONCE, '')
  expect(header).toBe(`Swarmery-HMAC ts=1791920751,nonce=${NONCE},sig=182f66fc97d1e1100ad18eab89718f01571529d8e904efaec47c41c0208ebbff`)
  expect(header.includes(KEY)).toBe(false)
  expect(requestTarget('http://127.0.0.1:7777/api/link/plan?session=a%2Fb&cwd=%2Fw')).toBe('/api/link/plan?session=a%2Fb&cwd=%2Fw')
})

test('verifyResponse: only the daemon\'s signature of this answer to this request verifies', () => {
  const body = '{"decision":"allow"}'
  const sig = signResponse(KEY, NONCE, 200, body)
  expect(verifyResponse(KEY, NONCE, 200, body, sig)).toBe(true)
  expect(verifyResponse(KEY, NONCE, 200, body, sig.toUpperCase())).toBe(true)
  expect(verifyResponse('j'.repeat(64), NONCE, 200, body, sig)).toBe(false)
  expect(verifyResponse(KEY, 'f'.repeat(32), 200, body, sig)).toBe(false)
  expect(verifyResponse(KEY, NONCE, 202, body, sig)).toBe(false)
  expect(verifyResponse(KEY, NONCE, 200, '{"decision":"deny"}', sig)).toBe(false)
  expect(verifyResponse(KEY, NONCE, 200, body, undefined)).toBe(false)
  expect(verifyResponse(KEY, NONCE, 200, body, '')).toBe(false)
  expect(verifyResponse('', NONCE, 200, body, sig)).toBe(false)
})

test('nonceOf: 32 hex characters from random bytes; a clock-and-counter nonce of the daemon\'s alphabet without them', () => {
  expect(nonceOf(repeat(0xab, 16), 0)).toBe('ab'.repeat(16))
  const a = nonceOf(undefined, 1_791_920_751_000)
  const b = nonceOf(undefined, 1_791_920_751_000)
  expect(a).not.toBe(b)
  for (const n of [a, b]) {
    expect(n.length).toBeGreaterThanOrEqual(16)
    expect(/^[0-9A-Za-z_-]+$/.test(n)).toBe(true)
  }
})
