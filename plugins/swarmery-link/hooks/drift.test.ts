import type { On } from 'claude-code'
import { type Engine, expect, test } from 'claude-code/testing'
import { DRIFT_LABELS, DRIFT_TIMEOUT_MS, driftInput, withBeneath } from './lib/plan'
import { COMPLETE, CWD, PLAN_VIEW, SESSION_ID, START, TOKEN, type World, track, world } from './testkit'

const ON = { options: { planBand: true } } as const
const PLAN = '/api/link/plan'
const EDIT = { tool: 'Edit', file_path: `${CWD}/src/billing/invoice.ts`, old_string: 'a', new_string: 'b' } as const
const WRITE = { tool: 'Write', file_path: `${CWD}/docs/notes.md`, content: 'x' } as const

const begin = async ($: Engine, on: On, w: World = {}): Promise<ReturnType<typeof world>> => {
  const recorded = world(on, { tokens: [TOKEN], plans: [{ view: PLAN_VIEW }], ...w })
  await $.session.start(START)
  await recorded.clock.advance(0)
  return recorded
}

const drifts = (w: ReturnType<typeof world>) => w.planCalls.filter(c => c.url.endsWith(`${PLAN}/drift`))

test('one classify per main-loop turn that edited files, none for a turn without edits', ON, async ($, on) => {
  const w = await begin($, on)
  await $.turn.start({ text: 'go', turnId: 'turn-1' })
  await $.tool.call(EDIT)
  await $.tool.call(WRITE)
  await $.tool.call({ tool: 'Bash', command: 'ls', description: 'list' })
  await $.turn.complete({ ...COMPLETE, turnId: 'turn-1' })
  await $.turn.start({ text: 'and explain it', turnId: 'turn-2' })
  await $.tool.call({ tool: 'Read', file_path: `${CWD}/src/a.ts` })
  await $.turn.complete({ ...COMPLETE, turnId: 'turn-2' })
  expect(w.classifyCalls).toHaveLength(1)
  const [call] = w.classifyCalls
  expect(call?.labels).toEqual([...DRIFT_LABELS])
  expect(call?.text).toContain('Running plan phase 2: Line-item CRUD')
  expect(call?.text).toContain('- GET lists the line items in insert order')
  expect(call?.text).toContain('- src/billing/invoice.ts\n- docs/notes.md')
})

test('a turn.complete raised twice for one turn classifies once', ON, async ($, on) => {
  const w = await begin($, on, { classify: ['off-phase'] })
  await $.turn.start({ text: 'go', turnId: 'turn-1' })
  await $.tool.call(EDIT)
  await $.turn.complete({ ...COMPLETE, turnId: 'turn-1' })
  const again = await $.turn.complete({ ...COMPLETE, turnId: 'turn-1' })
  expect(w.classifyCalls).toHaveLength(1)
  expect(again.text).toBe('hi')
})

test('a subagent\'s edits and turns never classify', ON, async ($, on) => {
  const w = await begin($, on, { classify: ['off-phase'] })
  await $.turn.start({ text: 'go', turnId: 'turn-1' })
  await $.tool.call({ ...EDIT, agentId: 'agent-1' } as never)
  await $.turn.complete({ ...COMPLETE, turnId: 'sub-1', agentId: 'agent-1' })
  await $.turn.complete({ ...COMPLETE, turnId: 'turn-1' })
  expect(w.classifyCalls).toHaveLength(0)
})

test('off-phase: a drift POST and one line beneath the answer', ON, async ($, on) => {
  const w = await begin($, on, { classify: ['off-phase'] })
  await $.turn.start({ text: 'go', turnId: 'turn-1' })
  await $.tool.call(EDIT)
  await $.tool.call(WRITE)
  const result = await $.turn.complete({ ...COMPLETE, turnId: 'turn-1' })
  expect(result.text).toBe('↯ this turn looks outside phase 2: edited src/billing/invoice.ts, docs/notes.md')
  await w.clock.advance(0)
  expect(drifts(w).map(c => c.body)).toEqual([
    {
      session: SESSION_ID,
      taskId: PLAN_VIEW.taskId,
      phaseId: 'phase-2',
      verdict: 'off-phase',
      reason: 'edited src/billing/invoice.ts, docs/notes.md',
    },
  ])
  expect(drifts(w)[0]?.authorization).toBe(`Bearer ${TOKEN}`)
})

test('on-phase or adjacent: no line, no drift POST', ON, async ($, on) => {
  const w = await begin($, on, { classify: ['on-phase', 'adjacent'] })
  for (const turnId of ['turn-1', 'turn-2']) {
    await $.turn.start({ text: 'go', turnId })
    await $.tool.call(EDIT)
    const result = await $.turn.complete({ ...COMPLETE, turnId })
    expect(result.text).toBe('hi')
  }
  await w.clock.advance(0)
  expect(w.classifyCalls).toHaveLength(2)
  expect(drifts(w)).toHaveLength(0)
})

test('a classify that fails: no line, the turn\'s result intact', ON, async ($, on) => {
  const w = await begin($, on, { classify: ['throw'] })
  await $.turn.start({ text: 'go', turnId: 'turn-1' })
  await $.tool.call(EDIT)
  const result = await $.turn.complete({ ...COMPLETE, turnId: 'turn-1' })
  expect(result.text).toBe('hi')
  expect(w.classifyCalls).toHaveLength(1)
  expect(drifts(w)).toHaveLength(0)
})

test('a classify that never answers is given up after 15 s: no line', ON, async ($, on) => {
  const w = await begin($, on, { classify: ['hang'] })
  await $.turn.start({ text: 'go', turnId: 'turn-1' })
  await $.tool.call(EDIT)
  const pending = $.turn.complete({ ...COMPLETE, turnId: 'turn-1' })
  const watched = track(pending)
  await w.clock.advance(DRIFT_TIMEOUT_MS - 1)
  expect(watched.isDone()).toBe(false)
  await w.clock.advance(1)
  expect((await pending).text).toBe('hi')
  expect(drifts(w)).toHaveLength(0)
})

test('no plan for the session: edits are not classified', ON, async ($, on) => {
  const w = await begin($, on, { plans: [204], classify: ['off-phase'] })
  await $.turn.start({ text: 'go', turnId: 'turn-1' })
  await $.tool.call(EDIT)
  const result = await $.turn.complete({ ...COMPLETE, turnId: 'turn-1' })
  expect(result.text).toBe('hi')
  expect(w.classifyCalls).toHaveLength(0)
})

test('a denied edit is not an edit', ON, async ($, on) => {
  const w = await begin($, on, { tools: ['deny'], classify: ['off-phase'] })
  await $.turn.start({ text: 'go', turnId: 'turn-1' })
  await $.tool.call(EDIT)
  await $.turn.complete({ ...COMPLETE, turnId: 'turn-1' })
  expect(w.classifyCalls).toHaveLength(0)
})

test('driftInput keeps the answer\'s last 2 KiB; withBeneath appends to a line already there', () => {
  const answer = `${'x'.repeat(5_000)}TAIL`
  const text = driftInput(
    { id: 'p', n: 1, title: 't', status: '', done: 0, total: 0, criteria: [] },
    answer,
    ['a.ts'],
  )
  expect(text).toContain(`${'x'.repeat(2_044)}TAIL`)
  expect(text).not.toContain('x'.repeat(2_045))
  expect(withBeneath('hi', 'hi', ['one'])).toBe('one')
  expect(withBeneath('TL;DR', 'hi', ['one', 'two'])).toBe('TL;DR\none\ntwo')
  expect(withBeneath('hi', 'hi', [])).toBe('hi')
})
