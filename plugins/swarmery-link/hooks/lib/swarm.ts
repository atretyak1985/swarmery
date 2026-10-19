// The swarm's decisions. Pure: reads the path a file tool writes, keeps this
// session's claims entry for the shared `$.store` registry, finds another
// session's live claim on the same path, builds the `session.send`,
// `session.receive`, `claim`, `release` and `conflict` payloads, the line
// shown beneath the answer, and the `claimConflicts` verdict on a write
// another session holds. Every `$` call is made in ../swarm.ts and
// ../register.ts. Contract: tools/swarmery/docs/mods-link-protocol.md.

import { FILE_TOOLS } from './forecast'

/** Every key this plugin writes to `$.store` sits under this prefix. */
export const STORE_PREFIX = 'swarmery-link/'
/** One key per session: `swarmery-link/claims/<sessionId>`, written by that session alone. */
export const CLAIMS_PREFIX = `${STORE_PREFIX}claims/`
/** Another session's entry counts while its `updatedAt` is this recent. */
export const CLAIM_TTL_MS = 30 * 60_000
/** This session's entry stays under this many bytes of JSON; the oldest paths go first. */
export const MAX_ENTRY_BYTES = 64 * 1024
/** A message's text on the wire, at most this many UTF-16 code units. */
export const MAX_MESSAGE_TEXT = 4096
/** The receive log line quotes at most this many characters of the text. */
export const RECEIVE_PREVIEW = 80
/** A short session id, as the conflict line shows it. */
export const SHORT_ID = 8

export const claimsKey = (sessionId: string): string => `${CLAIMS_PREFIX}${sessionId}`

export type ClaimedPath = { readonly path: string; readonly at: number }

/** One session's claims, as `$.store` holds them: paths and ms timestamps only. */
export type ClaimEntry = {
  readonly sessionId: string
  readonly cwd: string
  readonly updatedAt: number
  readonly paths: readonly ClaimedPath[]
}

export type Conflict = { readonly path: string; readonly otherSession: string }

/** The swarm's state for one main-loop turn. */
export type SwarmTurn = {
  /** Paths claimed since the last release, each once, in claim order. */
  readonly claimed: readonly string[]
  /** Conflicts found this turn, in the order found. */
  readonly conflicts: readonly Conflict[]
  /** This session's entry as last written, or undefined when none is stored. */
  readonly entry: ClaimEntry | undefined
}

export const EMPTY_TURN: SwarmTurn = { claimed: [], conflicts: [], entry: undefined }

const isRecord = (value: unknown): value is Readonly<Record<string, unknown>> =>
  typeof value === 'object' && value !== null && !Array.isArray(value)

/** `path` made absolute against `cwd`, with `.`, `..`, empty and trailing segments resolved. */
export const absolutePath = (path: string, cwd: string): string => {
  const raw = path.trim()
  const joined = raw.startsWith('/') ? raw : `${cwd.replace(/\/+$/, '')}/${raw}`
  const parts: string[] = []
  for (const part of joined.split('/')) {
    if (part === '' || part === '.') continue
    if (part === '..') parts.pop()
    else parts.push(part)
  }
  return `/${parts.join('/')}`
}

/**
 * The absolute path an Edit, Write, MultiEdit or NotebookEdit call writes, or
 * undefined for any other call (or one naming no path). A relative path is
 * resolved against the session's cwd; with no absolute cwd it is not claimed.
 */
export const claimPath = (tool: string, fields: Readonly<Record<string, unknown>>, cwd: string): string | undefined => {
  const field = FILE_TOOLS[tool]
  const target = field === undefined ? undefined : fields[field]
  if (typeof target !== 'string' || target.trim() === '') return undefined
  if (!target.trim().startsWith('/') && !cwd.startsWith('/')) return undefined
  return absolutePath(target, cwd)
}

const bytes = (value: unknown): number => new TextEncoder().encode(JSON.stringify(value)).length

/** `entry` with its oldest paths dropped until its JSON fits MAX_ENTRY_BYTES (`max` for tests). */
export const capEntry = (entry: ClaimEntry, max: number = MAX_ENTRY_BYTES): ClaimEntry => {
  let paths = entry.paths
  while (paths.length > 0 && bytes({ ...entry, paths }) > max) paths = paths.slice(1)
  return paths === entry.paths ? entry : { ...entry, paths }
}

/** This session's entry with `path` claimed at `now` (refreshed when already held), under the size cap. */
export const addClaim = (
  entry: ClaimEntry | undefined,
  sessionId: string,
  cwd: string,
  path: string,
  now: number,
  max: number = MAX_ENTRY_BYTES,
): ClaimEntry => {
  const kept = (entry?.paths ?? []).filter(p => p.path !== path)
  return capEntry({ sessionId, cwd, updatedAt: now, paths: [...kept, { path, at: now }] }, max)
}

/** A store value read back as a claims entry, or undefined when it is not one. */
export const parseEntry = (value: unknown): ClaimEntry | undefined => {
  if (!isRecord(value)) return undefined
  const { sessionId, cwd, updatedAt, paths } = value
  if (typeof sessionId !== 'string' || sessionId === '' || typeof updatedAt !== 'number' || !Array.isArray(paths)) return undefined
  const clean = paths.flatMap((p: unknown): ClaimedPath[] =>
    isRecord(p) && typeof p.path === 'string' && typeof p.at === 'number' ? [{ path: p.path, at: p.at }] : [],
  )
  return { sessionId, cwd: typeof cwd === 'string' ? cwd : '', updatedAt, paths: clean }
}

/** Whether `entry` is live at `now`: updated within the TTL (a clock behind by a little still counts). */
export const isLive = (entry: ClaimEntry, now: number): boolean => now - entry.updatedAt <= CLAIM_TTL_MS

/** The store keys of other sessions' claims entries. */
export const foreignKeys = (keys: readonly string[], sessionId: string): readonly string[] =>
  keys.filter(k => k.startsWith(CLAIMS_PREFIX) && k !== claimsKey(sessionId))

/** Each other session whose live entry holds `path`, once. */
export const conflictsFor = (path: string, sessionId: string, others: readonly ClaimEntry[], now: number): readonly Conflict[] => {
  const seen = new Set<string>()
  const out: Conflict[] = []
  for (const other of others) {
    if (other.sessionId === sessionId || seen.has(other.sessionId) || !isLive(other, now)) continue
    if (!other.paths.some(p => p.path === path)) continue
    seen.add(other.sessionId)
    out.push({ path, otherSession: other.sessionId })
  }
  return out
}

/** The turn after the first claim of `path`: whether it is new this turn, and the turn with it noted. */
export const noteClaim = (turn: SwarmTurn, path: string): { readonly isNew: boolean; readonly turn: SwarmTurn } =>
  turn.claimed.includes(path) ? { isNew: false, turn } : { isNew: true, turn: { ...turn, claimed: [...turn.claimed, path] } }

/** The daemon refuses a `release` listing more paths than this (maxReleasePaths). */
export const MAX_RELEASE_PATHS = 500

/**
 * The turn-end `release` payload: the turn's paths, or `{ all: true }` when
 * they are more than the daemon takes in one release. Both free the same
 * claims: every claim the session holds was taken this turn, since each
 * turn's end releases them all.
 */
export const releasePayload = (claimed: readonly string[]): Readonly<Record<string, unknown>> =>
  claimed.length > MAX_RELEASE_PATHS ? { all: true } : { paths: claimed }

/**
 * A command id as the wire carries it: a JSON number when the poll's id is a
 * safe integer (the protocol's form), else the id as given.
 */
export const wireCommandId = (id: string): number | string => {
  const n = Number(id)
  return /^[0-9]+$/.test(id) && Number.isSafeInteger(n) ? n : id
}

/** At most `max` code units of `text`, never splitting a surrogate pair. */
export const clip = (text: string, max: number = MAX_MESSAGE_TEXT): { readonly text: string; readonly truncated: boolean } => {
  if (text.length <= max) return { text, truncated: false }
  const code = text.charCodeAt(max - 1)
  const end = code >= 0xd800 && code <= 0xdbff ? max - 1 : max
  return { text: text.slice(0, end), truncated: true }
}

export type SendFacts = {
  readonly to: string
  readonly text: string
  readonly origin: 'model' | 'plugin'
  readonly isDelivered: boolean
  readonly reason?: string | undefined
  readonly commandId?: string | undefined
}

/** The `session.send` payload: `reason` only when not delivered, `commandId` (a number) only for a dashboard message. */
export const sendPayload = (facts: SendFacts): Readonly<Record<string, unknown>> => {
  const { text, truncated } = clip(facts.text)
  return {
    to: facts.to,
    text,
    truncated,
    isDelivered: facts.isDelivered,
    ...(!facts.isDelivered ? { reason: facts.reason ?? '' } : {}),
    origin: facts.origin,
    ...(facts.commandId !== undefined ? { commandId: wireCommandId(facts.commandId) } : {}),
  }
}

/** A dashboard message whose `$.session.send` has not settled yet. */
export type PendingSend = { readonly commandId: string; readonly to: string; readonly text: string; isLogged: boolean }

/** The unreported pending dashboard messages with this text, oldest first. */
const openWith = (pending: ReadonlyMap<string, PendingSend>, text: string): readonly PendingSend[] =>
  [...pending.values()].filter(p => !p.isLogged && p.text === text)

/**
 * The pending dashboard message a plugin send of `to`/`text` is, if any: the
 * oldest unreported one with that text addressed as the engine spelled `to`,
 * else the only unreported one with that text. Two or more with that text and
 * none addressed as `to` cannot be told apart: undefined, never a guess that
 * could swap their command ids.
 */
export const pendingFor = (pending: ReadonlyMap<string, PendingSend>, to: string, text: string): PendingSend | undefined => {
  const open = openWith(pending, text)
  return open.find(p => p.to === to) ?? (open.length === 1 ? open[0] : undefined)
}

/**
 * Whether a plugin send of `to`/`text` is one of several pending dashboard
 * messages that `pendingFor` cannot tell apart. The send hook then leaves the
 * report to each message's own path, which knows its command id.
 */
export const isAmbiguousSend = (pending: ReadonlyMap<string, PendingSend>, to: string, text: string): boolean =>
  pendingFor(pending, to, text) === undefined && openWith(pending, text).length > 1

/** The plugin named on a delivery's origin, when it names one. */
export const originPlugin = (origin: unknown): string | undefined => {
  const plugin = isRecord(origin) ? origin.plugin : undefined
  return typeof plugin === 'string' && plugin !== '' ? plugin : undefined
}

const originKind = (origin: unknown): string => {
  const kind = isRecord(origin) ? origin.kind : undefined
  return typeof kind === 'string' ? kind : 'unclassified'
}

// The engine hands the receiver a peer's message wrapped in an envelope that
// names the sender, while the sender's send hook sees the bare text:
//   <cross-session-message from="uds:/tmp/cc-socks/41881.sock" from-name="proj-10" from-mode="bypass">
//   ping-from-A
//   </cross-session-message>
const ENVELOPE_OPEN = /^\s*<cross-session-message((?:\s+[^\s=>"']+\s*=\s*(?:"[^"]*"|'[^']*'))*)\s*>/
const ENVELOPE_CLOSE = /<\/cross-session-message\s*>\s*$/
const ENVELOPE_ATTR = /([^\s=>"']+)\s*=\s*(?:"([^"]*)"|'([^']*)')/g
/** The engine names a session's messaging socket after its process id. */
const SOCKET_PID = /^uds:.*\/([0-9]+)\.sock$/

export type Unwrapped = { readonly body: string; readonly from: string | undefined }

/**
 * `text` without the engine's cross-session envelope: the body between the
 * tags with the whitespace around it trimmed, and the envelope's `from`.
 * Attributes may come in any order, with others among them. Undefined when
 * `text` is not one whole envelope.
 */
export const unwrapEnvelope = (text: string): Unwrapped | undefined => {
  const open = ENVELOPE_OPEN.exec(text)
  if (open === null) return undefined
  const rest = text.slice(open[0].length)
  const close = ENVELOPE_CLOSE.exec(rest)
  if (close === null) return undefined
  let from: string | undefined
  for (const attr of (open[1] ?? '').matchAll(ENVELOPE_ATTR)) {
    if (attr[1] === 'from') from = attr[2] ?? attr[3]
  }
  return { body: rest.slice(0, close.index).trim(), from }
}

/**
 * The sending process's id: the delivery's `verifiedPeerPid` (the engine
 * checked it), else the one its `from` socket (the origin's or the
 * envelope's) is named after. Undefined when neither names one.
 */
export const senderPid = (origin: unknown, envelopeFrom: string | undefined): number | undefined => {
  const verified = isRecord(origin) ? origin.verifiedPeerPid : undefined
  if (typeof verified === 'number' && Number.isSafeInteger(verified) && verified > 0) return verified
  const originFrom = isRecord(origin) && typeof origin.from === 'string' ? origin.from : undefined
  const digits = SOCKET_PID.exec(originFrom ?? envelopeFrom ?? '')?.[1]
  const pid = digits === undefined ? 0 : Number(digits)
  return Number.isSafeInteger(pid) && pid > 0 ? pid : undefined
}

/**
 * The `session.receive` payload: the text as the sender wrote it (the
 * envelope taken off) and, when the engine names it, the sender's `fromPid`.
 */
export const receivePayload = (text: string, origin: unknown): Readonly<Record<string, unknown>> => {
  const unwrapped = unwrapEnvelope(text)
  const clipped = clip(unwrapped?.body ?? text)
  const plugin = originPlugin(origin)
  const pid = senderPid(origin, unwrapped?.from)
  return {
    text: clipped.text,
    truncated: clipped.truncated,
    originKind: originKind(origin),
    ...(plugin !== undefined ? { fromPlugin: plugin } : {}),
    ...(pid !== undefined ? { fromPid: pid } : {}),
  }
}

/** The one log line for a received delivery: `from <kind>[ <plugin>]: <first 80 chars, on one line>`. */
export const receiveLine = (text: string, origin: unknown): string => {
  const plugin = originPlugin(origin)
  const body = unwrapEnvelope(text)?.body ?? text
  const preview = clip(body.replace(/\s+/g, ' ').trim(), RECEIVE_PREVIEW).text
  return `from ${originKind(origin)}${plugin !== undefined ? ` ${plugin}` : ''}: ${preview}`
}

/**
 * The `claimConflicts` option: `off` reports a conflict only (the line beneath
 * the answer), `ask` puts the write to the person, `deny` refuses it.
 */
export type ClaimConflicts = 'off' | 'ask' | 'deny'

/** The option's value as set; anything but `ask` or `deny` is `off`. */
export const claimConflictsOf = (value: unknown): ClaimConflicts => (value === 'ask' || value === 'deny' ? value : 'off')

// Permission modes in which an `ask` reaches nobody: `auto` hands it to the
// classifier, `dontAsk` refuses it unasked, `bypassPermissions` is a mode where
// nobody expects a prompt. guard-pack's prodGuard reads them the same way.
const UNATTENDED_MODES: ReadonlySet<string> = new Set(['auto', 'dontAsk', 'bypassPermissions'])

/** Whether an ask in `mode` reaches a person; a mode not seen yet keeps the ask. */
export const asksReachPerson = (mode: string | undefined): boolean => mode === undefined || !UNATTENDED_MODES.has(mode)

// The session's permission mode is not on `tool.check`'s input. Every classic
// (settings) hook event but PreToolUse carries it as `permission_mode`, with
// `agent_id` inside a subagent's loop, so the last one seen is kept per loop.
// A subagent not yet heard from takes the main loop's.
const MAIN_LOOP = ''

export type ModeBook = Map<string, string>

/** Records `e.permission_mode` under its loop; ignores anything else. */
export const noteMode = (book: ModeBook, e: unknown): void => {
  if (!isRecord(e)) return
  const mode = e.permission_mode
  if (typeof mode !== 'string' || mode === '') return
  book.set(typeof e.agent_id === 'string' && e.agent_id !== '' ? e.agent_id : MAIN_LOOP, mode)
}

/** The mode a call in loop `agentId` (absent on the main loop) is decided under; undefined when none was seen. */
export const modeOf = (book: ReadonlyMap<string, string>, agentId: string | undefined): string | undefined =>
  (agentId !== undefined ? book.get(agentId) : undefined) ?? book.get(MAIN_LOOP)

/** Why a write is held: `swarmery-link: <path> is being edited by session <short id>`, `(+N more)` with several. */
export const conflictReason = (conflicts: readonly Conflict[]): string | undefined => {
  const first = conflicts[0]
  if (first === undefined) return undefined
  const more = conflicts.length > 1 ? ` (+${conflicts.length - 1} more)` : ''
  return `swarmery-link: ${first.path} is being edited by session ${first.otherSession.slice(0, SHORT_ID)}${more}`
}

export type ConflictVerdict = { readonly decision: 'ask' | 'deny'; readonly reason: string }

/**
 * The `tool.check` verdict for a write another live session holds the path
 * of: none with `off` or no conflict; `deny` with `deny`; with `ask`, `ask`
 * where a person sees the dialog and `deny` in a mode where nobody would.
 */
export const conflictVerdict = (setting: ClaimConflicts, mode: string | undefined, conflicts: readonly Conflict[]): ConflictVerdict | undefined => {
  const reason = conflictReason(conflicts)
  if (setting === 'off' || reason === undefined) return undefined
  return setting === 'ask' && asksReachPerson(mode) ? { decision: 'ask', reason } : { decision: 'deny', reason }
}

/** The line shown beneath the answer for this turn's conflicts; none without one. */
export const conflictLines = (conflicts: readonly Conflict[]): readonly string[] => {
  const first = conflicts[0]
  if (first === undefined) return []
  const more = conflicts.length > 1 ? ` (+${conflicts.length - 1} more)` : ''
  return [`⚠ ${first.path} is also being edited by session ${first.otherSession.slice(0, SHORT_ID)}${more}`]
}

/**
 * A `turn.complete` result with `lines` shown beneath the answer. The engine
 * shows a returned text other than the main-loop answer beneath it and never
 * rewrites the answer itself, so the text returned is the lines alone; a text
 * a hook beneath already put there (not the answer) is kept, the lines after it.
 */
export const withBeneath = <R extends { readonly text: string }>(result: R, answer: string, lines: readonly string[]): R => {
  if (lines.length === 0) return result
  const own = lines.join('\n')
  const text = result.text !== answer && result.text !== '' ? `${result.text}\n${own}` : own
  return { ...result, text }
}
