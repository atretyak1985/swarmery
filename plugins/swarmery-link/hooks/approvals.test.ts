import type { On } from 'claude-code'
import { type Engine, expect, test } from 'claude-code/testing'
import {
  APPROVED_REASON,
  DEADLINE_GRACE_MS,
  DENIED_REASON,
  LOCAL_NOTICE,
  WAITING_STATUS,
  checkResult,
  deadlineOf,
  pollAnswer,
  postAnswer,
  questionAnswer,
  questionOutcome,
  shouldAsk,
} from './lib/approvals'
import { COMPLETE, CWD, END, SESSION_ID, START, TOKEN, TURN, type PermissionReply, type World, sent, track, world } from './testkit'

const ON = { options: { approvals: true } } as const
const OFF = { options: { approvals: false } } as const
const NOW = Date.parse('2026-10-13T10:00:00.000Z')
const ASK = { verdict: { decision: 'ask' } } as const
const POST_URL = 'http://127.0.0.1:7777/api/link/permission'
const pollUrl = (id: number): string => `http://127.0.0.1:7777/api/link/permission/${id}?session=${SESSION_ID}&wait=25`
const cancelUrl = (id: number): string => `http://127.0.0.1:7777/api/link/permission/${id}?session=${SESSION_ID}`

/** A real call the engine asks about: it carries its tool_use_id. */
const CALL = { tool: 'Bash', input: { command: 'rm -rf build' }, tool_use_id: 'toolu_01' } as const

const QUESTIONS = [
  {
    question: 'Which color do you prefer?',
    header: 'Color',
    multiSelect: false,
    options: [
      { label: 'Red', description: 'warm' },
      { label: 'Blue', description: 'cool' },
    ],
  },
]
const QUESTION_CALL = { tool: 'AskUserQuestion', questions: QUESTIONS } as const

const decided = (body: Readonly<Record<string, unknown>>): PermissionReply => ({ status: 200, body: { id: 7, ...body } })
const pending = (id: number, expiresInMs: number): PermissionReply => ({
  status: 202,
  body: { id, status: 'pending', expiresAt: new Date(NOW + expiresInMs).toISOString(), pollMs: 25_000 },
})

const begin = async ($: Engine, on: On, w: World = {}): Promise<ReturnType<typeof world>> => {
  const recorded = world(on, { tokens: [TOKEN], now: NOW, ...w })
  await $.session.start(START)
  return recorded
}

test('approvals off: no permission request even when the verdict is ask; the verdict stands', OFF, async ($, on) => {
  const w = await begin($, on, { ...ASK, permissionReplies: [decided({ decision: 'allow' })] })
  const verdict = await $.tool.check(CALL)
  expect(verdict.decision).toBe('ask')
  expect(w.checks).toHaveLength(1)
  const r = await $.tool.call(QUESTION_CALL)
  expect(w.calls).toEqual(['AskUserQuestion'])
  expect(r.deny).toBeUndefined()
  await w.clock.advance(60_000)
  expect(w.permissionPosts).toHaveLength(0)
  expect(w.permissionPolls).toHaveLength(0)
  expect(w.statuses).toHaveLength(0)
  await $.session.end(END)
})

for (const decision of ['allow', 'deny'] as const) {
  test(`verdict ${decision}: returned untouched, nothing asked of the daemon`, ON, async ($, on) => {
    const w = await begin($, on, { verdict: { decision, reason: 'rule' }, permissionReplies: [decided({ decision: 'allow' })] })
    const verdict = await $.tool.check(CALL)
    expect(verdict).toMatchObject({ decision, reason: 'rule' })
    expect(w.permissionPosts).toHaveLength(0)
  })
}

test('a $.tool.check query (no tool_use_id) is never put to the daemon', ON, async ($, on) => {
  const w = await begin($, on, { ...ASK, permissionReplies: [decided({ decision: 'allow' })] })
  const verdict = await $.tool.check({ tool: 'Bash', input: { command: 'ls' } })
  expect(verdict.decision).toBe('ask')
  expect(w.permissionPosts).toHaveLength(0)
})

for (const tool of ['AskUserQuestion', 'ExitPlanMode'] as const) {
  test(`tool.check of ${tool} (the person decides) is never put to the daemon`, ON, async ($, on) => {
    const w = await begin($, on, { ...ASK, permissionReplies: [decided({ decision: 'allow' })] })
    const verdict = await $.tool.check({ tool, input: {}, tool_use_id: 'toolu_02' })
    expect(verdict.decision).toBe('ask')
    expect(w.permissionPosts).toHaveLength(0)
  })
}

test('no token file: the verdict stands, no request', ON, async ($, on) => {
  const w = world(on, { ...ASK, now: NOW, permissionReplies: [decided({ decision: 'allow' })] })
  await $.session.start(START)
  expect((await $.tool.check(CALL)).decision).toBe('ask')
  expect(w.permissionPosts).toHaveLength(0)
})

test('ask + the daemon down: the engine verdict ask is returned (fail open)', ON, async ($, on) => {
  const w = await begin($, on, { ...ASK, permissionReplies: ['throw'] })
  const verdict = await $.tool.check(CALL)
  expect(verdict.decision).toBe('ask')
  expect(w.permissionPosts).toHaveLength(1)
  expect(w.permissionPolls).toHaveLength(0)
})

for (const status of [429, 500, 503, 400] as const) {
  test(`ask + POST ${status}: the verdict stands`, ON, async ($, on) => {
    const w = await begin($, on, { ...ASK, permissionReplies: [status] })
    expect((await $.tool.check(CALL)).decision).toBe('ask')
    expect(w.permissionPosts).toHaveLength(1)
  })
}

test('ask + a POST that never answers: given up at 5 s, the verdict stands', ON, async ($, on) => {
  const w = await begin($, on, { ...ASK, permissionReplies: ['hang'] })
  const pendingCheck = $.tool.check(CALL)
  const call = track(pendingCheck)
  await w.clock.advance(4_999)
  expect(call.isDone()).toBe(false)
  await w.clock.advance(1)
  expect(call.isDone()).toBe(true)
  expect((await pendingCheck).decision).toBe('ask')
})

test('ask + POST 204 (excluded project): the verdict stands, no poll', ON, async ($, on) => {
  const w = await begin($, on, { ...ASK, permissionReplies: [204] })
  expect((await $.tool.check(CALL)).decision).toBe('ask')
  expect(w.permissionPolls).toHaveLength(0)
})

test('ask + POST allow by rule: allow, "approved in Swarmery"; the request carries the call and the bearer token', ON, async ($, on) => {
  const w = await begin($, on, { ...ASK, permissionReplies: [decided({ decision: 'allow', via: 'rule' })] })
  const verdict = await $.tool.check(CALL)
  expect(verdict).toMatchObject({ decision: 'allow', reason: APPROVED_REASON })
  expect(w.permissionPosts).toHaveLength(1)
  const [post] = w.permissionPosts
  expect(post?.url).toBe(POST_URL)
  expect(post?.method).toBe('POST')
  expect(post?.authorization).toBe(`Bearer ${TOKEN}`)
  expect(post?.body).toEqual({
    sessionId: SESSION_ID,
    cwd: CWD,
    toolName: 'Bash',
    toolInput: { command: 'rm -rf build' },
    toolUseId: 'toolu_01',
  })
  expect(w.statuses).toHaveLength(0)
})

test('a subagent\'s call carries its agentId', ON, async ($, on) => {
  const w = await begin($, on, { ...ASK, permissionReplies: [decided({ decision: 'allow' })] })
  await $.tool.check({ ...CALL, agentId: 'agent-9' })
  expect(w.permissionPosts[0]?.body).toMatchObject({ agentId: 'agent-9', toolUseId: 'toolu_01' })
})

test('ask + 202, then 204, 204, 200 allow: allow after 3 polls; the status line is set, then cleared', ON, async ($, on) => {
  const w = await begin($, on, {
    ...ASK,
    permissionReplies: [pending(41, 600_000)],
    permissionPollReplies: [204, 204, decided({ decision: 'allow' })],
  })
  const pendingCheck = $.tool.check(CALL)
  const call = track(pendingCheck)
  await w.clock.settle()
  expect(w.permissionPolls).toHaveLength(1)
  expect(w.statuses).toEqual([WAITING_STATUS])
  expect(call.isDone()).toBe(false)
  // an empty reply that came back at once waits out the 1 s floor
  await w.clock.advance(1_000)
  expect(w.permissionPolls).toHaveLength(2)
  await w.clock.advance(1_000)
  expect(w.permissionPolls).toHaveLength(3)
  expect(call.isDone()).toBe(true)
  expect(await pendingCheck).toMatchObject({ decision: 'allow', reason: APPROVED_REASON })
  expect(w.permissionPolls.map(p => p.url)).toEqual([pollUrl(41), pollUrl(41), pollUrl(41)])
  expect(w.permissionPolls.every(p => p.method === 'GET' && p.authorization === `Bearer ${TOKEN}`)).toBe(true)
  expect(w.statuses).toEqual([WAITING_STATUS, undefined])
})

test('200 deny: deny with the dashboard\'s message; without one, "denied in Swarmery"', ON, async ($, on) => {
  const w = await begin($, on, {
    ...ASK,
    permissionReplies: [decided({ decision: 'deny', message: 'not on main' }), decided({ decision: 'deny' })],
  })
  expect(await $.tool.check(CALL)).toMatchObject({ decision: 'deny', reason: 'not on main' })
  expect(await $.tool.check({ ...CALL, tool_use_id: 'toolu_03' })).toMatchObject({ decision: 'deny', reason: DENIED_REASON })
  expect(w.permissionPosts).toHaveLength(2)
})

test('a deny after polling: the poll\'s deny answers', ON, async ($, on) => {
  const w = await begin($, on, {
    ...ASK,
    permissionReplies: [pending(5, 600_000)],
    permissionPollReplies: [{ status: 200, body: { id: 5, decision: 'deny', message: 'too risky' } }],
  })
  expect(await $.tool.check(CALL)).toMatchObject({ decision: 'deny', reason: 'too risky' })
  expect(w.statuses).toEqual([WAITING_STATUS, undefined])
})

test('200 pass: the engine verdict stands', ON, async ($, on) => {
  await begin($, on, { ...ASK, permissionReplies: [decided({ decision: 'pass' })] })
  expect((await $.tool.check(CALL)).decision).toBe('ask')
})

test('200 local (production deploy): the verdict stands and the dialog says to confirm it here', ON, async ($, on) => {
  const w = await begin($, on, { ...ASK, permissionReplies: [decided({ decision: 'local' })] })
  expect((await $.tool.check(CALL)).decision).toBe('ask')
  expect(w.notices).toEqual([{ toolUseId: 'toolu_01', text: LOCAL_NOTICE }])
})

test('the deadline (expiresAt + 5 s) bounds a poll that never answers: the verdict stands', ON, async ($, on) => {
  const w = await begin($, on, { ...ASK, permissionReplies: [pending(9, 10_000)], permissionPollReplies: ['hang'] })
  const pendingCheck = $.tool.check(CALL)
  const call = track(pendingCheck)
  await w.clock.advance(14_999)
  expect(call.isDone()).toBe(false)
  await w.clock.advance(1)
  expect(call.isDone()).toBe(true)
  expect((await pendingCheck).decision).toBe('ask')
  expect(w.permissionPolls).toHaveLength(1)
  expect(w.statuses).toEqual([WAITING_STATUS, undefined])
  // the deadline is not an interrupt: the row stays pending for the shim
  await w.clock.advance(60_000)
  expect(w.permissionCancels).toHaveLength(0)
})

test('the deadline ends a wait on a daemon that keeps answering 204', ON, async ($, on) => {
  const w = await begin($, on, { ...ASK, permissionReplies: [pending(9, 10_000)], permissionPollReplies: [204] })
  const pendingCheck = $.tool.check(CALL)
  const call = track(pendingCheck)
  await w.clock.advance(20_000)
  expect(call.isDone()).toBe(true)
  expect((await pendingCheck).decision).toBe('ask')
  const polled = w.permissionPolls.length
  expect(polled).toBeGreaterThan(5)
  expect(polled).toBeLessThanOrEqual(16)
  await w.clock.advance(60_000)
  expect(w.permissionPolls).toHaveLength(polled)
  expect(w.permissionCancels).toHaveLength(0)
})

test('three failed polls in a row: the verdict stands', ON, async ($, on) => {
  const w = await begin($, on, { ...ASK, permissionReplies: [pending(9, 600_000)], permissionPollReplies: [500, 'throw', 502, decided({ decision: 'allow' })] })
  expect((await $.tool.check(CALL)).decision).toBe('ask')
  expect(w.permissionPolls).toHaveLength(3)
  await w.clock.advance(60_000)
  expect(w.permissionCancels).toHaveLength(0)
})

test('a failure between empty polls does not add up: 500, 204, 500, 500, allow → allow', ON, async ($, on) => {
  const w = await begin($, on, {
    ...ASK,
    permissionReplies: [pending(9, 600_000)],
    permissionPollReplies: [500, 204, 500, 500, decided({ decision: 'allow' })],
  })
  const pendingCheck = $.tool.check(CALL)
  await w.clock.advance(5_000)
  expect((await pendingCheck).decision).toBe('allow')
  expect(w.permissionPolls).toHaveLength(5)
})

test('a poll that never answers is given up at 28 s and counts as a failure', ON, async ($, on) => {
  const w = await begin($, on, { ...ASK, permissionReplies: [pending(9, 600_000)], permissionPollReplies: ['hang', 'hang', 'hang'] })
  const pendingCheck = $.tool.check(CALL)
  const call = track(pendingCheck)
  await w.clock.advance(83_999)
  expect(call.isDone()).toBe(false)
  await w.clock.advance(1)
  expect(call.isDone()).toBe(true)
  expect((await pendingCheck).decision).toBe('ask')
  expect(w.permissionPolls.map(p => p.at - NOW)).toEqual([0, 28_000, 56_000])
})

test('a poll\'s 404 (the request is gone): the verdict stands', ON, async ($, on) => {
  const w = await begin($, on, { ...ASK, permissionReplies: [pending(9, 600_000)], permissionPollReplies: [404, decided({ decision: 'allow' })] })
  expect((await $.tool.check(CALL)).decision).toBe('ask')
  expect(w.permissionPolls).toHaveLength(1)
  expect(w.permissionCancels).toHaveLength(0)
})

test('401: the token is re-read once and the request retried with it', ON, async ($, on) => {
  const w = await begin($, on, { ...ASK, tokens: ['old-token', 'new-token'], permissionReplies: [401, decided({ decision: 'allow' })] })
  const pendingCheck = $.tool.check(CALL)
  await w.clock.advance(1_100)
  expect((await pendingCheck).decision).toBe('allow')
  expect(w.tokenReads()).toBe(2)
  expect(w.permissionPosts.map(p => p.authorization)).toEqual(['Bearer old-token', 'Bearer new-token'])
})

test('401 twice: the verdict stands after the one re-read', ON, async ($, on) => {
  const w = await begin($, on, { ...ASK, tokens: ['old-token', 'new-token'], permissionReplies: [401, 401, decided({ decision: 'allow' })] })
  const pendingCheck = $.tool.check(CALL)
  await w.clock.advance(1_100)
  expect((await pendingCheck).decision).toBe('ask')
  expect(w.tokenReads()).toBe(2)
  expect(w.permissionPosts).toHaveLength(2)
})

test('a poll\'s 401 after the POST\'s: no second re-read, the verdict stands', ON, async ($, on) => {
  const w = await begin($, on, {
    ...ASK,
    tokens: ['old-token', 'new-token'],
    permissionReplies: [401, pending(9, 600_000)],
    permissionPollReplies: [401, decided({ decision: 'allow' })],
  })
  const pendingCheck = $.tool.check(CALL)
  await w.clock.advance(1_100)
  expect((await pendingCheck).decision).toBe('ask')
  expect(w.tokenReads()).toBe(2)
  expect(w.permissionPolls).toHaveLength(1)
  expect(w.permissionCancels).toHaveLength(0)
})

/** A hook above the mod that settles tool.check itself 3 s in, abandoning (aborting) the mod's dispatch. */
const INTERRUPTED = {
  ...ON,
  plugins: [
    {
      name: 'interrupter',
      tier: 'prepend' as const,
      register(on: On) {
        on('tool.check', async ($, e, next) => {
          next(e).catch(() => undefined)
          await $.clock.sleep(3_000)
          return { decision: 'deny' as const, reason: 'interrupted above' }
        })
      },
    },
  ],
}

test(
  'the person interrupts (next.signal aborts): the wait ends at once, nothing more is polled, the status line is cleared, one DELETE gives the request up',
  INTERRUPTED,
  async ($, on) => {
    const w = await begin($, on, { ...ASK, permissionReplies: [pending(9, 600_000)], permissionPollReplies: ['hang'] })
    const pendingCheck = $.tool.check(CALL)
    await w.clock.settle()
    expect(w.permissionPolls).toHaveLength(1)
    expect(w.statuses).toEqual([WAITING_STATUS])
    expect(w.permissionCancels).toHaveLength(0)
    await w.clock.advance(3_000)
    expect((await pendingCheck).decision).toBe('deny')
    await w.clock.settle()
    expect(w.statuses).toEqual([WAITING_STATUS, undefined])
    expect(w.permissionCancels).toHaveLength(1)
    const [cancel] = w.permissionCancels
    expect(cancel?.url).toBe(cancelUrl(9))
    expect(cancel?.method).toBe('DELETE')
    expect(cancel?.authorization).toBe(`Bearer ${TOKEN}`)
    expect(cancel?.at).toBe(NOW + 3_000)
    await w.clock.advance(120_000)
    expect(w.permissionPolls).toHaveLength(1)
    expect(w.permissionCancels).toHaveLength(1)
  },
)

test('a DELETE that never answers does not hold the hook\'s return', INTERRUPTED, async ($, on) => {
  const w = await begin($, on, {
    ...ASK,
    permissionReplies: [pending(9, 600_000)],
    permissionPollReplies: ['hang'],
    permissionCancelReplies: ['hang'],
  })
  const pendingCheck = $.tool.check(CALL)
  const call = track(pendingCheck)
  await w.clock.advance(3_000)
  expect(call.isDone()).toBe(true)
  expect((await pendingCheck).decision).toBe('deny')
  expect(w.permissionCancels).toHaveLength(1)
  await w.clock.advance(120_000)
  expect(w.permissionCancels).toHaveLength(1)
})

test('two calls waiting at once share one status line, cleared when the last ends', ON, async ($, on) => {
  const w = await begin($, on, {
    ...ASK,
    permissionReplies: [pending(1, 600_000), pending(2, 600_000)],
    permissionPollReplies: [decided({ decision: 'allow' }), 'hang'],
  })
  const first = $.tool.check(CALL)
  await w.clock.settle()
  expect((await first).decision).toBe('allow')
  expect(w.statuses).toEqual([WAITING_STATUS, undefined])
  const second = track($.tool.check({ ...CALL, tool_use_id: 'toolu_04' }))
  await w.clock.settle()
  expect(second.isDone()).toBe(false)
  expect(w.statuses).toEqual([WAITING_STATUS, undefined, WAITING_STATUS])
  await w.clock.advance(28_000 * 3)
  expect(second.isDone()).toBe(true)
  expect(w.statuses.at(-1)).toBeUndefined()
})

test('AskUserQuestion answered in Swarmery: { result: { questions, answers } }, the dialog (next) never reached', ON, async ($, on) => {
  const answers = { 'Which color do you prefer?': 'Blue' }
  const w = await begin($, on, {
    permissionReplies: [pending(12, 600_000)],
    permissionPollReplies: [204, decided({ decision: 'allow', answer: { questions: QUESTIONS, answers } })],
  })
  const pendingCall = $.tool.call(QUESTION_CALL)
  await w.clock.advance(1_000)
  const r = await pendingCall
  expect(r.deny).toBeUndefined()
  expect(r.result).toEqual({ questions: QUESTIONS, answers })
  expect(w.calls).toEqual([])
  const [post] = w.permissionPosts
  expect(post?.body).toMatchObject({ sessionId: SESSION_ID, cwd: CWD, toolName: 'AskUserQuestion', toolInput: { questions: QUESTIONS } })
  expect(typeof post?.body?.toolUseId).toBe('string')
  expect(w.statuses).toEqual([WAITING_STATUS, undefined])
})

test('AskUserQuestion: an answer without the questions is given the questions as asked', ON, async ($, on) => {
  const answers = { 'Which color do you prefer?': 'Red' }
  await begin($, on, { permissionReplies: [decided({ decision: 'allow', answer: { answers } })] })
  const r = await $.tool.call(QUESTION_CALL)
  expect(r.result).toEqual({ questions: QUESTIONS, answers })
})

test('AskUserQuestion multi-select answered in Swarmery: the labels the dashboard lists are comma-joined (M4)', ON, async ($, on) => {
  const multi = [{ ...QUESTIONS[0], question: 'Which colors?', multiSelect: true }]
  await begin($, on, {
    permissionReplies: [decided({ decision: 'allow', answer: { questions: multi, answers: { 'Which colors?': ['Red', 'Blue'] } } })],
  })
  const r = await $.tool.call({ tool: 'AskUserQuestion', questions: multi })
  expect(r.result).toEqual({ questions: multi, answers: { 'Which colors?': 'Red, Blue' } })
})

test('AskUserQuestion + deny: the call is refused with the dashboard\'s message, next(e) never runs (no native dialog, no shim)', ON, async ($, on) => {
  const w = await begin($, on, {
    permissionReplies: [pending(6, 600_000), decided({ decision: 'deny' })],
    permissionPollReplies: [decided({ decision: 'deny', message: 'ask me later' })],
  })
  const r = await $.tool.call(QUESTION_CALL)
  expect(r.deny).toBe('ask me later')
  expect(r.result).toBeUndefined()
  // without a message: "denied in Swarmery"
  const again = await $.tool.call(QUESTION_CALL)
  expect(again.deny).toBe(DENIED_REASON)
  expect(again.result).toBeUndefined()
  expect(w.calls).toEqual([])
  expect(w.permissionPosts).toHaveLength(2)
  expect(w.permissionCancels).toHaveLength(0)
  expect(w.statuses).toEqual([WAITING_STATUS, undefined])
})

const fallbacks: ReadonlyArray<readonly [string, Partial<World>, number]> = [
  ['pass', { permissionReplies: [decided({ decision: 'pass' })] }, 0],
  ['an allow with no answer', { permissionReplies: [decided({ decision: 'allow' })] }, 0],
  ['an allow with no answer after polling', { permissionReplies: [pending(4, 600_000)], permissionPollReplies: [decided({ decision: 'allow' })] }, 0],
  ['local', { permissionReplies: [decided({ decision: 'local' })] }, 0],
  ['204 (excluded)', { permissionReplies: [204] }, 0],
  ['the daemon down', { permissionReplies: ['throw'] }, 0],
  ['a POST that never answers', { permissionReplies: ['hang'] }, 5_000],
  ['the deadline', { permissionReplies: [pending(3, 10_000)], permissionPollReplies: ['hang'] }, 15_000],
]

for (const [name, replies, wait] of fallbacks) {
  test(`AskUserQuestion + ${name}: next(e) runs, the native dialog takes over`, ON, async ($, on) => {
    const w = await begin($, on, replies)
    const pendingCall = $.tool.call(QUESTION_CALL)
    await w.clock.advance(wait)
    const r = await pendingCall
    expect(r.result).toBe('ran')
    expect(w.calls).toEqual(['AskUserQuestion'])
    expect(w.permissionPosts).toHaveLength(1)
    expect(w.permissionCancels).toHaveLength(0)
    expect(w.statuses.at(-1)).toBeUndefined()
  })
}

test('AskUserQuestion answered in Swarmery is still reported and journaled by the sensor and the forecast', { options: { approvals: true, forecast: true } }, async ($, on) => {
  const answers = { 'Which color do you prefer?': 'Blue' }
  const w = await begin($, on, { permissionReplies: [decided({ decision: 'allow', answer: { questions: QUESTIONS, answers } })] })
  await $.turn.start(TURN)
  await w.clock.advance(0)
  const r = await $.tool.call(QUESTION_CALL)
  expect(r.result).toEqual({ questions: QUESTIONS, answers })
  await $.turn.complete(COMPLETE)
  await w.clock.advance(0)
  const events = sent(w.requests)
  const call = events.find(e => e.kind === 'tool.call')
  expect((call?.payload as Readonly<Record<string, unknown>> | undefined)?.tool).toBe('AskUserQuestion')
  const actual = events.find(e => e.kind === 'turn.actual')
  expect((actual?.payload as Readonly<Record<string, unknown>> | undefined)?.steps).toBe(1)
  expect(w.calls).toEqual([])
  await $.session.end(END)
})

test('shouldAsk: only an ask, on a real call, for a tool the person does not decide, under no ask/deny ceiling', () => {
  const real = { tool: 'Bash', tool_use_id: 'toolu_1' }
  expect(shouldAsk({ decision: 'ask' }, real)).toBe(true)
  expect(shouldAsk({ decision: 'allow' }, real)).toBe(false)
  expect(shouldAsk({ decision: 'deny' }, real)).toBe(false)
  expect(shouldAsk({ decision: 'ask' }, { tool: 'Bash' })).toBe(false)
  expect(shouldAsk({ decision: 'ask' }, { tool: 'ExitPlanMode', tool_use_id: 'toolu_1' })).toBe(false)
  expect(shouldAsk({ decision: 'ask' }, { ...real, ceiling: 'ask' })).toBe(false)
  expect(shouldAsk({ decision: 'ask' }, { ...real, ceiling: 'allow' })).toBe(true)
})

test('replies: what each status means; an unreadable 200 or a 202 without an id is a failure', () => {
  const now = NOW
  expect(postAnswer({ status: 200, text: '{"id":1,"decision":"allow","via":"rule"}' }, now)).toEqual({ kind: 'allow' })
  expect(postAnswer({ status: 200, text: 'not json' }, now)).toEqual({ kind: 'fail' })
  expect(postAnswer({ status: 202, text: '{"status":"pending"}' }, now)).toEqual({ kind: 'fail' })
  expect(postAnswer({ status: 202, text: '{"id":"12","expiresAt":"2026-10-13T10:10:00.000Z"}' }, now)).toEqual({
    kind: 'pending',
    id: '12',
    deadline: Date.parse('2026-10-13T10:10:00.000Z') + DEADLINE_GRACE_MS,
  })
  expect(postAnswer({ status: 204 }, now)).toEqual({ kind: 'skip' })
  expect(postAnswer({ status: 401 }, now)).toEqual({ kind: 'auth' })
  expect(postAnswer({ error: 'timeout' }, now)).toEqual({ kind: 'fail' })
  expect(pollAnswer({ status: 204 })).toEqual({ kind: 'wait' })
  expect(pollAnswer({ status: 404 })).toEqual({ kind: 'skip' })
  expect(pollAnswer({ status: 503 })).toEqual({ kind: 'fail' })
  expect(deadlineOf('garbage', now)).toBe(now + 600_000 + DEADLINE_GRACE_MS)
})

test('checkResult never invents an ask or a deny of its own', () => {
  const verdict = { decision: 'ask', reason: 'Bash(rm:*)' } as const
  for (const kind of ['pass', 'local', 'skip', 'wait', 'auth', 'fail'] as const) {
    expect(checkResult({ kind }, verdict)).toBe(verdict)
  }
  expect(checkResult({ kind: 'deny', message: '' }, verdict)).toEqual({ decision: 'deny', reason: DENIED_REASON })
  expect(questionAnswer({ kind: 'allow', answer: { questions: [] } }, [])).toBeUndefined()
  expect(questionAnswer({ kind: 'deny', message: 'x' }, [])).toBeUndefined()
  expect(questionAnswer({ kind: 'allow', answer: { answers: { a: ['x', 'y'], b: 'z' } } }, [])).toEqual({
    questions: [],
    answers: { a: 'x, y', b: 'z' },
  })
})

test('questionOutcome: an answer is the result, a deny the refusal, anything else no outcome', () => {
  const answers = { 'Which color do you prefer?': 'Red' }
  expect(questionOutcome({ kind: 'allow', answer: { questions: QUESTIONS, answers } }, QUESTIONS)).toEqual({ result: { questions: QUESTIONS, answers } })
  expect(questionOutcome({ kind: 'deny', message: 'no' }, QUESTIONS)).toEqual({ deny: 'no' })
  expect(questionOutcome({ kind: 'deny', message: '' }, QUESTIONS)).toEqual({ deny: DENIED_REASON })
  for (const kind of ['pass', 'local', 'skip', 'wait', 'auth', 'fail'] as const) {
    expect(questionOutcome({ kind }, QUESTIONS)).toBeUndefined()
  }
  expect(questionOutcome({ kind: 'allow' }, QUESTIONS)).toBeUndefined()
})
