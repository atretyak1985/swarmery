import type { On } from 'claude-code'
import { type Engine, expect, test } from 'claude-code/testing'
import {
  FORECAST_MAX_TOKENS,
  INITIAL_FORECAST,
  MAX_FILES,
  NO_CLAIMS,
  PROMPT_TEXT_LIMIT,
  RUN_ID_RANDOM,
  forecastRequest,
  noteToolCall,
  parseForecast,
  resetForecast,
  runIdOf,
  startTurn,
} from './lib/forecast'
import { COMPLETE, CWD, END, SESSION_ID, START, TOKEN, type World, sent, track, world } from './testkit'

const ON = { options: { forecast: true } } as const

type Events = ReadonlyArray<Readonly<Record<string, unknown>>>

/** The payloads of `kind`, runId left out (it carries a random part): see runsOf. */
const ofKind = (events: Events, kind: string): ReadonlyArray<Readonly<Record<string, unknown>>> =>
  events
    .filter(e => e.kind === kind)
    .map(e => {
      const { runId: _runId, ...rest } = (e.payload ?? {}) as Readonly<Record<string, unknown>>
      return rest
    })

/** The runId of every `kind` payload, in order. */
const runsOf = (events: Events, kind: string): readonly unknown[] =>
  events.filter(e => e.kind === kind).map(e => ((e.payload ?? {}) as Readonly<Record<string, unknown>>).runId)

/** The daemon's runId shape (internal/turnforecast). */
const RUN_ID = /^[A-Za-z0-9._:-]{1,64}$/

/** One main-loop turn: start, let the detached forecast run, the tool calls, complete. */
const turn = async (
  $: Engine,
  w: ReturnType<typeof world>,
  turnId: string,
  text: string,
  calls: ReadonlyArray<Parameters<Engine['tool']['call']>[0]> = [],
): Promise<void> => {
  await $.turn.start({ text, turnId })
  await w.clock.advance(0)
  for (const call of calls) await $.tool.call(call)
  await $.turn.complete({ ...COMPLETE, turnId })
  await w.clock.advance(0)
}

const begin = async ($: Engine, on: On, w: World = {}): Promise<ReturnType<typeof world>> => {
  const recorded = world(on, { tokens: [TOKEN], ...w })
  await $.session.start(START)
  return recorded
}

test('forecast off: no model call, no turn.forecast or turn.actual', async ($, on) => {
  const w = await begin($, on)
  await turn($, w, 'turn-1', 'fix the bug in src/a.ts', [{ tool: 'Edit', file_path: `${CWD}/src/a.ts`, old_string: 'a', new_string: 'b' }])
  await w.clock.advance(5_000)
  expect(w.modelCalls).toHaveLength(0)
  const kinds = sent(w.requests).map(e => e.kind)
  expect(kinds).toContain('turn.complete')
  expect(kinds).not.toContain('turn.forecast')
  expect(kinds).not.toContain('turn.actual')
  expect(w.logs.some(l => l.includes('forecast'))).toBe(false)
})

test('one haiku model.complete per user turn, maxTokens ≤ 300; forecast and actual carry turnSeq 1, 2', ON, async ($, on) => {
  const w = await begin($, on)
  await turn($, w, 'turn-1', 'fix the bug in src/a.ts', [{ tool: 'Edit', file_path: `${CWD}/src/a.ts`, old_string: 'a', new_string: 'b' }])
  await turn($, w, 'turn-2', 'now add a test')
  await w.clock.advance(5_000)
  expect(w.modelCalls).toHaveLength(2)
  for (const call of w.modelCalls) {
    expect(call.model).toBe('haiku')
    expect(call.maxTokens).toBeLessThanOrEqual(300)
    expect(call.maxTokens).toBe(FORECAST_MAX_TOKENS)
    expect(call.system).toContain('JSON')
  }
  const events = sent(w.requests)
  expect(ofKind(events, 'turn.forecast')).toEqual([
    { turnSeq: 1, files: ['src/a.ts'], testsPass: true, steps: 4, confidence: 0.7, model: 'haiku', forecastTokens: 220 },
    { turnSeq: 2, files: ['src/a.ts'], testsPass: true, steps: 4, confidence: 0.7, model: 'haiku', forecastTokens: 220 },
  ])
  expect(ofKind(events, 'turn.actual')).toEqual([
    { turnSeq: 1, files: ['src/a.ts'], testsRan: false, testsPass: null, steps: 1 },
    { turnSeq: 2, files: [], testsRan: false, testsPass: null, steps: 0 },
  ])
  // the second turn's model input names the file the first turn edited
  expect(w.modelCalls[1]?.prompt).toContain('- src/a.ts')
})

test('a turn without the person\'s text is not forecast: no model call, no forecast, no actual; it still counts', ON, async ($, on) => {
  const w = await begin($, on)
  await turn($, w, 'turn-1', '')
  await turn($, w, 'turn-2', '   ')
  await turn($, w, 'turn-3', '<command-name>/review</command-name>')
  await turn($, w, 'turn-4', '/compact')
  expect(w.modelCalls).toHaveLength(0)
  await turn($, w, 'turn-5', 'refactor the queue')
  await w.clock.advance(5_000)
  expect(w.modelCalls).toHaveLength(1)
  const events = sent(w.requests)
  expect(ofKind(events, 'turn.forecast').map(p => p.turnSeq)).toEqual([5])
  expect(ofKind(events, 'turn.actual').map(p => p.turnSeq)).toEqual([5])
})

test('a turn that starts while the last forecast is still open is not forecast', ON, async ($, on) => {
  const w = await begin($, on, { models: ['hang'] })
  await turn($, w, 'turn-1', 'first request')
  await turn($, w, 'turn-2', 'second request')
  await turn($, w, 'turn-3', 'third request')
  await w.clock.advance(5_000)
  expect(w.modelCalls).toHaveLength(1)
  const events = sent(w.requests)
  // turn 1's forecast never settled, so only its actual is on the wire
  expect(ofKind(events, 'turn.forecast')).toHaveLength(0)
  expect(ofKind(events, 'turn.actual').map(p => p.turnSeq)).toEqual([1])
})

test('the hook returns before model.complete resolves: the turn never waits on the model', ON, async ($, on) => {
  const w = await begin($, on, { models: ['hang'] })
  const start = track($.turn.start({ text: 'do the thing', turnId: 'turn-1' }))
  await w.clock.advance(0)
  expect(start.isDone()).toBe(true)
  expect(w.modelCalls).toHaveLength(1)
  const complete = track($.turn.complete({ ...COMPLETE, turnId: 'turn-1' }))
  await w.clock.advance(0)
  expect(complete.isDone()).toBe(true)
})

test('a forecast that answers after its turn completed is still sent, after the actual', ON, async ($, on) => {
  const w = await begin($, on, { models: [{ text: '{"files":["b.ts"],"steps":2,"confidence":0.5}', afterMs: 10_000 }] })
  await turn($, w, 'turn-1', 'quick one')
  await w.clock.advance(3_000)
  expect(ofKind(sent(w.requests), 'turn.actual')).toHaveLength(1)
  expect(ofKind(sent(w.requests), 'turn.forecast')).toHaveLength(0)
  await w.clock.advance(10_000)
  const events = sent(w.requests)
  expect(ofKind(events, 'turn.forecast')).toEqual([
    { turnSeq: 1, files: ['b.ts'], testsPass: null, steps: 2, confidence: 0.5, model: 'haiku', forecastTokens: 220 },
  ])
  expect(events.findIndex(e => e.kind === 'turn.actual')).toBeLessThan(events.findIndex(e => e.kind === 'turn.forecast'))
})

for (const reply of ['I think it will touch a few files.', '{"files": [', ''] as const) {
  test(`an unreadable model reply (${JSON.stringify(reply)}) still sends a forecast, every claim null`, ON, async ($, on) => {
    const w = await begin($, on, { models: [reply] })
    await turn($, w, 'turn-1', 'something')
    await w.clock.advance(5_000)
    expect(ofKind(sent(w.requests), 'turn.forecast')).toEqual([
      { turnSeq: 1, files: [], testsPass: null, steps: null, confidence: null, model: 'haiku', forecastTokens: 220 },
    ])
  })
}

test('an unanswered model call (api-error) still sends a forecast, every claim null, tokens counted', ON, async ($, on) => {
  const w = await begin($, on, { models: [{ reason: 'api-error' }] })
  await turn($, w, 'turn-1', 'something')
  await w.clock.advance(5_000)
  expect(ofKind(sent(w.requests), 'turn.forecast')).toEqual([
    { turnSeq: 1, files: [], testsPass: null, steps: null, confidence: null, model: 'haiku', forecastTokens: 220 },
  ])
  expect(w.logs.some(l => l.includes('forecast: no reply (api-error)'))).toBe(true)
})

test('the actual comes from the turn\'s tool calls: files relative to cwd, a red test run, steps', ON, async ($, on) => {
  // ok Edit, ok Write, denied Edit, errored Edit, ok Read, red test run
  const w = await begin($, on, { tools: ['ok', 'ok', 'deny', 'error', 'ok', 'error'] })
  await turn($, w, 'turn-1', 'fix it', [
    { tool: 'Edit', file_path: `${CWD}/src/a.ts`, old_string: 'a', new_string: 'b' },
    { tool: 'Write', file_path: '/elsewhere/notes.md', content: 'x' },
    { tool: 'Edit', file_path: `${CWD}/src/denied.ts`, old_string: 'a', new_string: 'b' },
    { tool: 'Edit', file_path: `${CWD}/src/failed.ts`, old_string: 'a', new_string: 'b' },
    { tool: 'Read', file_path: `${CWD}/src/c.ts` },
    { tool: 'Bash', command: 'go test ./internal/...', description: 'run tests' },
  ])
  await w.clock.advance(5_000)
  expect(ofKind(sent(w.requests), 'turn.actual')).toEqual([
    { turnSeq: 1, files: ['src/a.ts', '/elsewhere/notes.md'], testsRan: true, testsPass: false, steps: 6 },
  ])
})

test('a green test run after a red one in the same turn reads as passing', ON, async ($, on) => {
  const w = await begin($, on, { tools: ['error', 'ok'] })
  await turn($, w, 'turn-1', 'fix the tests', [
    { tool: 'Bash', command: 'npm test', description: 'run tests' },
    { tool: 'Bash', command: 'npx vitest run', description: 'run tests' },
  ])
  await w.clock.advance(5_000)
  expect(ofKind(sent(w.requests), 'turn.actual')).toEqual([{ turnSeq: 1, files: [], testsRan: true, testsPass: true, steps: 2 }])
})

test('a subagent\'s tool calls never reach the journal', () => {
  const { state } = startTurn(INITIAL_FORECAST, 'turn-1', 'go', true)
  const edit = { tool: 'Edit', fields: { file_path: `${CWD}/src/a.ts` } }
  const fromSubagent = noteToolCall(state, { ...edit, agentId: 'agent-1' }, 'ok', CWD)
  expect(fromSubagent.journal).toEqual(state.journal)
  const fromMain = noteToolCall(state, edit, 'ok', CWD)
  expect(fromMain.journal).toEqual({ files: ['src/a.ts'], testsRan: false, testsPass: null, steps: 1 })
  const ran = noteToolCall(state, { tool: 'Bash', agentId: 'agent-1', fields: { command: 'pytest' } }, 'error', CWD)
  expect(ran.journal.testsRan).toBe(false)
})

test('turnSeq starts over at a session.end with reason clear, not at any other end; the clear starts a new run', ON, async ($, on) => {
  const w = await begin($, on)
  await turn($, w, 'turn-1', 'one')
  await turn($, w, 'turn-2', 'two')
  await w.clock.advance(1_000)
  await $.session.end({ ...END, reason: 'clear' })
  await turn($, w, 'turn-3', 'three')
  await $.session.end({ ...END, reason: 'other' })
  await turn($, w, 'turn-4', 'four')
  await w.clock.advance(5_000)
  const events = sent(w.requests)
  expect(ofKind(events, 'turn.forecast').map(p => p.turnSeq)).toEqual([1, 2, 1, 2])
  expect(ofKind(events, 'turn.actual').map(p => p.turnSeq)).toEqual([1, 2, 1, 2])
  const [a, b, c, d] = runsOf(events, 'turn.forecast')
  expect(a).toBe(b)
  expect(c).toBe(d)
  expect(c).not.toBe(a)
  expect(runsOf(events, 'turn.actual')).toEqual([a, a, c, c])
})

test('turnSeq starts over at session.start; a --resume (same session id) is a new run', ON, async ($, on) => {
  const w = await begin($, on)
  await turn($, w, 'turn-1', 'one')
  await w.clock.advance(1_000)
  await $.session.start(START)
  await turn($, w, 'turn-2', 'two')
  await w.clock.advance(5_000)
  const events = sent(w.requests)
  expect(ofKind(events, 'turn.forecast').map(p => p.turnSeq)).toEqual([1, 1])
  expect(events.filter(e => e.kind === 'turn.forecast').map(e => e.sessionId)).toEqual([SESSION_ID, SESSION_ID])
  const [first, second] = runsOf(events, 'turn.forecast')
  expect(first).not.toBe(second)
  expect(runsOf(events, 'turn.actual')).toEqual([first, second])
})

test('an in-process resume starts a new run at the resync: turnSeq 1 again, a fresh runId', ON, async ($, on) => {
  const w = await begin($, on)
  await turn($, w, 'turn-1', 'one')
  await turn($, w, 'turn-2', 'two')
  await w.clock.advance(1_000)
  await $.session.end({ ...END, reason: 'resume' })
  w.moveTo('3c9d2b1a-0000-4000-8000-00000000abcd')
  await turn($, w, 'turn-3', 'three')
  await w.clock.advance(5_000)
  const events = sent(w.requests)
  expect(ofKind(events, 'turn.forecast').map(p => p.turnSeq)).toEqual([1, 2, 1])
  const [a, b, c] = runsOf(events, 'turn.forecast')
  expect(a).toBe(b)
  expect(c).not.toBe(a)
  expect(runsOf(events, 'turn.actual')).toEqual([a, a, c])
  const resumed = events.filter(e => e.kind === 'turn.forecast').at(-1)
  expect(resumed?.sessionId).toBe('3c9d2b1a-0000-4000-8000-00000000abcd')
})

test('an in-process resume to the same session id is still a new run', ON, async ($, on) => {
  const w = await begin($, on)
  await turn($, w, 'turn-1', 'one')
  await w.clock.advance(1_000)
  await $.session.end({ ...END, reason: 'resume' })
  await turn($, w, 'turn-2', 'two')
  await w.clock.advance(5_000)
  const events = sent(w.requests)
  expect(ofKind(events, 'turn.forecast').map(p => p.turnSeq)).toEqual([1, 1])
  const [a, b] = runsOf(events, 'turn.forecast')
  expect(a).not.toBe(b)
})

test('every runId on the wire has the daemon\'s shape and starts with the reset\'s ms, then 6 random base36 chars (crypto.getRandomValues)', ON, async ($, on) => {
  const w = await begin($, on)
  const startedAt = w.clock.now()
  await turn($, w, 'turn-1', 'one')
  await w.clock.advance(5_000)
  const runs = [...runsOf(sent(w.requests), 'turn.forecast'), ...runsOf(sent(w.requests), 'turn.actual')]
  expect(runs).toHaveLength(2)
  for (const run of runs) {
    expect(typeof run).toBe('string')
    expect(RUN_ID.test(String(run))).toBe(true)
    expect(String(run).split('-')[0]).toBe(String(startedAt))
    expect(/^[0-9]+-[0-9a-z]{6}$/.test(String(run))).toBe(true)
  }
})

test('a forecast still in flight across a /clear is dropped, never queued under the new session', ON, async ($, on) => {
  const NEW_ID = '9e8d7c6b-0000-4000-8000-0000000000ff'
  const w = await begin($, on, { models: [{ text: '{"files":["old.ts"],"steps":9,"confidence":0.9}', afterMs: 10_000 }, '{"files":["new.ts"],"steps":1,"confidence":0.3}'] })
  await turn($, w, 'turn-1', 'slow forecast')
  await $.session.end({ ...END, reason: 'clear' })
  w.moveTo(NEW_ID)
  await turn($, w, 'turn-2', 'after the clear')
  await w.clock.advance(15_000)
  const events = sent(w.requests)
  expect(w.modelCalls).toHaveLength(2)
  const forecasts = events.filter(e => e.kind === 'turn.forecast')
  expect(ofKind(events, 'turn.forecast')).toEqual([
    { turnSeq: 1, files: ['new.ts'], testsPass: null, steps: 1, confidence: 0.3, model: 'haiku', forecastTokens: 220 },
  ])
  expect(forecasts.map(e => e.sessionId)).toEqual([NEW_ID])
  // turn 1's actual went out under the old session before the clear; turn 2's pairs with turn 2's forecast
  expect(events.filter(e => e.kind === 'turn.actual').map(e => e.sessionId)).toEqual([SESSION_ID, NEW_ID])
  expect(runsOf(events, 'turn.actual')[1]).toBe(runsOf(events, 'turn.forecast')[0])
})

test('runIdOf: ms then 6 base36 chars from the random bytes; the ms alone with no random source', () => {
  const bytes = new Uint8Array([0, 35, 36, 71, 255, 10, 99])
  expect(RUN_ID_RANDOM).toBe(6)
  expect(runIdOf(1760436000123.7, bytes)).toBe('1760436000123-0z0z3a')
  expect(runIdOf(1760436000123, undefined)).toBe('1760436000123')
  expect(runIdOf(1760436000123, new Uint8Array(2))).toBe('1760436000123')
  expect(runIdOf(Number.NaN, bytes)).toBe('0-0z0z3a')
  expect(RUN_ID.test(runIdOf(Number.MAX_SAFE_INTEGER, new Uint8Array(6).fill(255)))).toBe(true)
})

test('no run id yet (before any reset): the turn is counted but never forecast', () => {
  const before = startTurn(INITIAL_FORECAST, 'turn-1', 'a real request', true)
  expect(before.shouldAsk).toBe(false)
  expect(before.state.turnSeq).toBe(1)
  const after = startTurn(resetForecast(INITIAL_FORECAST, 'r-1'), 'turn-1', 'a real request', true)
  expect(after.shouldAsk).toBe(true)
  expect(after.state.runId).toBe('r-1')
})

test('no prompt text, tool input or model prose ever reaches the wire', ON, async ($, on) => {
  const MARK = 'PRIVATE-MARKER-7f3a'
  const reply = `Sure! ${MARK} {"files":["src/a.ts","${MARK} words here","${MARK}"],"testsPass":false,"steps":3,"confidence":0.4}`
  const w = await begin($, on, { models: [reply] })
  await turn($, w, 'turn-1', `please fix ${MARK} in src/a.ts`, [
    { tool: 'Bash', command: `echo ${MARK} && go test ./...`, description: 'run tests' },
    { tool: 'Edit', file_path: `${CWD}/src/a.ts`, old_string: MARK, new_string: 'b' },
  ])
  await w.clock.advance(5_000)
  // the marker did go to the model: the assertion below is not vacuous
  expect(w.modelCalls[0]?.prompt).toContain(MARK)
  const events = sent(w.requests)
  expect(ofKind(events, 'turn.forecast')).toHaveLength(1)
  expect(ofKind(events, 'turn.forecast')[0]?.files).toEqual(['src/a.ts'])
  expect(JSON.stringify(w.requests.map(r => r.body))).not.toContain(MARK)
})

test('no token file: forecast on makes no model call', ON, async ($, on) => {
  const w = world(on)
  await $.session.start(START)
  await turn($, w, 'turn-1', 'anything')
  expect(w.modelCalls).toHaveLength(0)
  expect(w.requests).toHaveLength(0)
})

test('the model is shown at most 2000 chars of the text and the files of the previous 3 turns', () => {
  const ask = forecastRequest('x'.repeat(5_000), [['a.ts'], ['b.ts', 'a.ts'], ['c.ts']])
  const shown = ask.prompt.split('\n')[1] ?? ''
  expect(shown.length).toBeLessThanOrEqual(PROMPT_TEXT_LIMIT + 1)
  expect(ask.prompt).toContain('- a.ts\n- b.ts\n- c.ts')
})

test('history keeps the files of the last 3 turns only', ON, async ($, on) => {
  const w = await begin($, on)
  for (const [i, file] of ['one.ts', 'two.ts', 'three.ts', 'four.ts'].entries()) {
    await turn($, w, `turn-${i}`, `edit ${file}`, [{ tool: 'Write', file_path: `${CWD}/${file}`, content: 'x' }])
  }
  await turn($, w, 'turn-last', 'what next')
  const last = w.modelCalls.at(-1)?.prompt ?? ''
  expect(last).not.toContain('one.ts')
  expect(last).toContain('- two.ts\n- three.ts\n- four.ts')
})

test('parseForecast: fenced JSON is read; fields are held to the protocol limits', () => {
  const many = Array.from({ length: 80 }, (_, i) => `src/f${i}.ts`)
  const text = [
    '```json',
    JSON.stringify({ files: [`./src/a.ts`, `${CWD}/src/b.ts`, 'p'.repeat(301) + '.ts', 42, ...many], testsPass: 'yes', steps: -3.6, confidence: 1.7 }),
    '```',
  ].join('\n')
  const claims = parseForecast(text, CWD)
  expect(claims.files.slice(0, 3)).toEqual(['src/a.ts', 'src/b.ts', 'src/f0.ts'])
  expect(claims.files).toHaveLength(MAX_FILES)
  expect(claims.testsPass).toBeNull()
  expect(claims.steps).toBe(0)
  expect(claims.confidence).toBe(1)
  expect(parseForecast('{"files":"a.ts","steps":"4","confidence":null}', CWD)).toEqual(NO_CLAIMS)
  expect(parseForecast('[1,2]', CWD)).toEqual(NO_CLAIMS)
  expect(parseForecast('{"note":"a } inside","steps":2}', CWD)).toEqual({ ...NO_CLAIMS, steps: 2 })
})
