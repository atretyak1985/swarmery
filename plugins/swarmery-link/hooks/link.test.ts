import { expect, test } from 'claude-code/testing'
import { COMPLETE, SESSION_ID, CWD, START, TOKEN, TURN, sent, world } from './testkit'

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
  expect(w.logs.filter(l => l.includes('control: not implemented'))).toHaveLength(1)
  expect(w.logs.filter(l => l.includes('planBand: not implemented'))).toHaveLength(1)
  expect(w.logs.some(l => l.includes('swarm:') || l.includes('forecast:'))).toBe(false)
  // control on still registers no gate in 0.1
  expect((await $.tool.check({ tool: 'Bash', input: { command: 'ls' } })).decision).toBe('allow')
})

test('a failing daemon never surfaces as a hook error; the events wait for the next flush', async ($, on) => {
  const w = world(on, { tokens: [TOKEN], replies: ['throw', 500, 202] })
  await $.session.start(START)
  await $.turn.start(TURN)
  const r = await $.turn.complete(COMPLETE)
  expect(r.text).toBe('hi')
  expect(w.requests).toHaveLength(1)
  expect(w.logs.filter(l => l.includes('daemon unreachable'))).toHaveLength(1)
  // while failing, turn hooks stop waiting on the daemon; the 2 s timer retries
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

test('daemonUrl option is honoured', { options: { daemonUrl: 'http://127.0.0.1:8899/' } }, async ($, on) => {
  const w = world(on, { tokens: [TOKEN] })
  await $.session.start(START)
  await $.turn.start(TURN)
  expect(w.requests[0]?.url).toBe('http://127.0.0.1:8899/api/link/events')
})
