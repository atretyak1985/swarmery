// Loads the pattern files shipped in hooks/patterns/. They are JSON so the
// Go side can pin them to its own lists (parity tests); a hooks module may
// only import code, so they are read through `$.fs` once per module load.

import type { EngineInterface } from 'claude-code'

export type SecretRule = {
  readonly name: string
  readonly re: RegExp
  /** Replacement template: `$1` keeps a captured prefix, `{mark}` is the mark. */
  readonly replace: string
}

export type ProdPattern = {
  readonly source: string
  readonly tool: string
  /** Lower-cased argument glob; undefined matches any call of the tool. */
  readonly inner: string | undefined
}

const isRecord = (v: unknown): v is Record<string, unknown> =>
  typeof v === 'object' && v !== null && !Array.isArray(v)

export const compileSecrets = (raw: unknown): SecretRule[] => {
  if (!Array.isArray(raw)) return []
  const out: SecretRule[] = []
  for (const entry of raw) {
    if (!isRecord(entry) || typeof entry.name !== 'string' || typeof entry.pattern !== 'string') continue
    const flags = typeof entry.flags === 'string' ? entry.flags.replace(/g/g, '') : ''
    try {
      out.push({
        name: entry.name,
        re: new RegExp(entry.pattern, `g${flags}`),
        replace: typeof entry.replace === 'string' ? entry.replace : '{mark}',
      })
    } catch {
      // a pattern this engine cannot compile is skipped, the rest still apply
    }
  }
  return out
}

// The approval-rule syntax: a literal tool name, then an optional (argGlob).
const RULE = /^([A-Za-z][A-Za-z0-9_-]*)(?:\((.+)\))?$/

export const parseProdPattern = (s: string): ProdPattern | undefined => {
  const m = RULE.exec(s.trim())
  if (m === null || m[1] === undefined || m[1] === 'AskUserQuestion') return undefined
  return { source: s, tool: m[1], inner: m[2] === undefined || m[2] === '' ? undefined : m[2].toLowerCase() }
}

export const compileProd = (raw: unknown): ProdPattern[] =>
  Array.isArray(raw)
    ? raw
        .filter((s): s is string => typeof s === 'string')
        .map(parseProdPattern)
        .filter((p): p is ProdPattern => p !== undefined)
    : []

const readJson = async ($: EngineInterface, file: string): Promise<unknown> => {
  try {
    const text = await $.fs.read(`${$.plugin.root}/hooks/patterns/${file}`)
    return typeof text === 'string' ? JSON.parse(text) : undefined
  } catch {
    return undefined
  }
}

/** One memoized read of a pattern file per module load; a failed read is retried next time. */
export const createPatternCache = <T>(file: string, compile: (raw: unknown) => T) => {
  let cached: T | undefined
  return async ($: EngineInterface): Promise<T> => {
    if (cached !== undefined) return cached
    const raw = await readJson($, file)
    const value = compile(raw)
    if (raw !== undefined) cached = value
    return value
  }
}
