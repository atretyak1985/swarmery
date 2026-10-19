// Production-deploy guard: a tool call matching a production-deploy pattern
// is put to the native ask dialog, whatever the permission rules allowed, and
// refused outright in a permission mode where that ask would reach nobody
// (auto, dontAsk, bypassPermissions). Additive to any shell hook or daemon
// guard doing the same; a deny beneath stays a deny.
//
// Matching is a port of the daemon's approval-rule matcher: `Tool` or
// `Tool(argGlob)`; the tool part exact, the glob over the tool's argument
// string, case-insensitive, `*` any run of characters and nothing else
// special. The shipped list is hooks/patterns/prod.json; the project's own
// .claude/project.json approvals.prodDeployPatterns are added to it.

import { type ProdPattern, compileProd } from './patterns'
import type { ProjectConfig } from './roots'

/** `*` matches any run of characters (including `/`, spaces); the rest is literal. */
export const globMatch = (pattern: string, s: string): boolean => {
  const parts = pattern.split('*')
  if (parts.length === 1) return s === pattern
  const first = parts[0] ?? ''
  if (!s.startsWith(first)) return false
  let rest = s.slice(first.length)
  for (const mid of parts.slice(1, -1)) {
    if (mid === '') continue
    const i = rest.indexOf(mid)
    if (i < 0) return false
    rest = rest.slice(i + mid.length)
  }
  const last = parts[parts.length - 1] ?? ''
  return last === '' || rest.endsWith(last)
}

// The tool → argument-field allow-list; a tool not listed has no argument
// string, so a Tool(argGlob) pattern never matches it.
const ARG_FIELDS: Readonly<Record<string, string>> = {
  Bash: 'command',
  Read: 'file_path',
  Write: 'file_path',
  Edit: 'file_path',
  WebFetch: 'url',
  Glob: 'pattern',
  Grep: 'pattern',
}

export const argOf = (tool: string, input: unknown): string | undefined => {
  const field = ARG_FIELDS[tool]
  if (field === undefined || typeof input !== 'object' || input === null) return undefined
  const v = (input as Readonly<Record<string, unknown>>)[field]
  return typeof v === 'string' && v !== '' ? v : undefined
}

export const matchProd = (
  patterns: readonly ProdPattern[],
  tool: string,
  input: unknown,
): ProdPattern | undefined => {
  const arg = argOf(tool, input)?.toLowerCase()
  return patterns.find(p => p.tool === tool && (p.inner === undefined || (arg !== undefined && globMatch(p.inner, arg))))
}

/** The project's own approvals.prodDeployPatterns, added to the shipped list. */
export const projectProdPatterns = (project: ProjectConfig | undefined): ProdPattern[] => {
  const approvals = project?.json.approvals
  if (typeof approvals !== 'object' || approvals === null) return []
  return compileProd((approvals as Readonly<Record<string, unknown>>).prodDeployPatterns)
}

export const prodReason = (hit: ProdPattern): string =>
  `guard-pack: production deploy (matches ${hit.source}); confirm it yourself`

// Permission modes in which an `ask` reaches nobody: `auto` hands it to the
// classifier, which settles it silently (observed on Claude Code 2.1.296,
// interactive and -p alike); `dontAsk` refuses it unasked; `bypassPermissions`
// is a mode where nobody expects a prompt. A production call is refused there
// outright. Every other mode, and a mode not yet seen, keeps the ask.
const UNATTENDED_MODES: ReadonlySet<string> = new Set(['auto', 'dontAsk', 'bypassPermissions'])

export const asksReachPerson = (mode: string | undefined): boolean =>
  mode === undefined || !UNATTENDED_MODES.has(mode)

export const prodDenyReason = (hit: ProdPattern, mode: string): string =>
  `guard-pack: production deploy (matches ${hit.source}) refused in ${mode} permission mode, ` +
  'where no confirmation dialog would reach a person. Ask the operator to run it from a ' +
  'session in default permission mode (shift+tab out of auto), where it is confirmed in the native dialog.'

export type ProdVerdict = { readonly decision: 'ask' | 'deny'; readonly reason: string }

/** `ask` where a person sees the dialog, `deny` in a mode where nobody would. */
export const prodVerdict = (hit: ProdPattern, mode: string | undefined): ProdVerdict =>
  asksReachPerson(mode)
    ? { decision: 'ask', reason: prodReason(hit) }
    : { decision: 'deny', reason: prodDenyReason(hit, mode ?? '') }

// The session's permission mode is not on `tool.check`'s input. Every classic
// (settings) hook event but PreToolUse carries it as `permission_mode`, with
// `agent_id` inside a subagent's loop, so the guard keeps the last one seen
// per loop. A subagent not yet heard from inherits the main loop's mode.
const MAIN_LOOP = ''

export type ModeBook = Map<string, string>

/** Records `e.permission_mode` under its loop; ignores anything else. */
export const noteMode = (book: ModeBook, e: unknown): void => {
  if (typeof e !== 'object' || e === null) return
  const r = e as Readonly<Record<string, unknown>>
  const mode = r.permission_mode
  if (typeof mode !== 'string' || mode === '') return
  book.set(typeof r.agent_id === 'string' && r.agent_id !== '' ? r.agent_id : MAIN_LOOP, mode)
}

/** The mode a call in loop `agentId` (absent on main) is decided under. */
export const modeOf = (book: ModeBook, agentId: string | undefined): string | undefined =>
  (agentId !== undefined ? book.get(agentId) : undefined) ?? book.get(MAIN_LOOP)
