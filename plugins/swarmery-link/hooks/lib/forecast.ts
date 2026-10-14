// The turn forecast loop (option `forecast`). Pure: every function takes the
// loop's state or a raw value and returns the next state or a payload. The
// `$` calls (the model call, the push into the sensor queue) are spelled in
// register.ts. The wire shapes are the link protocol's `turn.forecast` and
// `turn.actual` (tools/swarmery/docs/mods-link-protocol.md): paths and numbers
// only, never prompt text, tool input or model reply text.

/** The model asked: an alias the engine resolves like `--model`. */
export const FORECAST_MODEL = 'haiku'
/** The reply's token cap: a forecast is one short JSON object. */
export const FORECAST_MAX_TOKENS = 300
/** The model call's own time limit; past it the call resolves `aborted`. */
export const FORECAST_TIMEOUT_MS = 20_000
/** The most of the user's text the model is shown. */
export const PROMPT_TEXT_LIMIT = 2_000
/** Completed turns whose touched files the model is shown. */
export const HISTORY_TURNS = 3
/** The daemon's limits on `files`. */
export const MAX_FILES = 50
export const MAX_PATH = 300

/** A Bash command that runs tests (the protocol's pattern). */
export const TEST_COMMAND = /\b(test|vitest|jest|go test|pytest|cargo test|npm (run )?test)\b/

/** Tools whose input names the file they write, and the field that names it. */
const FILE_TOOLS: Readonly<Record<string, string>> = {
  Edit: 'file_path',
  Write: 'file_path',
  MultiEdit: 'file_path',
  NotebookEdit: 'notebook_path',
}

/** What the model claimed about the coming turn; null where it made no claim. */
export type Claims = {
  readonly files: readonly string[]
  readonly testsPass: boolean | null
  readonly steps: number | null
  readonly confidence: number | null
}

export const NO_CLAIMS: Claims = { files: [], testsPass: null, steps: null, confidence: null }

/** What the main loop did this turn, from its tool calls. */
export type Journal = {
  readonly files: readonly string[]
  readonly testsRan: boolean
  readonly testsPass: boolean | null
  readonly steps: number
}

export const EMPTY_JOURNAL: Journal = { files: [], testsRan: false, testsPass: null, steps: 0 }

/** The loop's state for one load of the module. */
export type ForecastState = {
  /** The main loop's turn count since session.start or a /clear. */
  readonly turnSeq: number
  /** Bumped on every reset, so a forecast in flight from before it does not reopen the gate. */
  readonly generation: number
  /** The running main-loop turn's id; undefined between turns. */
  readonly turnId: string | undefined
  /** This turn asked the model, so its actual is sent. */
  readonly isForecasted: boolean
  /** A forecast call is still in flight: the next turn is not forecast. */
  readonly isOpen: boolean
  readonly journal: Journal
  /** Files touched per completed turn, newest last, at most HISTORY_TURNS. */
  readonly history: ReadonlyArray<readonly string[]>
}

export const INITIAL_FORECAST: ForecastState = {
  turnSeq: 0,
  generation: 0,
  turnId: undefined,
  isForecasted: false,
  isOpen: false,
  journal: EMPTY_JOURNAL,
  history: [],
}

/** The state after session.start or a session.end with reason `clear`: numbering starts over. */
export const resetForecast = (state: ForecastState): ForecastState => ({
  ...INITIAL_FORECAST,
  generation: state.generation + 1,
})

/**
 * Whether a turn's text was typed by the person: empty text (a continuation)
 * and the forms a command or a notification arrives in are not.
 */
export const isUserText = (text: string): boolean => {
  const t = text.trim()
  return t !== '' && !/^(\/|<(command-|local-command|task-notification|system-reminder))/.test(t)
}

/** The state at a main-loop turn.start, and whether to ask the model for this turn. */
export const startTurn = (
  state: ForecastState,
  turnId: string,
  text: string,
  canSend: boolean,
): { readonly state: ForecastState; readonly shouldAsk: boolean } => {
  const shouldAsk = canSend && !state.isOpen && isUserText(text)
  return {
    shouldAsk,
    state: {
      ...state,
      turnSeq: state.turnSeq + 1,
      turnId,
      isForecasted: shouldAsk,
      isOpen: state.isOpen || shouldAsk,
      journal: EMPTY_JOURNAL,
    },
  }
}

/** The state once the forecast call of `generation` settled. */
export const closeForecast = (state: ForecastState, generation: number): ForecastState =>
  state.generation === generation ? { ...state, isOpen: false } : state

/** The state at the main loop's turn.complete, and the turn.actual payload when the turn was forecast. */
export const endTurn = (
  state: ForecastState,
  turnId: string,
): { readonly state: ForecastState; readonly actual: Readonly<Record<string, unknown>> | undefined } => {
  if (state.turnId === undefined || state.turnId !== turnId) return { state, actual: undefined }
  const { journal } = state
  const actual = state.isForecasted
    ? { turnSeq: state.turnSeq, files: journal.files, testsRan: journal.testsRan, testsPass: journal.testsPass, steps: journal.steps }
    : undefined
  return {
    actual,
    state: {
      ...state,
      turnId: undefined,
      isForecasted: false,
      journal: EMPTY_JOURNAL,
      history: [...state.history, journal.files].slice(-HISTORY_TURNS),
    },
  }
}

/** `path` with `./`, duplicate and trailing separators gone, relative to `cwd` when under it. */
export const normalizePath = (path: string, cwd: string): string => {
  const clean = (p: string): string =>
    p
      .trim()
      .replace(/\/{2,}/g, '/')
      .replace(/^(\.\/)+/, '')
      .replace(/(.)\/$/, '$1')
  const p = clean(path)
  const root = clean(cwd)
  return root !== '' && root !== '/' && p.startsWith(`${root}/`) ? p.slice(root.length + 1) : p
}

/**
 * Whether `p` can stand on the wire as a path: one token of at most MAX_PATH
 * characters naming a directory or an extension. A model that echoes prose
 * into `files` has it dropped here, so prompt text never rides along.
 */
const isWirePath = (p: string): boolean =>
  p.length > 0 && p.length <= MAX_PATH && !/[\s\u0000-\u001f<>"'`|;]/.test(p) && /[./]/.test(p)

/** The paths added to `files` in order, each once, valid ones only, at most MAX_FILES. */
const addFiles = (files: readonly string[], more: readonly string[]): readonly string[] => {
  const out = [...files]
  for (const p of more) {
    if (out.length >= MAX_FILES) break
    if (isWirePath(p) && !out.includes(p)) out.push(p)
  }
  return out
}

/** A tool call as the journal reads it: the tool, its loop, and the input fields it may name. */
export type ToolCallFacts = {
  readonly tool: string
  readonly agentId?: string | undefined
  readonly fields: Readonly<Record<string, unknown>>
}

/** How the call ended, from `next(e)`'s result. */
export type ToolOutcome = 'ok' | 'error' | 'denied'

/**
 * The journal after one tool call of the running turn ended with `outcome`.
 * A subagent's call (agentId present), or a call outside a turn, leaves it as
 * it was. Every main-loop call is a step; a file counts once its write
 * succeeded; a test command run in the foreground sets testsRan and testsPass
 * (the last one wins: a red run fixed later in the turn reads as passing).
 */
export const noteToolCall = (state: ForecastState, call: ToolCallFacts, outcome: ToolOutcome, cwd: string): ForecastState => {
  if (state.turnId === undefined || call.agentId !== undefined) return state
  let journal: Journal = { ...state.journal, steps: state.journal.steps + 1 }
  const field = FILE_TOOLS[call.tool]
  const target = field === undefined ? undefined : call.fields[field]
  if (typeof target === 'string' && outcome === 'ok') {
    journal = { ...journal, files: addFiles(journal.files, [normalizePath(target, cwd)]) }
  }
  const command = call.tool === 'Bash' ? call.fields.command : undefined
  if (typeof command === 'string' && TEST_COMMAND.test(command) && outcome !== 'denied') {
    const isBackground = call.fields.run_in_background === true
    journal = { ...journal, testsRan: true, testsPass: isBackground ? journal.testsPass : outcome === 'ok' }
  }
  return { ...state, journal }
}

/** The model's request: a short fixed instruction, the user's text and the files of the last turns. */
export const forecastRequest = (text: string, history: ReadonlyArray<readonly string[]>): { readonly system: string; readonly prompt: string } => {
  const recent = [...new Set(history.flat())]
  const shown = text.length > PROMPT_TEXT_LIMIT ? `${text.slice(0, PROMPT_TEXT_LIMIT)}…` : text
  return {
    system: [
      'You forecast what a coding agent will do while it answers the request below.',
      'Reply with one JSON object and nothing else:',
      '{"files": [paths it will create or edit, relative to the repository root], "testsPass": true | false | null (null when it will not run tests),',
      '"steps": number of tool calls it will make, "confidence": 0..1 that the forecast is right}.',
    ].join(' '),
    prompt: [
      'Request:',
      shown,
      '',
      'Files edited in the previous turns:',
      recent.length > 0 ? recent.map(p => `- ${p}`).join('\n') : '(none)',
    ].join('\n'),
  }
}

/** The first balanced `{...}` in `text`, strings respected; undefined when there is none. */
const firstObject = (text: string): string | undefined => {
  const start = text.indexOf('{')
  if (start < 0) return undefined
  let depth = 0
  let inString = false
  let escaped = false
  for (let i = start; i < text.length; i += 1) {
    const c = text[i]
    if (inString) {
      if (escaped) escaped = false
      else if (c === '\\') escaped = true
      else if (c === '"') inString = false
      continue
    }
    if (c === '"') inString = true
    else if (c === '{') depth += 1
    else if (c === '}') {
      depth -= 1
      if (depth === 0) return text.slice(start, i + 1)
    }
  }
  return undefined
}

const isRecord = (v: unknown): v is Readonly<Record<string, unknown>> => typeof v === 'object' && v !== null && !Array.isArray(v)

/**
 * The claims in a model reply: the first JSON object in it, each field held to
 * the protocol's limits; a field out of shape is null, a reply with no
 * readable object is NO_CLAIMS.
 */
export const parseForecast = (reply: string | undefined, cwd: string): Claims => {
  const raw = reply === undefined ? undefined : firstObject(reply)
  if (raw === undefined) return NO_CLAIMS
  let value: unknown
  try {
    value = JSON.parse(raw)
  } catch {
    return NO_CLAIMS
  }
  if (!isRecord(value)) return NO_CLAIMS
  const files = Array.isArray(value.files)
    ? addFiles(
        [],
        value.files.filter((f): f is string => typeof f === 'string').map(f => normalizePath(f, cwd)),
      )
    : []
  const steps = typeof value.steps === 'number' && Number.isFinite(value.steps) ? Math.max(0, Math.round(value.steps)) : null
  const confidence =
    typeof value.confidence === 'number' && Number.isFinite(value.confidence) ? Math.min(1, Math.max(0, value.confidence)) : null
  return {
    files,
    testsPass: typeof value.testsPass === 'boolean' ? value.testsPass : null,
    steps,
    confidence,
  }
}

/** The four token counts of a model call, as ModelUsage spells them. */
export type TokenUsage = {
  readonly input_tokens: number
  readonly output_tokens: number
  readonly cache_read_input_tokens: number
  readonly cache_creation_input_tokens: number
}

/** Every token the call was billed for, input (cached or not) and output; null when the reply carried no usage. */
export const usageTokens = (usage: TokenUsage | undefined): number | null => {
  if (usage === undefined) return null
  const sum = usage.input_tokens + usage.output_tokens + usage.cache_read_input_tokens + usage.cache_creation_input_tokens
  return Number.isFinite(sum) && sum >= 0 ? Math.round(sum) : null
}

/** The turn.forecast payload. */
export const forecastPayload = (
  turnSeq: number,
  claims: Claims,
  model: string,
  forecastTokens: number | null,
): Readonly<Record<string, unknown>> => ({
  turnSeq,
  files: claims.files,
  testsPass: claims.testsPass,
  steps: claims.steps,
  confidence: claims.confidence,
  model: model.slice(0, 80),
  forecastTokens,
})
