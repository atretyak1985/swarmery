import { expect, test } from 'claude-code/testing'
import { COMPLETE, CWD, END, SESSION_ID, START, TOKEN, TURN, sent, track, world } from './testkit'

test('no token file: the sensor is off, one log line, no request', async ($, on) => {
  const w = world(on)
  await $.session.start(START)
  await $.turn.start(TURN)
  await $.tool.call({ tool: 'Bash', command: 'echo hi', description: 'say hi' })
  await $.session.measure({ context: { tokens: 1, window: 10, percent: 10 }, rateLimits: [], changed: ['context'] })
  await $.turn.complete(COMPLETE)
  await w.clock.advance(10_000)
  await $.session.end({ reason: 'other', sessionId: SESSION_ID, resume: { sessionId: SESSION_ID } as never })
  expect(w.requests).toHaveLength(0)
  expect(w.logs).toEqual(['swarmery-link: no token, sensor off'])
})

test('turn.complete sends one POST carrying turn.complete and a session.measure snapshot', async ($, on) => {
  const w = world(on, { tokens: [`${TOKEN}\n`] })
  await $.session.start(START)
  await $.turn.complete(COMPLETE)
  await w.clock.advance(0)
  expect(w.requests).toHaveLength(1)
  const [request] = w.requests
  expect(request?.url).toBe('http://127.0.0.1:7777/api/link/events')
  expect(request?.method).toBe('POST')
  expect(request?.headers.authorization).toBe(`Bearer ${TOKEN}`)
  const kinds = request?.body.events.map(e => e.kind)
  expect(kinds).toEqual(['session.start', 'turn.complete', 'session.measure'])
  const measure = request?.body.events.find(e => e.kind === 'session.measure')
  expect(measure).toMatchObject({ sessionId: SESSION_ID, cwd: CWD })
  expect(measure?.payload).toEqual({
    context: { tokens: 32108, window: 1000000, percent: 3 },
    rateLimits: [
      { kind: 'five_hour', percentUsed: 9, resetsAt: '2026-10-13T20:20:00.000Z' },
      { kind: 'seven_day', percentUsed: 48, resetsAt: '2026-10-17T14:00:00.000Z' },
    ],
    cost: { usd: 0.0043368899999999995 },
    changed: ['context', 'rateLimits', 'cost'],
  })
  const complete = request?.body.events.find(e => e.kind === 'turn.complete')
  expect(complete?.payload).toEqual({ durationMs: 1200, stopReason: 'answer' })
  expect(typeof complete?.at).toBe('string')
})

test('control off: no tool.check hook, the verdict beneath stands untouched', { options: { control: false } }, async ($, on) => {
  const w = world(on, { tokens: [TOKEN] })
  await $.session.start(START)
  const input = { tool: 'Bash', input: { command: 'rm -rf build' } } as const
  const verdict = await $.tool.check(input)
  expect(verdict.decision).toBe('allow')
  expect(w.checks).toHaveLength(1)
  expect(w.logs.some(l => l.includes('control'))).toBe(false)
})

test('reserved options switched on only log "not implemented", once each', { options: { control: true, planBand: true } }, async ($, on) => {
  const w = world(on, { tokens: [TOKEN] })
  await $.session.start(START)
  await $.session.start(START)
  expect(w.logs.filter(l => l.includes('planBand: not implemented'))).toHaveLength(1)
  expect(w.logs.some(l => l.includes('swarm:') || l.includes('forecast:'))).toBe(false)
  // control is implemented in 0.2 (control.test.ts), and still registers no gate
  expect(w.logs.some(l => l.includes('control: not implemented'))).toBe(false)
  expect((await $.tool.check({ tool: 'Bash', input: { command: 'ls' } })).decision).toBe('allow')
  await $.session.end(END)
})

test('a failing daemon never surfaces as a hook error; the events wait for the next flush', async ($, on) => {
  const w = world(on, { tokens: [TOKEN], replies: ['throw', 500, 202] })
  await $.session.start(START)
  await $.turn.start(TURN)
  const r = await $.turn.complete(COMPLETE)
  expect(r.text).toBe('hi')
  await w.clock.advance(0)
  expect(w.requests).toHaveLength(1)
  expect(w.logs.filter(l => l.includes('daemon unreachable'))).toHaveLength(1)
  // while failing, turn boundaries leave sending to the 2 s timer
  await w.clock.advance(2_000)
  expect(w.requests).toHaveLength(2)
  await w.clock.advance(2_000)
  expect(w.requests).toHaveLength(3)
  const kinds = sent([w.requests[2]!]).map(e => e.kind)
  expect(kinds).toEqual(['session.start', 'turn.start', 'turn.complete', 'session.measure'])
  await w.clock.advance(10_000)
  expect(w.requests).toHaveLength(3)
})

test('401: the token is re-read once and retried no sooner than 1 s later; a second 401 turns the link off', async ($, on) => {
  const w = world(on, { tokens: ['old-token', 'new-token'], replies: [401, 401] })
  await $.session.start(START)
  await $.turn.start(TURN)
  // the turn's flush runs after the hook returned: let it reach the 401 and the re-read
  await w.clock.advance(0)
  expect(w.requests).toHaveLength(1)
  expect(w.tokenReads()).toBe(2)
  await w.clock.advance(999)
  expect(w.requests).toHaveLength(1)
  await w.clock.advance(200)
  expect(w.requests).toHaveLength(2)
  expect(w.requests[1]?.headers.authorization).toBe('Bearer new-token')
  expect(w.tokenReads()).toBe(2)
  expect(w.logs.some(l => l.includes('link off for this session'))).toBe(true)
  await $.turn.complete(COMPLETE)
  await w.clock.advance(10_000)
  expect(w.requests).toHaveLength(2)
})

test('401 then a good token: the retry is served and the queue drains', async ($, on) => {
  const w = world(on, { tokens: ['old-token', 'new-token'], replies: [401, 202] })
  await $.session.start(START)
  await $.turn.start(TURN)
  await w.clock.advance(1_100)
  expect(w.requests).toHaveLength(2)
  expect(sent([w.requests[1]!]).map(e => e.kind)).toEqual(['session.start', 'turn.start'])
  await $.turn.complete(COMPLETE)
  await w.clock.advance(0)
  expect(w.requests).toHaveLength(3)
})

test('tool.call passes through and reports the tool and an input hash, never the input', async ($, on) => {
  const w = world(on, { tokens: [TOKEN] })
  await $.session.start(START)
  const secret = 'export API_TOKEN=do-not-send-me'
  const r = await $.tool.call({ tool: 'Bash', command: secret, description: 'set it' })
  expect(r).toMatchObject({ result: 'ran' })
  await $.session.end({ reason: 'other', sessionId: SESSION_ID, resume: { sessionId: SESSION_ID } as never })
  const events = sent(w.requests)
  const call = events.find(e => e.kind === 'tool.call')
  expect(call?.payload).toMatchObject({ tool: 'Bash' })
  const hash = (call?.payload as { inputHash?: unknown } | undefined)?.inputHash
  expect(typeof hash).toBe('string')
  expect(hash as string).toMatch(/^[0-9a-f]{16}$/)
  expect(JSON.stringify(w.requests.map(q => q.body))).not.toContain('do-not-send-me')
  expect(events.at(-1)).toMatchObject({ kind: 'session.end', payload: { reason: 'other' } })
})

test('same-kind events in one millisecond get distinct, increasing at values', async ($, on) => {
  const w = world(on, { tokens: [TOKEN] })
  await $.session.start(START)
  await $.tool.call({ tool: 'Bash', command: 'a', description: 'a' })
  await $.tool.call({ tool: 'Bash', command: 'b', description: 'b' })
  await $.tool.call({ tool: 'Bash', command: 'c', description: 'c' })
  await w.clock.advance(2_000)
  const ats = sent(w.requests)
    .filter(e => e.kind === 'tool.call')
    .map(e => e.at as string)
  expect(ats).toEqual(['2026-10-13T10:00:00.000Z', '2026-10-13T10:00:00.001Z', '2026-10-13T10:00:00.002Z'])
})

test('a subagent turn.complete is not reported as the session waiting', async ($, on) => {
  const w = world(on, { tokens: [TOKEN] })
  await $.session.start(START)
  await $.turn.complete({ ...COMPLETE, agentId: 'a-1' })
  await w.clock.advance(2_000)
  expect(sent(w.requests).map(e => e.kind)).toEqual(['session.start'])
})

test('a daemon that never answers never holds a turn: turn-boundary flushes are fire-and-forget', async ($, on) => {
  const w = world(on, { tokens: [TOKEN], replies: ['hang'] })
  await $.session.start(START)
  const start = track($.turn.start(TURN))
  await w.clock.advance(0)
  expect(start.isDone()).toBe(true)
  expect(w.requests).toHaveLength(1)
  const complete = track($.turn.complete(COMPLETE))
  await w.clock.advance(0)
  expect(complete.isDone()).toBe(true)
  // the hung request's own 5 s guard still ends it
  await w.clock.advance(10_000)
})

test('session.end waits at most 1.2 s even behind a hung in-flight flush', async ($, on) => {
  const w = world(on, { tokens: [TOKEN], replies: ['hang'] })
  await $.session.start(START)
  await w.clock.advance(2_000)
  expect(w.requests).toHaveLength(1)
  const end = track($.session.end(END))
  await w.clock.advance(1_200)
  expect(end.isDone()).toBe(true)
  await w.clock.advance(10_000)
})

test('session.end behind an in-flight turn flush still sends the session.end event', async ($, on) => {
  const w = world(on, { tokens: [TOKEN] })
  await $.session.start(START)
  await $.turn.complete(COMPLETE)
  await $.session.end(END)
  await w.clock.advance(0)
  expect(sent(w.requests).at(-1)).toMatchObject({ kind: 'session.end', payload: { reason: 'other' } })
})

// daemonUrl: only a loopback http origin may receive the bearer token.
const DEFAULT_EVENTS = 'http://127.0.0.1:7777/api/link/events'
const DAEMON_URLS: ReadonlyArray<readonly [string, string]> = [
  ['http://127.0.0.1:8899/', 'http://127.0.0.1:8899/api/link/events'],
  ['http://127.0.0.1', 'http://127.0.0.1/api/link/events'],
  ['http://localhost:7777', 'http://localhost:7777/api/link/events'],
  ['HTTP://LocalHost:7777', 'http://localhost:7777/api/link/events'],
  ['http://[::1]:7777/some/path?q=1', 'http://[::1]:7777/api/link/events'],
  ['http://daemon.example.com:7777', DEFAULT_EVENTS],
  ['https://daemon.example.com', DEFAULT_EVENTS],
  ['https://127.0.0.1:7777', DEFAULT_EVENTS],
  ['http://a@remote.example', DEFAULT_EVENTS],
  ['http://127.0.0.1@remote.example', DEFAULT_EVENTS],
  ['http://127.0.0.1:7777@remote.example', DEFAULT_EVENTS],
  ['http://127.0.0.1.remote.example:7777', DEFAULT_EVENTS],
  ['http://localhost.remote.example', DEFAULT_EVENTS],
  ['http://127.0.0.1\\@remote.example', DEFAULT_EVENTS],
  ['http://127.0.0.1:99999', DEFAULT_EVENTS],
  ['http://127.0.0.1:0', DEFAULT_EVENTS],
  ['not a url', DEFAULT_EVENTS],
]

for (const [daemonUrl, expected] of DAEMON_URLS) {
  test(`daemonUrl ${JSON.stringify(daemonUrl)} → ${expected}`, { options: { daemonUrl } }, async ($, on) => {
    const w = world(on, { tokens: [TOKEN] })
    await $.session.start(START)
    await $.session.start(START)
    await $.turn.start(TURN)
    await w.clock.advance(0)
    expect(w.requests[0]?.url).toBe(expected)
    const refusals = w.logs.filter(l => l.includes('daemonUrl refused'))
    expect(refusals).toHaveLength(expected === DEFAULT_EVENTS ? 1 : 0)
    // the refused value may carry credentials: it is never echoed
    expect(refusals.some(l => l.includes(daemonUrl))).toBe(false)
  })
}
