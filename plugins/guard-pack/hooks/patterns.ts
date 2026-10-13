// Compiles the pattern files shipped in hooks/patterns/. They are JSON so the
// Go side can pin them to its own lists (parity tests); a hooks module may
// only import code, so register.ts reads them through `$.fs` on session.start.

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

/** A pattern file's text as JSON, or undefined when unreadable or malformed. */
export const parseJson = (text: unknown): unknown => {
  if (typeof text !== 'string') return undefined
  try {
    return JSON.parse(text)
  } catch {
    return undefined
  }
}
