// The plan band (option `planBand`): the session's running plan phase above
// the prompt, `/plan`, and the requests behind them. Every function here that
// takes `$` is a top-level declaration, as the engine requires; register.ts
// wires them to its hooks and nothing here registers a hook.
//
// A render never fetches: the band and the pane draw from `planner.view`,
// which only the refreshes (session.start, each main-loop turn.complete,
// /plan, a button's request, the 60 s timer) write. A write that changed what
// is drawn redraws with `$.ui.invalidate('ui.render')`.

import type { CommandRunResult, EngineInterface, RenderElement, RenderInput, Timer, UiPressArgument } from 'claude-code'
import { linkRequest } from './lib/client'
import {
  type DriftTurn,
  NO_PLAN_TEXT,
  PLAN_PANE_ID,
  PLAN_REFRESH_MS,
  PLAN_TIMEOUT_MS,
  type PlanAction,
  type PlanView,
  absoluteUrl,
  bandLayout,
  checklistLines,
  criterionLine,
  headerLine,
  isSameView,
  nextLine,
  parsePlanView,
  planActionUrl,
  planGetRequest,
  planUrl,
  replyError,
  statusLine,
  tickChoices,
  titleLine,
  truncate,
} from './lib/plan'

/** What the plan requests read of the link: its token, session and cwd, and whether it is off. */
export type PlanAccess = {
  readonly token: string | undefined
  readonly sessionId: string
  readonly cwd: string
  readonly isOff: boolean
}

/** The band's state for one load of the module. Exists only with `planBand` on. */
export type Planner = {
  readonly origin: string
  /** The session's plan as the daemon last answered it; undefined: draw nothing. */
  view: PlanView | undefined
  /** Bumped by every write of `view`: a GET started under an older one is dropped. */
  version: number
  /** The 60 s refresh, while the band has a token. */
  timer: Timer | undefined
  /** The running main-loop turn, for the drift check. */
  turn: DriftTurn | undefined
}

export const newPlanner = (origin: string): Planner => ({ origin, view: undefined, version: 0, timer: undefined, turn: undefined })

export type PlanReply = { readonly status?: number; readonly text?: string; readonly error?: unknown }

const START_LABEL = 'Start it'
const TICK_LABEL = 'Tick it'

/** `pending`, or `'timeout'` once `ms` pass on `$.clock` (the same race as register.ts's `within`). */
export async function bounded<T>($: EngineInterface, pending: Promise<T>, ms: number): Promise<T | 'timeout'> {
  let guard: Timer | undefined
  const timeout = new Promise<'timeout'>(resolve => {
    try {
      guard = $.clock.after(ms, () => resolve('timeout'))
    } catch {
      // no timer: the pending work alone decides
    }
  })
  try {
    return await Promise.race([pending, timeout])
  } finally {
    guard?.cancel()
  }
}

/** One plan request, bounded by PLAN_TIMEOUT_MS. Never rejects. */
async function planFetch($: EngineInterface, url: string, init: Parameters<EngineInterface['http']['fetch']>[1]): Promise<PlanReply> {
  try {
    const request = $.http.fetch(url, init).then(
      (r): PlanReply => ({ status: r.status, text: r.text }),
      (error: unknown): PlanReply => ({ error }),
    )
    const reply = await bounded($, request, PLAN_TIMEOUT_MS)
    return reply === 'timeout' ? { error: 'timeout' } : reply
  } catch (error) {
    return { error }
  }
}

/** Writes the cache; redraws only when what is drawn changed. */
export function setView($: EngineInterface, planner: Planner, view: PlanView | undefined): void {
  planner.version += 1
  if (isSameView(planner.view, view)) return
  planner.view = view
  try {
    $.ui.invalidate('ui.render')
  } catch {
    // the next draw picks it up
  }
}

const canRequest = (access: PlanAccess): boolean => access.token !== undefined && !access.isOff && access.sessionId !== ''

/**
 * Reads the session's plan into the cache: a 200 view, else nothing (204, no
 * token, the daemon down or slow). A reply that lands after another write of
 * the cache, or after the session id moved, is dropped. Never rejects.
 */
export async function refreshPlan($: EngineInterface, planner: Planner, access: PlanAccess): Promise<void> {
  if (!canRequest(access)) {
    setView($, planner, undefined)
    return
  }
  const version = planner.version
  const sessionId = access.sessionId
  const reply = await planFetch($, planUrl(planner.origin, sessionId, access.cwd), planGetRequest(access.token ?? ''))
  if (planner.version !== version || access.sessionId !== sessionId) return
  setView($, planner, reply.status === 200 ? parsePlanView(reply.text) : undefined)
}

/** POSTs `body` to /api/link/plan/<action>. Never rejects. */
export async function postPlan($: EngineInterface, planner: Planner, access: PlanAccess, action: PlanAction, body: object): Promise<PlanReply> {
  if (!canRequest(access)) return { error: 'no token' }
  return planFetch($, planActionUrl(planner.origin, action), linkRequest(access.token ?? '', JSON.stringify(body)))
}

/** Starts the 60 s refresh once; it runs until stopPlanTimer. */
export function startPlanTimer($: EngineInterface, planner: Planner, access: PlanAccess): void {
  if (planner.timer !== undefined || !canRequest(access)) return
  try {
    planner.timer = $.clock.every(PLAN_REFRESH_MS, () => {
      void refreshPlan($, planner, access)
    })
  } catch {
    planner.timer = undefined
  }
}

export const stopPlanTimer = (planner: Planner): void => {
  planner.timer?.cancel()
  planner.timer = undefined
}

/** Declares `/plan [task-id]` for the session. */
export async function registerPlanCommand($: EngineInterface): Promise<void> {
  try {
    await $.command.register({
      name: 'plan',
      description: 'Show this session\'s swarmery plan phase and its checklist; /plan <task-id> picks the plan',
      argumentHint: '[task-id]',
    })
  } catch {
    // no command: the band still draws
  }
}

function toast($: EngineInterface, text: string): void {
  try {
    $.ui.toast(text)
  } catch {
    // a toast is a courtesy
  }
}

/**
 * `/plan [task-id]`, answered without a model turn: with an argument the
 * daemon selects that plan for the session first; then the cache is read
 * again and the checklist opens in a pane, or is the command's output where
 * no pane is placed.
 */
export async function runPlanCommand($: EngineInterface, planner: Planner, access: PlanAccess, args: string | undefined): Promise<CommandRunResult> {
  if (access.token === undefined) return { text: 'swarmery-link: no daemon token (~/.swarmery/link-token); /plan needs the swarmery daemon' }
  const taskId = (args ?? '').trim().split(/\s+/)[0] ?? ''
  if (taskId !== '') {
    const reply = await postPlan($, planner, access, 'select', { session: access.sessionId, cwd: access.cwd, taskId })
    if (reply.status !== 200) return { text: `swarmery-link: could not select ${taskId}: ${replyError(reply.status, reply.text)}` }
    const selected = parsePlanView(reply.text)
    if (selected !== undefined) setView($, planner, selected)
    else await refreshPlan($, planner, access)
  } else {
    await refreshPlan($, planner, access)
  }
  const view = planner.view
  if (view === undefined) return { text: NO_PLAN_TEXT }
  let isPlaced = false
  try {
    const opened = await $.ui.open({ id: PLAN_PANE_ID, title: 'Plan', rows: Math.min(20, view.phase.criteria.length + 6) })
    isPlaced = opened.isPlaced
  } catch {
    // no pane: the checklist is the output
  }
  return { text: isPlaced ? `${headerLine(view.phase)} (checklist in the Plan pane)` : checklistLines(view, planner.origin).join('\n') }
}

/** [1] next phase: asks first, then has the daemon start the next phase. */
export async function pressNext($: EngineInterface, planner: Planner, access: PlanAccess): Promise<void> {
  const view = planner.view
  const next = view?.next ?? null
  if (view === undefined || next === null) return
  let answer: string
  try {
    answer = await $.ui.ask(`Start phase ${next.n} — ${truncate(next.title, 60)} now?`, { options: [START_LABEL, 'Not now'], header: 'Plan' })
  } catch {
    return
  }
  if (answer !== START_LABEL) return
  const reply = await postPlan($, planner, access, 'run', { session: access.sessionId, cwd: access.cwd, taskId: view.taskId, phaseId: next.id })
  const isStarted = reply.status !== undefined && reply.status >= 200 && reply.status < 300
  toast($, isStarted ? `phase ${next.n} started` : `could not start phase ${next.n}: ${replyError(reply.status, reply.text)}`)
  await refreshPlan($, planner, access)
}

/** [2] open in Swarmery: no call opens a browser, so the URL goes to the clipboard of the surface pressed on. */
export async function pressOpen($: EngineInterface, planner: Planner, press: UiPressArgument): Promise<void> {
  const view = planner.view
  if (view === undefined || view.url === '') return
  const url = absoluteUrl(view.url, planner.origin)
  let isCopied = false
  try {
    isCopied = (await $.ui.copy({ text: url, surface: press.surface })).isCopied
  } catch {
    // shown instead
  }
  toast($, isCopied ? `copied ${url}` : `open ${url}`)
}

/** [3] tick: picks an open criterion, then has the daemon tick it in the phase doc. */
export async function pressTick($: EngineInterface, planner: Planner, access: PlanAccess): Promise<void> {
  const view = planner.view
  if (view === undefined) return
  const choices = tickChoices(view)
  const only = choices[0]
  if (only === undefined) {
    toast($, `phase ${view.phase.n}: nothing left to tick`)
    return
  }
  let idx: number | undefined
  try {
    if (choices.length === 1) {
      const answer = await $.ui.ask(`Tick "${only.label}"?`, { options: [TICK_LABEL, 'Cancel'], header: 'Tick' })
      idx = answer === TICK_LABEL ? only.idx : undefined
    } else {
      const answer = await $.ui.ask(`Which criterion of phase ${view.phase.n} is done?`, { options: choices.map(c => c.label), header: 'Tick' })
      idx = choices.find(c => c.label === answer)?.idx
    }
  } catch {
    return
  }
  if (idx === undefined) return
  // The drawn text rides along: the daemon answers 409 when the doc's idx-th
  // criterion no longer reads so, instead of ticking whatever moved there.
  const text = view.phase.criteria.find(c => c.idx === idx)?.text
  const reply = await postPlan($, planner, access, 'tick', {
    session: access.sessionId,
    cwd: access.cwd,
    taskId: view.taskId,
    phaseId: view.phase.id,
    idx,
    text,
    done: true,
  })
  if (reply.status === 200) {
    const ticked = parsePlanView(reply.text)
    if (ticked !== undefined) setView($, planner, ticked)
    else await refreshPlan($, planner, access)
    return
  }
  if (reply.status === 409) {
    await refreshPlan($, planner, access)
    toast($, 'the phase doc changed since the band drew it: refreshed, tick again')
    return
  }
  toast($, `could not tick: ${replyError(reply.status, reply.text)}`)
}

/**
 * The band above the prompt, from the cache alone; undefined (draw the
 * engine's) with no plan or while a survey holds the band.
 */
export function drawBand($: EngineInterface, planner: Planner, access: PlanAccess, e: RenderInput<'AbovePrompt'>): RenderElement | undefined {
  const view = planner.view
  if (view === undefined || e.props.hasSurvey) return undefined
  const layout = bandLayout(view, e.props.maxRows, e.props.bodyColumns || (e.viewport?.columns ?? 0))
  const { Box, Text, Button } = $.ui.resolve(e)
  return Box({
    flexDirection: 'column',
    children: [
      Text({ bold: true, wrap: 'truncate-end', children: layout.header }),
      layout.rows.map(row => Text({ wrap: 'truncate-end', dimColor: row.startsWith('☑'), children: row })),
      Box({
        flexDirection: 'row',
        gap: 2,
        children: [
          layout.hasNext
            ? Button({ key: 'plan-next', label: 'next phase', hotkey: '1', plain: true, onPress: () => void pressNext($, planner, access) })
            : null,
          Button({ key: 'plan-open', label: 'open in Swarmery', hotkey: '2', plain: true, onPress: press => void pressOpen($, planner, press) }),
          Button({ key: 'plan-tick', label: 'tick', hotkey: '3', plain: true, onPress: () => void pressTick($, planner, access) }),
        ],
      }),
    ],
  })
}

/** The Plan pane /plan opens: the whole checklist, from the cache alone. */
export function drawPane($: EngineInterface, planner: Planner, e: RenderInput<'Pane'>): RenderElement {
  const { Box, Text, Link } = $.ui.resolve(e)
  const view = planner.view
  if (view === undefined) return Text({ dimColor: true, children: NO_PLAN_TEXT })
  return Box({
    flexDirection: 'column',
    children: [
      Text({ bold: true, children: titleLine(view) }),
      Text({ children: statusLine(view.phase) }),
      view.phase.criteria.map(c => Text({ dimColor: c.done, children: criterionLine(c) })),
      Text({ dimColor: true, children: nextLine(view) }),
      view.url === '' ? null : Link({ href: absoluteUrl(view.url, planner.origin), label: 'open in Swarmery' }),
    ],
  })
}
