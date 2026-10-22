import { expect, test } from 'claude-code/testing'
import { COMPLETE, END, SESSION_ID, START, TOKEN, TURN, type PollReply, world } from './testkit'

const ON = { options: { control: true } } as const
const POLL_URL = `http://127.0.0.1:7777/api/link/commands?session=${SESSION_ID}&wait=25`
const ACK_URL = (id: number): string => `http://127.0.0.1:7777/api/link/commands/${id}/ack`

const stop = (id: number): PollReply => ({ command: { id, kind: 'stop', payload: {} } })
const prompt = (id: number, text: string, asUser?: boolean): PollReply => ({
  command: { id, kind: 'prompt', payload: asUser === undefined ? { text } : { text, asUser } },
})

/** The gaps between consecutive polls, in mock-clock milliseconds. */
const gaps = (times: readonly number[]): number[] => times.slice(1).map((t, i) => t - (times[i] ?? 0))

test('204: the next poll follows, a GET signed with the token on the session URL', ON, async ($, on) => {
  const w = world(on, { tokens: [TOKEN], polls: [204, 204, 'hang'] })
  await $.session.start(START)
  await w.clock.settle()
  expect(w.polls).toHaveLength(1)
  expect(w.polls[0]).toMatchObject({ url: POLL_URL, method: 'GET', signedWith: TOKEN })
  // an empty reply that came back at once waits out the 1 s floor, no longer
  await w.clock.advance(999)
  expect(w.polls).toHaveLength(1)
  await w.clock.advance(1)
  expect(w.polls).toHaveLength(2)
  await w.clock.advance(1_000)
  expect(w.polls).toHaveLength(3)
  expect(w.acks).toHaveLength(0)
  await $.session.end(END)
})

test('stop: turn.abort runs exactly once per commandId, a re-delivery is acked done without re-running', ON, async ($, on) => {
  const w = world(on, { tokens: [TOKEN], polls: [stop(7), stop(7), 'hang'] })
  await $.session.start(START)
  await $.turn.start(TURN)
  await w.clock.settle()
  expect(w.aborts).toEqual(['turn-1'])
  expect(w.acks).toEqual([
    { url: ACK_URL(7), body: { result: 'done' } },
    { url: ACK_URL(7), body: { result: 'done' } },
  ])
  expect(w.polls).toHaveLength(3)
  await $.session.end(END)
})

for (const responseSig of ['none', 'wrong'] as const) {
  test(`a command the daemon did not sign (${responseSig}) never runs: no abort, no submit, no ack`, ON, async ($, on) => {
    const keys = [TOKEN]
    const w = world(on, { tokens: keys, responseSig, polls: [stop(7), prompt(8, 'go on'), 'hang'] })
    await $.session.start(START)
    await $.turn.start(TURN)
    await w.clock.advance(10_000)
    expect(w.polls.length).toBeGreaterThanOrEqual(2)
    expect(w.aborts).toEqual([])
    expect(w.submits).toEqual([])
    expect(w.acks).toEqual([])
    await $.session.end(END)
  })
}

test('a lost ack does not run the command twice when the daemon delivers it again', ON, async ($, on) => {
  const w = world(on, { tokens: [TOKEN], polls: [stop(5), stop(5), 'hang'], ackReplies: ['throw', 204] })
  await $.session.start(START)
  await $.turn.start(TURN)
  await w.clock.settle()
  expect(w.aborts).toEqual(['turn-1'])
  expect(w.acks.map(a => a.body.result)).toEqual(['done', 'done'])
  await $.session.end(END)
})

test('stop with no turn running: nothing aborted, acked done', ON, async ($, on) => {
  const w = world(on, { tokens: [TOKEN], polls: [stop(3), 'hang'] })
  await $.session.start(START)
  await $.turn.start(TURN)
  await $.turn.complete(COMPLETE)
  await w.clock.settle()
  expect(w.aborts).toEqual([])
  expect(w.acks).toEqual([{ url: ACK_URL(3), body: { result: 'done', message: 'no turn running' } }])
  await $.session.end(END)
})

test('prompt while a turn runs: acked failed "turn in progress", submitted once the turn completed and it came again', ON, async ($, on) => {
  const w = world(on, { tokens: [TOKEN], polls: [prompt(8, 'continue'), 204, prompt(8, 'continue'), 'hang'] })
  await $.session.start(START)
  await $.turn.start(TURN)
  await w.clock.settle()
  expect(w.submits).toEqual([])
  expect(w.acks).toEqual([{ url: ACK_URL(8), body: { result: 'failed', message: 'turn in progress' } }])
  await $.turn.complete(COMPLETE)
  await w.clock.advance(1_000)
  expect(w.submits).toEqual([{ text: 'continue', asUser: false }])
  expect(w.acks.at(-1)).toEqual({ url: ACK_URL(8), body: { result: 'done' } })
  await $.session.end(END)
})

test('a prompt refused beneath because a turn is running is acked failed "turn in progress"', ON, async ($, on) => {
  const w = world(on, { tokens: [TOKEN], polls: [prompt(11, 'go on'), 'hang'], submitDrop: 'a turn is running' })
  await $.session.start(START)
  await w.clock.settle()
  expect(w.submits).toHaveLength(1)
  expect(w.acks).toEqual([{ url: ACK_URL(11), body: { result: 'failed', message: 'turn in progress' } }])
  await $.session.end(END)
})

test('any other refusal of the prompt is acked failed with a short, one-line message', ON, async ($, on) => {
  const w = world(on, { tokens: [TOKEN], polls: [prompt(12, 'go on'), 'hang'], submitDrop: 'quota exhausted\nstack…' })
  await $.session.start(START)
  await w.clock.settle()
  const ack = w.acks[0]
  expect(ack?.body.result).toBe('failed')
  expect(ack?.body.message).not.toBe('turn in progress')
  expect(ack?.body.message ?? '').toContain('quota exhausted')
  expect(ack?.body.message ?? '').not.toContain('\n')
  await $.session.end(END)
})

test('prompt: asUser defaults to false, true only when the payload says so', ON, async ($, on) => {
  const w = world(on, { tokens: [TOKEN], polls: [prompt(9, 'framed'), prompt(10, 'as me', true), prompt(13, 'not bool', 'yes' as never), 'hang'] })
  await $.session.start(START)
  await w.clock.settle()
  expect(w.submits).toEqual([
    { text: 'framed', asUser: false },
    { text: 'as me', asUser: true },
    { text: 'not bool', asUser: false },
  ])
  expect(w.acks.map(a => a.body)).toEqual([{ result: 'done' }, { result: 'done' }, { result: 'done' }])
  await $.session.end(END)
})

test('a command it cannot run is acked failed and nothing runs', ON, async ($, on) => {
  const w = world(on, {
    tokens: [TOKEN],
    polls: [{ command: { id: 20, kind: 'reboot', payload: {} } }, { command: { id: 21, kind: 'prompt', payload: { text: '  ' } } }, 'hang'],
  })
  await $.session.start(START)
  await $.turn.start(TURN)
  await w.clock.settle()
  expect(w.aborts).toEqual([])
  expect(w.submits).toEqual([])
  expect(w.acks).toEqual([
    { url: ACK_URL(20), body: { result: 'failed', message: 'unknown command kind: reboot' } },
    { url: ACK_URL(21), body: { result: 'failed', message: 'prompt without text' } },
  ])
  await $.session.end(END)
})

test('session.end stops the loop: no request after it, even with polls answering at once', ON, async ($, on) => {
  const w = world(on, { tokens: [TOKEN], polls: [204] })
  await $.session.start(START)
  await w.clock.settle()
  await w.clock.advance(2_500)
  expect(w.polls).toHaveLength(3)
  await $.session.end(END)
  await w.clock.advance(60_000)
  expect(w.polls).toHaveLength(3)
})

test('session.end stops a loop held in a long poll: the reply is never acted on', ON, async ($, on) => {
  const w = world(on, { tokens: [TOKEN] })
  await $.session.start(START)
  await w.clock.settle()
  expect(w.polls).toHaveLength(1)
  await $.session.end(END)
  await w.clock.advance(120_000)
  expect(w.polls).toHaveLength(1)
  expect(w.acks).toHaveLength(0)
})

for (const failure of ['throw', 500, 404] as const) {
  test(`a failing daemon (${failure}): polls back off 3 s, never more than one a second`, ON, async ($, on) => {
    const w = world(on, { tokens: [TOKEN], polls: [failure] })
    await $.session.start(START)
    await w.clock.settle()
    await w.clock.advance(30_000)
    const times = w.polls.map(p => p.at)
    expect(times.length).toBeGreaterThan(5)
    expect(times.length).toBeLessThanOrEqual(11)
    expect(gaps(times).every(g => g >= 3_000)).toBe(true)
    expect(w.shown.filter(l => l.includes('control: command poll failed'))).toHaveLength(1)
    await $.session.end(END)
  })
}

test('a daemon that answers 204 at once is polled at most once a second', ON, async ($, on) => {
  const w = world(on, { tokens: [TOKEN], polls: [204] })
  await $.session.start(START)
  await w.clock.settle()
  await w.clock.advance(10_000)
  const times = w.polls.map(p => p.at)
  expect(times.length).toBeLessThanOrEqual(11)
  expect(gaps(times).every(g => g >= 1_000)).toBe(true)
  await $.session.end(END)
})

test('a poll that never answers is given up at 28 s, then the next waits 3 s', ON, async ($, on) => {
  const w = world(on, { tokens: [TOKEN], polls: ['hang'] })
  await $.session.start(START)
  await w.clock.settle()
  await w.clock.advance(30_999)
  expect(w.polls).toHaveLength(1)
  await w.clock.advance(1)
  expect(w.polls.map(p => p.at - (w.polls[0]?.at ?? 0))).toEqual([0, 31_000])
  await $.session.end(END)
})

test('503: the loop stops for good and the link is off', ON, async ($, on) => {
  const w = world(on, { tokens: [TOKEN], polls: [503, 204] })
  await $.session.start(START)
  await w.clock.settle()
  await w.clock.advance(60_000)
  expect(w.polls).toHaveLength(1)
  expect(w.logs.some(l => l.includes('link off for this session'))).toBe(true)
  await $.session.end(END)
})

test('401: the token is re-read once and the poll retried with it', ON, async ($, on) => {
  const w = world(on, { tokens: ['old-token', 'new-token'], polls: [401, 204, 'hang'] })
  await $.session.start(START)
  await w.clock.settle()
  expect(w.polls).toHaveLength(1)
  expect(w.tokenReads()).toBe(2)
  await w.clock.advance(1_099)
  expect(w.polls).toHaveLength(1)
  await w.clock.advance(1)
  expect(w.polls[1]?.signedWith).toBe('new-token')
  await w.clock.advance(1_000)
  expect(w.polls).toHaveLength(3)
  await $.session.end(END)
})

test('401 twice: the loop stops after the one re-read', ON, async ($, on) => {
  const w = world(on, { tokens: ['old-token', 'new-token'], polls: [401] })
  await $.session.start(START)
  await w.clock.settle()
  await w.clock.advance(60_000)
  expect(w.polls).toHaveLength(2)
  expect(w.logs.some(l => l.includes('control off for this session'))).toBe(true)
  await $.session.end(END)
})

test('control off: no request to the command endpoints, nothing aborted or submitted', { options: { control: false } }, async ($, on) => {
  const w = world(on, { tokens: [TOKEN], polls: [stop(1), prompt(2, 'x')] })
  await $.session.start(START)
  await $.turn.start(TURN)
  await w.clock.settle()
  await w.clock.advance(60_000)
  expect(w.polls).toHaveLength(0)
  expect(w.acks).toHaveLength(0)
  expect(w.aborts).toHaveLength(0)
  expect(w.submits).toHaveLength(0)
  expect(w.requests.every(r => !r.url.includes('/api/link/commands'))).toBe(true)
  expect(w.logs.some(l => l.includes('control'))).toBe(false)
  await $.session.end(END)
})

test('control on but no token file: no polling', ON, async ($, on) => {
  const w = world(on, { polls: [stop(1)] })
  await $.session.start(START)
  await w.clock.settle()
  await w.clock.advance(60_000)
  expect(w.polls).toHaveLength(0)
  expect(w.logs).toEqual(['swarmery-link: no token, sensor off'])
})

test('a second session.start keeps one loop', ON, async ($, on) => {
  const w = world(on, { tokens: [TOKEN] })
  await $.session.start(START)
  await $.session.start(START)
  await w.clock.settle()
  expect(w.polls).toHaveLength(1)
  await $.session.end(END)
})

const CLEARED_ID = '0b6f1c2e-7d4a-4c1e-9a55-3f0e2b7c9d10'
const CLEAR = { reason: 'clear', sessionId: SESSION_ID, resume: { sessionId: SESSION_ID } as never } as const
const pollUrl = (sessionId: string): string => `http://127.0.0.1:7777/api/link/commands?session=${sessionId}&wait=25`

test('/clear: polling pauses until the next turn.start, then resumes under the new session id', ON, async ($, on) => {
  const w = world(on, { tokens: [TOKEN], polls: [204] })
  await $.session.start(START)
  await w.clock.settle()
  await $.session.end(CLEAR)
  const beforeTurn = w.polls.length
  await w.clock.advance(10_000)
  expect(w.polls).toHaveLength(beforeTurn)
  w.moveTo(CLEARED_ID)
  await $.turn.start({ text: 'after clear', turnId: 'turn-2' })
  await w.clock.advance(3_000)
  const after = w.polls.slice(beforeTurn)
  expect(after.length).toBeGreaterThan(1)
  expect(after.every(p => p.url === pollUrl(CLEARED_ID))).toBe(true)
  await $.session.end({ reason: 'other', sessionId: CLEARED_ID, resume: { sessionId: CLEARED_ID } as never })
})

test('/clear: a stop for the new session aborts the turn that started after the clear', ON, async ($, on) => {
  // the old session's one poll comes back empty; the new session's first carries a stop
  const w = world(on, { tokens: [TOKEN], polls: [204, stop(40), 'hang'] })
  await $.session.start(START)
  await w.clock.settle()
  expect(w.polls).toHaveLength(1)
  await $.session.end(CLEAR)
  w.moveTo(CLEARED_ID)
  await $.turn.start({ text: 'after clear', turnId: 'turn-2' })
  await w.clock.settle()
  expect(w.polls.map(p => p.url)).toEqual([POLL_URL, pollUrl(CLEARED_ID), pollUrl(CLEARED_ID)])
  expect(w.aborts).toEqual(['turn-2'])
  expect(w.acks).toEqual([{ url: ACK_URL(40), body: { result: 'done' } }])
  await $.session.end({ reason: 'other', sessionId: CLEARED_ID, resume: { sessionId: CLEARED_ID } as never })
})

test('/clear with an id that did not move: the loop restarts for the same id', ON, async ($, on) => {
  const w = world(on, { tokens: [TOKEN], polls: [204] })
  await $.session.start(START)
  await w.clock.settle()
  await $.session.end(CLEAR)
  const beforeTurn = w.polls.length
  await $.turn.start(TURN)
  await w.clock.advance(3_000)
  const after = w.polls.slice(beforeTurn)
  expect(after.length).toBeGreaterThan(1)
  expect(after.every(p => p.url === POLL_URL)).toBe(true)
  await $.session.end(END)
})

test('any other session.end is final: a later turn.start does not restart polling', ON, async ($, on) => {
  const w = world(on, { tokens: [TOKEN], polls: [204] })
  await $.session.start(START)
  await w.clock.settle()
  await $.session.end(END)
  const atEnd = w.polls.length
  w.moveTo(CLEARED_ID)
  await $.turn.start({ text: 'late', turnId: 'turn-2' })
  await w.clock.advance(30_000)
  expect(w.polls).toHaveLength(atEnd)
})

test('the command loop never holds a hook: turn hooks return while a poll is open', ON, async ($, on) => {
  const w = world(on, { tokens: [TOKEN] })
  await $.session.start(START)
  await w.clock.settle()
  const r = await $.turn.complete(COMPLETE)
  expect(r.text).toBe('hi')
  expect(w.polls).toHaveLength(1)
  await $.session.end(END)
})
