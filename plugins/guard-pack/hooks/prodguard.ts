// Production-deploy guard: a tool call matching a production-deploy pattern
// is put to the native ask dialog, whatever the permission mode allowed.
// Additive to any shell hook or daemon guard doing the same; a deny beneath
// stays a deny.
//
// Matching is a port of the daemon's approval-rule matcher: `Tool` or
// `Tool(argGlob)`; the tool part exact, the glob over the tool's argument
// string, case-insensitive, `*` any run of characters and nothing else
// special. The shipped list is hooks/patterns/prod.json; the project's own
// .claude/project.json approvals.prodDeployPatterns are added to it.

import type { EngineInterface, On } from 'claude-code'
import { type ProdPattern, compileProd, createPatternCache } from './patterns'
import type { ContextCache } from './roots'
import { quietly } from './util'

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

const projectPatterns = async ($: EngineInterface, cache: ContextCache): Promise<ProdPattern[]> => {
  const ctx = await cache.get($)
  const approvals = ctx.project?.json.approvals
  if (typeof approvals !== 'object' || approvals === null) return []
  return compileProd((approvals as Readonly<Record<string, unknown>>).prodDeployPatterns)
}

export const registerProdGuard = (on: On, cache: ContextCache): void => {
  const defaults = createPatternCache('prod.json', compileProd)

  on('tool.check', async ($, e, next) => {
    const below = await next(e)
    if (below.decision === 'deny') return below
    let hit: ProdPattern | undefined
    try {
      const patterns = [...(await defaults($)), ...(await projectPatterns($, cache))]
      hit = matchProd(patterns, String(e.tool), e.input)
    } catch {
      hit = undefined
    }
    if (hit === undefined) return below
    const reason = `guard-pack: production deploy (matches ${hit.source}); confirm it yourself`
    const id = e.tool_use_id
    if (id !== undefined) quietly(() => $.ui.notice(id, reason))
    return { decision: 'ask', reason }
  }).catch(($, e, next) => next(e))
}
