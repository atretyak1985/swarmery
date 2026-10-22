// The plan band's decisions. Pure: parses the daemon's plan view, builds the
// plan requests, lays the band and the /phase checklist out, picks what a tick
// offers, and builds the drift classification's input and its line. The `$`
// calls are made in ../plan.ts.
// Contract: GET/POST /api/link/plan* (tools/swarmery, phase 7).

import { FILE_TOOLS, normalizePath } from './forecast'

/** Every plan request gives up after this long (raced against a `$.clock` timer). */
export const PLAN_TIMEOUT_MS = 5_000
/** The cache is refreshed this often while the band is on. */
export const PLAN_REFRESH_MS = 60_000
/** The drift classification gives up after this long. */
export const DRIFT_TIMEOUT_MS = 15_000
/** How much of the turn's answer (its tail, in characters) the classification reads. */
export const DRIFT_ANSWER_LIMIT = 2_048
/** The drift line's reason is cut to this many characters. */
export const DRIFT_REASON_LIMIT = 120
/** The whole drift line, its `↯ … phase N:` lead included, is cut to this many characters. */
export const DRIFT_LINE_LIMIT = 120
/** The view's `phase.status` of a phase that is running (the Plans page's chip). */
export const PHASE_IN_PROGRESS = 'In progress'
/** The labels the drift classification picks from. */
export const DRIFT_LABELS = ['on-phase', 'adjacent', 'off-phase'] as const
/** At most this many criteria rows in the band. */
export const BAND_CRITERIA_ROWS = 3
/** The width the band is laid out in when the surface measured none. */
export const DEFAULT_COLUMNS = 80
/** The id of the pane /phase opens. */
export const PLAN_PANE_ID = 'swarmery-plan'
/** `$.ui.ask` takes 2-4 option labels. */
const ASK_OPTIONS_MAX = 4
/** An ask label keeps this much of a criterion's text. */
const ASK_LABEL_LIMIT = 60

export type CriterionClass = '' | 'land' | 'manual'

export type PlanCriterion = {
  readonly idx: number
  readonly text: string
  readonly done: boolean
  readonly class: CriterionClass
}

export type PlanPhase = {
  readonly id: string
  readonly n: number
  readonly title: string
  readonly status: string
  readonly done: number
  readonly total: number
  readonly criteria: readonly PlanCriterion[]
}

export type PlanNext = { readonly id: string; readonly n: number; readonly title: string }

/** The daemon's PlanView: the session's plan and its running phase. */
export type PlanView = {
  readonly taskId: string
  readonly title: string
  readonly url: string
  readonly chosenBy: 'terminal' | 'auto'
  readonly phase: PlanPhase
  readonly next: PlanNext | null
}

const isRecord = (v: unknown): v is Readonly<Record<string, unknown>> => typeof v === 'object' && v !== null && !Array.isArray(v)
const str = (v: unknown): string | undefined => (typeof v === 'string' ? v : undefined)
const num = (v: unknown): number | undefined => (typeof v === 'number' && Number.isFinite(v) ? v : undefined)

const parseCriterion = (v: unknown): PlanCriterion | undefined => {
  if (!isRecord(v)) return undefined
  const idx = num(v.idx)
  const text = str(v.text)
  if (idx === undefined || text === undefined) return undefined
  const cls = v.class === 'land' || v.class === 'manual' ? v.class : ''
  return { idx, text, done: v.done === true, class: cls }
}

const parseNext = (v: unknown): PlanNext | null => {
  if (!isRecord(v)) return null
  const id = str(v.id)
  const n = num(v.n)
  return id === undefined || n === undefined ? null : { id, n, title: str(v.title) ?? '' }
}

/** A PlanView from the daemon's JSON text, or undefined when it is not one. */
export const parsePlanView = (text: unknown): PlanView | undefined => {
  if (typeof text !== 'string' || text.trim() === '') return undefined
  let raw: unknown
  try {
    raw = JSON.parse(text)
  } catch {
    return undefined
  }
  if (!isRecord(raw) || !isRecord(raw.phase)) return undefined
  const taskId = str(raw.taskId)
  const phase = raw.phase
  const id = str(phase.id)
  const n = num(phase.n)
  if (taskId === undefined || id === undefined || n === undefined) return undefined
  const criteria = (Array.isArray(phase.criteria) ? phase.criteria : [])
    .map(parseCriterion)
    .filter((c): c is PlanCriterion => c !== undefined)
  return {
    taskId,
    title: str(raw.title) ?? taskId,
    url: str(raw.url) ?? '',
    chosenBy: raw.chosenBy === 'terminal' ? 'terminal' : 'auto',
    phase: {
      id,
      n,
      title: str(phase.title) ?? '',
      status: str(phase.status) ?? '',
      done: num(phase.done) ?? criteria.filter(c => c.done).length,
      total: num(phase.total) ?? criteria.length,
      criteria,
    },
    next: parseNext(raw.next),
  }
}

/** Whether two views draw the same: a refresh that changed nothing redraws nothing. */
export const isSameView = (a: PlanView | undefined, b: PlanView | undefined): boolean =>
  JSON.stringify(a ?? null) === JSON.stringify(b ?? null)

/** GET /api/link/plan for this session and cwd. */
export const planUrl = (origin: string, sessionId: string, cwd: string): string =>
  `${origin}/api/link/plan?session=${encodeURIComponent(sessionId)}&cwd=${encodeURIComponent(cwd)}`

export type PlanAction = 'select' | 'tick' | 'run' | 'drift'

/** POST /api/link/plan/<action>. */
export const planActionUrl = (origin: string, action: PlanAction): string => `${origin}/api/link/plan/${action}`

export type PlanGet = { readonly method: 'GET' }

/** The plan read; signedFetch (../signed.ts) adds its signature. */
export const planGetRequest = (): PlanGet => ({ method: 'GET' })

/** The plan's dashboard URL, absolute: a path the daemon gave is put under its origin. */
export const absoluteUrl = (url: string, origin: string): string => (url.startsWith('/') ? `${origin}${url}` : url)

/** The daemon's `error` text from a JSON reply body, or the status. */
export const replyError = (status: number | undefined, text: string | undefined): string => {
  try {
    const body: unknown = JSON.parse(text ?? '')
    if (isRecord(body) && typeof body.error === 'string' && body.error.trim() !== '') return body.error.trim().slice(0, 200)
  } catch {
    // not JSON: the status says it
  }
  return status === undefined ? 'the daemon did not answer' : `HTTP ${status}`
}

/** `text` cut to `max` characters, the cut marked with an ellipsis. */
export const truncate = (text: string, max: number): string => {
  const one = text.replace(/\s+/g, ' ').trim()
  if (max <= 0) return ''
  return one.length <= max ? one : `${one.slice(0, Math.max(0, max - 1))}…`
}

/** `☑` or `☐`, the mark of a criterion. */
export const mark = (c: PlanCriterion): string => (c.done ? '☑' : '☐')

/** The band's and the pane's first line. */
export const headerLine = (phase: PlanPhase): string => `Phase ${phase.n} — ${phase.title} · ${phase.done}/${phase.total}`

export type BandLayout = {
  readonly header: string
  readonly rows: readonly string[]
  readonly hasNext: boolean
  readonly hasOpen: boolean
}

/**
 * What the band draws in `maxRows` rows of `columns` cells: the header, up to
 * three criteria (the open ones first, the done ones filling what is left)
 * and the button row. A band of fewer than three rows draws the header and
 * the buttons alone.
 */
export const bandLayout = (view: PlanView, maxRows: number, columns: number): BandLayout => {
  const width = columns > 0 ? columns : DEFAULT_COLUMNS
  const room = Math.max(0, Math.min(BAND_CRITERIA_ROWS, Math.floor(maxRows) - 2))
  const open = view.phase.criteria.filter(c => !c.done)
  const done = view.phase.criteria.filter(c => c.done)
  const shown = [...open, ...done].slice(0, room)
  return {
    header: truncate(headerLine(view.phase), width),
    rows: shown.map(c => truncate(`${mark(c)} ${c.text}`, width)),
    hasNext: view.next !== null,
    hasOpen: open.length > 0,
  }
}

/** The plan's line in the checklist: its title and task id. */
export const titleLine = (view: PlanView): string => `${view.title} (${view.taskId})`

/** The phase's line in the checklist: the header and the phase's status. */
export const statusLine = (phase: PlanPhase): string => `${headerLine(phase)}${phase.status === '' ? '' : ` · ${phase.status}`}`

/** One criterion in the checklist, its class (`[LAND]`, `[MANUAL]`) after it. */
export const criterionLine = (c: PlanCriterion): string => `${mark(c)} ${c.text}${c.class === '' ? '' : ` [${c.class.toUpperCase()}]`}`

export const nextLine = (view: PlanView): string =>
  view.next === null ? 'next: none (last phase)' : `next: Phase ${view.next.n} — ${view.next.title}`

/** The /phase checklist as text: the command's output where no pane is placed. */
export const checklistLines = (view: PlanView, origin: string): readonly string[] => [
  titleLine(view),
  statusLine(view.phase),
  ...view.phase.criteria.map(criterionLine),
  nextLine(view),
  ...(view.url === '' ? [] : [absoluteUrl(view.url, origin)]),
]

export const NO_PLAN_TEXT = 'no active plan for this session (pick one with /phase <task-id>)'

/** What a tick offers: the open criteria, each under a label unique within the ask. */
export type TickChoice = { readonly label: string; readonly idx: number }

export const tickChoices = (view: PlanView): readonly TickChoice[] =>
  view.phase.criteria
    .filter(c => !c.done)
    .slice(0, ASK_OPTIONS_MAX)
    .map(c => ({ label: `${c.idx}. ${truncate(c.text, ASK_LABEL_LIMIT)}`, idx: c.idx }))

/** Whether a call of `tool` edits a file (Edit, Write, MultiEdit, NotebookEdit). */
export const isEditTool = (tool: string): boolean => FILE_TOOLS[tool] !== undefined

/** The path a main-loop edit call names, relative to `cwd`, or undefined. */
export const editedPath = (tool: string, fields: Readonly<Record<string, unknown>>, cwd: string): string | undefined => {
  const field = FILE_TOOLS[tool]
  const target = field === undefined ? undefined : fields[field]
  return typeof target === 'string' && target.trim() !== '' ? normalizePath(target, cwd) : undefined
}

/** The running turn as the drift check sees it: its id, the files it edited, whether it was classified. */
export type DriftTurn = {
  readonly turnId: string
  readonly edits: readonly string[]
  readonly isClassified: boolean
}

export const noteEdit = (turn: DriftTurn | undefined, path: string): DriftTurn | undefined =>
  turn === undefined || turn.edits.includes(path) ? turn : { ...turn, edits: [...turn.edits, path] }

/**
 * The classification's input: the phase and its open criteria for context,
 * the files the turn edited, and the tail of its answer. `$.model.classify`
 * takes no context of its own, so it rides in the text.
 */
export const driftInput = (phase: PlanPhase, answer: string, edits: readonly string[]): string => {
  const open = phase.criteria.filter(c => !c.done).map(c => `- ${truncate(c.text, 200)}`)
  return [
    `Running plan phase ${phase.n}: ${phase.title}`,
    'Open acceptance criteria:',
    ...(open.length === 0 ? ['- (none)'] : open.slice(0, 12)),
    'Files this turn edited:',
    ...edits.slice(0, 30).map(p => `- ${p}`),
    'The end of the turn\'s answer:',
    answer.slice(-DRIFT_ANSWER_LIMIT),
    'Label whether this turn\'s work is on this phase, adjacent to it, or off it.',
  ].join('\n')
}

/** Why the turn looks off-phase: `classify` names a label alone, so the edited files say it. */
export const driftReason = (edits: readonly string[]): string => truncate(`edited ${edits.join(', ')}`, DRIFT_REASON_LIMIT)

/** The drift line, cut as a whole to DRIFT_LINE_LIMIT characters. */
export const driftLine = (n: number, reason: string): string => truncate(`↯ this turn looks outside phase ${n}: ${reason}`, DRIFT_LINE_LIMIT)

/**
 * Whether a turn's edits are held against the view's phase: only a phase that
 * is running, or one the person picked in the terminal. An automatic view can
 * land on a Pending or Blocked phase (the first not Done), and no work is
 * "off" a phase nobody has started.
 */
export const isDriftWatched = (view: PlanView): boolean => view.phase.status === PHASE_IN_PROGRESS || view.chosenBy === 'terminal'
