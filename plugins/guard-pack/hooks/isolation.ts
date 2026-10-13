// Isolation: file tools stay inside the session's roots; destructive Bash
// (rm/mv/cp, a write redirect, cd) may not name an absolute path outside
// them. Lexical only (see paths.ts). A miss is preferred to a false deny:
// anything the heuristics cannot read (a `$VAR`, a relative path in Bash)
// passes.

import type { EngineInterface, On } from 'claude-code'
import { isDevicePath, isInsideAny, normalize } from './paths'
import { type ContextCache, type GuardContext, describeRoots } from './roots'

const HINT = "add it to guard-pack's extraRoots option if it belongs to this project"

const outsideReason = (path: string, ctx: GuardContext, what: string): string =>
  `guard-pack: ${what}${path} is outside this project's roots (${describeRoots(ctx.roots)}); ${HINT}.`

const GLOB_CHARS = /[*?[{]/

/** The part of a path before its first glob character, folded to a directory. */
const staticPrefix = (path: string): string => {
  const i = path.search(GLOB_CHARS)
  if (i < 0) return path
  const head = path.slice(0, i)
  return head.slice(0, head.lastIndexOf('/') + 1) || '/'
}

const str = (v: unknown): string | undefined => (typeof v === 'string' && v.trim() !== '' ? v : undefined)

/** The paths one file-tool call names, by tool. */
export const fileToolPaths = (tool: string, input: Readonly<Record<string, unknown>>): string[] => {
  switch (tool) {
    case 'Read':
    case 'Edit':
    case 'Write':
    case 'MultiEdit':
      return [str(input.file_path)].filter((p): p is string => p !== undefined)
    case 'NotebookEdit':
      return [str(input.notebook_path)].filter((p): p is string => p !== undefined)
    case 'Glob': {
      const out: string[] = []
      const path = str(input.path)
      if (path !== undefined) out.push(path)
      const pattern = str(input.pattern)
      if (pattern !== undefined && (pattern.startsWith('/') || pattern.startsWith('~'))) {
        out.push(staticPrefix(pattern))
      }
      return out
    }
    case 'Grep':
      return [str(input.path)].filter((p): p is string => p !== undefined)
    default:
      return []
  }
}

export const FILE_TOOLS: ReadonlySet<string> = new Set([
  'Read',
  'Edit',
  'Write',
  'MultiEdit',
  'NotebookEdit',
  'Glob',
  'Grep',
])

type Segment = { words: string[]; writes: string[] }

/**
 * Splits a command into simple commands: their words (quotes removed) and
 * the targets of their output redirects. Not a shell parser: enough to read
 * `rm -rf /x`, `cd ~/y`, `cmd > /z`, `a && b`, `2>&1`.
 */
export const lexCommand = (command: string): Segment[] => {
  const segments: Segment[] = []
  let seg: Segment = { words: [], writes: [] }
  let word = ''
  let inWord = false
  let pending: 'write' | 'read' | undefined

  const endWord = (): void => {
    if (!inWord) return
    if (pending === 'write') seg.writes.push(word)
    else if (pending === undefined) seg.words.push(word)
    pending = undefined
    word = ''
    inWord = false
  }
  const endSegment = (): void => {
    endWord()
    pending = undefined
    if (seg.words.length > 0 || seg.writes.length > 0) segments.push(seg)
    seg = { words: [], writes: [] }
  }

  for (let i = 0; i < command.length; i++) {
    const c = command[i] ?? ''
    if (c === '\\') {
      word += command[i + 1] ?? ''
      inWord = true
      i++
    } else if (c === "'") {
      const end = command.indexOf("'", i + 1)
      const stop = end < 0 ? command.length : end
      word += command.slice(i + 1, stop)
      inWord = true
      i = stop
    } else if (c === '"') {
      let j = i + 1
      while (j < command.length && command[j] !== '"') {
        if (command[j] === '\\' && j + 1 < command.length) j++
        word += command[j] ?? ''
        j++
      }
      inWord = true
      i = j
    } else if (c === ' ' || c === '\t') {
      endWord()
    } else if (c === '\n' || c === ';' || c === '|' || c === '(' || c === ')') {
      endSegment()
    } else if (c === '&') {
      if (command[i + 1] === '>') {
        endWord()
        i += command[i + 2] === '>' ? 2 : 1
        pending = 'write'
      } else {
        endSegment()
      }
    } else if (c === '>' || c === '<') {
      // a word made only of digits right before is the fd (`2>`), not an argument
      if (inWord && /^\d+$/.test(word)) {
        word = ''
        inWord = false
      } else {
        endWord()
      }
      if (c === '>' && command[i + 1] === '>') i++
      if (command[i + 1] === '&') {
        // `>&1`, `2>&1`: a dup, no file
        i++
        while (/\d|-/.test(command[i + 1] ?? '')) i++
        pending = undefined
        continue
      }
      pending = c === '>' ? 'write' : 'read'
    } else {
      word += c
      inWord = true
    }
  }
  endSegment()
  return segments
}

const PREFIX_WORDS = new Set(['sudo', 'command', 'builtin', 'exec', 'nohup', 'time'])
const DESTRUCTIVE = new Set(['rm', 'mv', 'cp'])

const isAbsoluteSpelling = (w: string): boolean =>
  !w.includes('$') && !w.includes('`') && (w.startsWith('/') || w === '~' || w.startsWith('~/'))

/** The first absolute path a destructive part of `command` names outside the roots. */
export const bashViolation = (
  command: string,
  ctx: GuardContext,
): { path: string; what: string } | undefined => {
  const outside = (raw: string): string | undefined => {
    if (!isAbsoluteSpelling(raw)) return undefined
    const path = normalize(staticPrefix(raw), ctx.cwd, ctx.home)
    if (isDevicePath(path) || isInsideAny(path, ctx.roots)) return undefined
    return path
  }

  for (const seg of lexCommand(command)) {
    for (const target of seg.writes) {
      const path = outside(target)
      if (path !== undefined) return { path, what: 'a redirect to ' }
    }
    let words = seg.words
    while (words[0] !== undefined && /^[A-Za-z_][A-Za-z0-9_]*=/.test(words[0])) words = words.slice(1)
    while (words[0] !== undefined && PREFIX_WORDS.has(words[0])) words = words.slice(1)
    const cmd = words[0]
    if (cmd === undefined) continue
    const name = cmd.slice(cmd.lastIndexOf('/') + 1)
    const args = words.slice(1).filter(w => !w.startsWith('-'))
    if (DESTRUCTIVE.has(name)) {
      for (const arg of args) {
        const path = outside(arg)
        if (path !== undefined) return { path, what: `${name} of ` }
      }
    } else if (name === 'cd' && args[0] !== undefined) {
      const path = outside(args[0])
      if (path !== undefined) return { path, what: 'cd into ' }
    }
  }
  return undefined
}

/** The deny reason for one tool call, or undefined to let it through. */
export const judgeToolCall = async (
  $: EngineInterface,
  cache: ContextCache,
  tool: string,
  input: Readonly<Record<string, unknown>>,
): Promise<string | undefined> => {
  if (tool === 'Bash') {
    const command = str(input.command)
    if (command === undefined) return undefined
    const ctx = await cache.get($)
    const hit = bashViolation(command, ctx)
    return hit === undefined ? undefined : outsideReason(hit.path, ctx, hit.what)
  }
  if (!FILE_TOOLS.has(tool)) return undefined
  const raw = fileToolPaths(tool, input)
  if (raw.length === 0) return undefined
  const ctx = await cache.get($)
  for (const p of raw) {
    const path = normalize(p, ctx.cwd, ctx.home)
    if (!isInsideAny(path, ctx.roots)) return outsideReason(path, ctx, '')
  }
  return undefined
}

export const registerIsolation = (on: On, cache: ContextCache): void => {
  on('tool.call', async ($, e, next) => {
    let reason: string | undefined
    try {
      reason = await judgeToolCall($, cache, String(e.tool), e as unknown as Readonly<Record<string, unknown>>)
    } catch {
      reason = undefined
    }
    return reason === undefined ? next(e) : { deny: reason }
  }).catch(($, e, next) => next(e))
}
