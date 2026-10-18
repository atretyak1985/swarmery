import type { CommandRunInput, On, RenderPropsOf } from 'claude-code'
import { type Engine, expect, test } from 'claude-code/testing'
import { NO_PLAN_TEXT, PLAN_PANE_ID, PLAN_REFRESH_MS, bandLayout, parsePlanView } from './lib/plan'
import { BAND_PROPS, COMPLETE, CWD, END, ENGINE_DRAWING, PLAN_VIEW, SESSION_ID, START, SURFACES, TOKEN, type World, world } from './testkit'

const ON = { options: { planBand: true } } as const
const PLUGIN = 'swarmery-link'
const PLAN = '/api/link/plan'

const begin = async ($: Engine, on: On, w: World = {}): Promise<ReturnType<typeof world>> => {
  const recorded = world(on, { tokens: [TOKEN], plans: [{ view: PLAN_VIEW }], ...w })
  await $.session.start(START)
  await recorded.clock.advance(0)
  return recorded
}

const band = ($: Engine, surface: (typeof SURFACES)[number] = 'terminal', props: Partial<RenderPropsOf['AbovePrompt']> = {}) =>
  $.ui.mount({ plugin: PLUGIN, surface, component: 'AbovePrompt', props: { ...BAND_PROPS, ...props } })

/** `/plan <args>` as the person types it at the prompt. */
const slash = (args = ''): CommandRunInput => ({
  command: 'plan',
  args,
  origin: { kind: 'composer' },
  presentation: { isFullscreen: false, columns: 80 },
})

const gets = (w: ReturnType<typeof world>) => w.planCalls.filter(c => c.method === 'GET')
const posts = (w: ReturnType<typeof world>, action: string) => w.planCalls.filter(c => c.url.endsWith(`${PLAN}/${action}`))

test('planBand off: no band, no /plan, no timer, no plan request', async ($, on) => {
  const w = world(on, { tokens: [TOKEN], plans: [{ view: PLAN_VIEW }] })
  await $.session.start(START)
  await $.turn.start({ text: 'go', turnId: 'turn-1' })
  await $.tool.call({ tool: 'Edit', file_path: `${CWD}/src/a.ts`, old_string: 'a', new_string: 'b' })
  await $.turn.complete({ ...COMPLETE, turnId: 'turn-1' })
  await w.clock.advance(3 * PLAN_REFRESH_MS)
  const ui = await band($)
  expect(await ui.drawn()).toEqual(ENGINE_DRAWING)
  expect(await ui.find({ text: /Phase/ })).toBeUndefined()
  expect(w.planCalls).toHaveLength(0)
  expect(w.commands).toHaveLength(0)
  expect(w.classifyCalls).toHaveLength(0)
  expect(w.logs.some(l => l.includes('planBand'))).toBe(false)
})

test('session.start registers /plan and reads the plan with the bearer token, session and cwd', ON, async ($, on) => {
  const w = await begin($, on)
  expect(w.commands).toEqual([{ name: 'plan', argumentHint: '[task-id]' }])
  expect(gets(w)).toHaveLength(1)
  const [get] = gets(w)
  expect(get?.url).toBe(`http://127.0.0.1:7777${PLAN}?session=${SESSION_ID}&cwd=${encodeURIComponent(CWD)}`)
  expect(get?.authorization).toBe(`Bearer ${TOKEN}`)
})

test('the band: Phase n — title · k/m, the open criteria, and three hotkey buttons', ON, async ($, on) => {
  const w = await begin($, on)
  const ui = await band($)
  expect(await ui.find({ type: 'Text', text: 'Phase 2 — Line-item CRUD · 1/4' })).toBeDefined()
  const rows = (await ui.findAll({ type: 'Text', text: /^[☐☑] / })).map(t => t.text)
  // open ones first, at most three
  expect(rows).toEqual(['☐ GET lists the line items in insert order', '☐ DELETE removes one and answers 204', '☐ deployed to staging'])
  const buttons = await ui.findAll({ type: 'Button' })
  expect(buttons.map(b => [b.key, b.props.hotkey, b.props.label])).toEqual([
    ['plan-next', '1', 'next phase'],
    ['plan-open', '2', 'open in Swarmery'],
    ['plan-tick', '3', 'tick'],
  ])
  expect(w.planCalls).toHaveLength(1)
})

test('the band\'s tree validates on every surface the kit knows', ON, async ($, on) => {
  await begin($, on)
  for (const surface of SURFACES) {
    const ui = await band($, surface)
    expect(await ui.find({ type: 'Text', text: /^Phase 2 — / }), surface).toBeDefined()
    expect(await ui.findAll({ type: 'Button' }), surface).toHaveLength(3)
    await ui.unmount()
  }
})

test('the last phase draws no [1] next phase', ON, async ($, on) => {
  await begin($, on, { plans: [{ view: { ...PLAN_VIEW, next: null } }] })
  const ui = await band($)
  expect((await ui.findAll({ type: 'Button' })).map(b => b.props.hotkey)).toEqual(['2', '3'])
})

test('a 204 (no plan), no token or a daemon down draws nothing of the plugin\'s', ON, async ($, on) => {
  await begin($, on, { plans: [204] })
  const ui = await band($)
  expect(await ui.drawn()).toEqual(ENGINE_DRAWING)
  expect(await ui.findAll({ type: 'Button' })).toHaveLength(0)
})

test('no token: no plan request, and the band draws nothing', ON, async ($, on) => {
  const w = world(on, { plans: [{ view: PLAN_VIEW }] })
  await $.session.start(START)
  await w.clock.advance(PLAN_REFRESH_MS * 2)
  const ui = await band($)
  expect(await ui.drawn()).toEqual(ENGINE_DRAWING)
  expect(w.planCalls).toHaveLength(0)
})

test('a render never fetches: drawing and redrawing make no request', ON, async ($, on) => {
  const w = await begin($, on)
  const before = w.planCalls.length
  const ui = await band($)
  await ui.redraw({ ...BAND_PROPS, isWorking: true })
  await ui.redraw({ ...BAND_PROPS, bodyColumns: 30 })
  expect(w.planCalls).toHaveLength(before)
})

test('the band keeps to its rows and columns, and yields to a survey', ON, async ($, on) => {
  await begin($, on)
  const small = await band($, 'terminal', { maxRows: 3, bodyColumns: 20 })
  const texts = (await small.findAll({ type: 'Text' })).map(t => t.text)
  expect(texts).toEqual(['Phase 2 — Line-item…', '☐ GET lists the lin…'])
  await small.unmount()
  const survey = await band($, 'terminal', { hasSurvey: true })
  expect(await survey.drawn()).toEqual(ENGINE_DRAWING)
})

test('bandLayout: the done criteria fill what the open ones leave', () => {
  const view = parsePlanView(JSON.stringify(PLAN_VIEW))
  expect(view).toBeDefined()
  if (view === undefined) return
  const allButOne = { ...view, phase: { ...view.phase, criteria: view.phase.criteria.map(c => ({ ...c, done: c.idx !== 3 })) } }
  expect(bandLayout(allButOne, 10, 80).rows).toEqual(['☐ deployed to staging', '☑ POST /orders/{id}/line-items creates a row', '☑ GET lists the line items in insert order'])
})

test('the cache refreshes every 60 s and stops at session.end', ON, async ($, on) => {
  const w = await begin($, on)
  await w.clock.advance(PLAN_REFRESH_MS)
  expect(gets(w)).toHaveLength(2)
  await w.clock.advance(PLAN_REFRESH_MS)
  expect(gets(w)).toHaveLength(3)
  await $.session.end(END)
  await w.clock.advance(PLAN_REFRESH_MS * 3)
  expect(gets(w)).toHaveLength(3)
})

test('a main-loop turn.complete refreshes the cache and redraws the band', ON, async ($, on) => {
  const later = { ...PLAN_VIEW, phase: { ...PLAN_VIEW.phase, done: 2 } }
  const w = await begin($, on, { plans: [{ view: PLAN_VIEW }, { view: later }] })
  const ui = await band($)
  expect(await ui.find({ text: /· 1\/4$/ })).toBeDefined()
  await $.turn.start({ text: 'go', turnId: 'turn-1' })
  await $.turn.complete({ ...COMPLETE, turnId: 'turn-1' })
  await w.clock.advance(0)
  expect(gets(w)).toHaveLength(2)
  expect(await ui.find({ text: /· 2\/4$/ })).toBeDefined()
})

test('/plan runs no model turn and opens the Plan pane with the whole checklist', ON, async ($, on) => {
  const w = await begin($, on)
  const result = await $.command.run(slash())
  expect(w.submits).toHaveLength(0)
  expect(result.text).toBe('Phase 2 — Line-item CRUD · 1/4 (checklist in the Plan pane)')
  expect(w.panes).toEqual([{ id: PLAN_PANE_ID, title: 'Plan' }])
  // /plan read the plan again
  expect(gets(w)).toHaveLength(2)
  const pane = await $.ui.mount({
    plugin: PLUGIN,
    surface: 'terminal',
    component: 'Pane',
    requestId: PLAN_PANE_ID,
    props: { title: 'Plan', isFocused: false, bodyColumns: 60, placement: 'inline', scroll: { offset: 0, bodyRows: 20 }, view: {} },
  })
  const texts = (await pane.findAll({ type: 'Text' })).map(t => t.text)
  expect(texts).toEqual([
    'Order line items (2026-10-13-order-line-items)',
    'Phase 2 — Line-item CRUD · 1/4 · In progress',
    '☑ POST /orders/{id}/line-items creates a row',
    '☐ GET lists the line items in insert order',
    '☐ DELETE removes one and answers 204',
    '☐ deployed to staging [LAND]',
    'next: Phase 3 — Totals and tax',
  ])
  expect((await pane.find({ type: 'Link' }))?.props.href).toBe(PLAN_VIEW.url)
})

test('/plan where no pane is placed: the checklist is the command\'s output', ON, async ($, on) => {
  await begin($, on, { isPanePlaced: false })
  const result = await $.command.run(slash())
  expect(result.text?.split('\n')).toEqual([
    'Order line items (2026-10-13-order-line-items)',
    'Phase 2 — Line-item CRUD · 1/4 · In progress',
    '☑ POST /orders/{id}/line-items creates a row',
    '☐ GET lists the line items in insert order',
    '☐ DELETE removes one and answers 204',
    '☐ deployed to staging [LAND]',
    'next: Phase 3 — Totals and tax',
    PLAN_VIEW.url,
  ])
})

test('/plan without a token says what is missing and makes no request', ON, async ($, on) => {
  const w = world(on, { plans: [{ view: PLAN_VIEW }] })
  await $.session.start(START)
  const result = await $.command.run(slash())
  expect(result.text).toContain('no daemon token')
  expect(w.planCalls).toHaveLength(0)
  expect(w.commands).toHaveLength(1)
})

test('/plan with no plan says so in one line', ON, async ($, on) => {
  const w = await begin($, on, { plans: [204] })
  const result = await $.command.run(slash())
  expect(result.text).toBe(NO_PLAN_TEXT)
  expect(w.panes).toHaveLength(0)
  expect(w.submits).toHaveLength(0)
})

test('/plan <task-id> POSTs select, then shows the plan the daemon answered', ON, async ($, on) => {
  const w = await begin($, on, { plans: [204], planPosts: { select: [{ status: 200, body: PLAN_VIEW }] } })
  const result = await $.command.run(slash('2026-10-13-order-line-items'))
  expect(posts(w, 'select').map(c => c.body)).toEqual([{ session: SESSION_ID, cwd: CWD, taskId: '2026-10-13-order-line-items' }])
  expect(posts(w, 'select')[0]?.authorization).toBe(`Bearer ${TOKEN}`)
  expect(result.text).toContain('Phase 2 — Line-item CRUD')
  const ui = await band($)
  expect(await ui.find({ text: /^Phase 2 — / })).toBeDefined()
})

test('/plan <task-id> that the daemon refuses shows its error', ON, async ($, on) => {
  await begin($, on, { plans: [204], planPosts: { select: [404] } })
  const result = await $.command.run(slash('nope'))
  expect(result.text).toBe('swarmery-link: could not select nope: refused (404)')
})

test('[3] tick asks which open criterion, then POSTs tick with its idx and done', ON, async ($, on) => {
  const ticked = { ...PLAN_VIEW, phase: { ...PLAN_VIEW.phase, done: 2, criteria: PLAN_VIEW.phase.criteria.map(c => ({ ...c, done: c.idx <= 2 })) } }
  const w = await begin($, on, { asks: ['2. DELETE removes one and answers 204'], planPosts: { tick: [{ status: 200, body: ticked }] } })
  const ui = await band($)
  await ui.press({ key: 'plan-tick' })
  expect(w.asked).toEqual([
    {
      question: 'Which criterion of phase 2 is done?',
      header: 'Tick',
      options: ['1. GET lists the line items in insert order', '2. DELETE removes one and answers 204', '3. deployed to staging'],
    },
  ])
  expect(posts(w, 'tick').map(c => c.body)).toEqual([
    { session: SESSION_ID, cwd: CWD, taskId: PLAN_VIEW.taskId, phaseId: 'phase-2', idx: 2, text: 'DELETE removes one and answers 204', done: true },
  ])
  expect(await ui.find({ text: /· 2\/4$/ })).toBeDefined()
})

test('a tick answered 409 refreshes the plan and tells the person', ON, async ($, on) => {
  const w = await begin($, on, { planPosts: { tick: [409] }, asks: [] })
  const ui = await band($)
  await ui.press({ key: 'plan-tick' })
  expect(posts(w, 'tick')).toHaveLength(1)
  expect(gets(w)).toHaveLength(2)
  expect(w.toasts).toEqual(['the phase doc changed since the band drew it: refreshed, tick again'])
})

test('a tick of a criterion that moved: the drawn text goes along, 409 "criterion changed" redraws the band from the doc', ON, async ($, on) => {
  // A criterion was inserted above the one picked: idx 1 now reads differently.
  const moved = {
    ...PLAN_VIEW,
    phase: {
      ...PLAN_VIEW.phase,
      total: 5,
      criteria: [
        PLAN_VIEW.phase.criteria[0],
        { idx: 1, text: 'PATCH edits a line item', done: false, class: '' },
        ...PLAN_VIEW.phase.criteria.slice(1).map(c => ({ ...c, idx: c.idx + 1 })),
      ],
    },
  }
  const w = await begin($, on, {
    plans: [{ view: PLAN_VIEW }, { view: moved }],
    planPosts: { tick: [{ status: 409, body: { error: 'criterion changed' } }] },
    asks: ['1. GET lists the line items in insert order'],
  })
  const ui = await band($)
  await ui.press({ key: 'plan-tick' })
  expect(posts(w, 'tick').map(c => c.body)).toEqual([
    { session: SESSION_ID, cwd: CWD, taskId: PLAN_VIEW.taskId, phaseId: 'phase-2', idx: 1, text: 'GET lists the line items in insert order', done: true },
  ])
  expect(gets(w)).toHaveLength(2)
  expect(w.toasts).toEqual(['the phase doc changed since the band drew it: refreshed, tick again'])
  expect(await ui.find({ text: /· 1\/5$/ })).toBeDefined()
  expect(await ui.find({ text: '☐ PATCH edits a line item' })).toBeDefined()
})

test('[1] next phase asks first, then POSTs run for the next phase', ON, async ($, on) => {
  const w = await begin($, on, { asks: [] })
  const ui = await band($)
  await ui.press({ key: 'plan-next' })
  expect(w.asked.map(a => a.question)).toEqual(['Start phase 3 — Totals and tax now?'])
  expect(posts(w, 'run').map(c => c.body)).toEqual([{ session: SESSION_ID, cwd: CWD, taskId: PLAN_VIEW.taskId, phaseId: 'phase-3' }])
  expect(w.toasts).toEqual(['phase 3 started'])
})

test('[1] next phase declined: no run', ON, async ($, on) => {
  const w = await begin($, on, { asks: ['Not now'] })
  const ui = await band($)
  await ui.press({ key: 'plan-next' })
  expect(w.asked).toHaveLength(1)
  expect(posts(w, 'run')).toHaveLength(0)
})

test('[1] next phase the daemon refuses: its error is shown', ON, async ($, on) => {
  const w = await begin($, on, { planPosts: { run: [{ status: 409, body: { error: 'phase 2 is still running' } }] }, asks: [] })
  const ui = await band($)
  await ui.press({ key: 'plan-next' })
  expect(w.toasts).toEqual(['could not start phase 3: phase 2 is still running'])
})

test('[2] open in Swarmery copies the plan URL', ON, async ($, on) => {
  const w = await begin($, on)
  const ui = await band($)
  await ui.press({ key: 'plan-open' })
  expect(w.copies).toEqual([PLAN_VIEW.url])
  expect(w.toasts).toEqual([`copied ${PLAN_VIEW.url}`])
  expect(w.planCalls).toHaveLength(1)
})
