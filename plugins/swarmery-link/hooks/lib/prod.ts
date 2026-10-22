// The production-deploy match, on the mod's side. Pure. A call that matches
// is never put to the daemon: it is confirmed in the terminal whatever any
// answer on the daemon's port says, so the approvals channel stays safe even
// where the daemon (or something in its place) would have allowed it.
//
// The patterns are guard-pack's shipped list, vendored: a hooks module may
// only import code, and swarmery-link cannot import another plugin. The
// mirror below is pinned to plugins/guard-pack/hooks/patterns/prod.json by
// tools/swarmery/internal/approvals/guardpack_parity_test.go
// (TestSwarmeryLinkProdMirrorParity): edit the file, paste it here.
// Matching is guard-pack's (prodguard.ts globMatch/argOf/matchProd).

// BEGIN mirror: plugins/guard-pack/hooks/patterns/prod.json
const PROD_JSON = String.raw`[
  "Bash(*deploy*prod*)",
  "Bash(*release*prod*)",
  "Bash(terraform apply*)",
  "Bash(*&& terraform apply*)",
  "Bash(*; terraform apply*)",
  "Bash(*command terraform apply*)",
  "Bash(*terraform -chdir=* apply*)",
  "Bash(*terraform*apply*prod*)",
  "Bash(*kubectl*prod*)",
  "Bash(*helm*upgrade*prod*)",
  "Bash(*git push*prod*)"
]`
// END mirror: plugins/guard-pack/hooks/patterns/prod.json

export type ProdPattern = {
  readonly source: string
  readonly tool: string
  /** Lower-cased argument glob; undefined matches any call of the tool. */
  readonly inner: string | undefined
}

// The approval-rule syntax: a literal tool name, then an optional (argGlob).
const RULE = /^([A-Za-z][A-Za-z0-9_-]*)(?:\((.+)\))?$/

export const parseProdPattern = (s: string): ProdPattern | undefined => {
  const m = RULE.exec(s.trim())
  if (m === null || m[1] === undefined || m[1] === 'AskUserQuestion') return undefined
  return { source: s, tool: m[1], inner: m[2] === undefined || m[2] === '' ? undefined : m[2].toLowerCase() }
}

const compile = (text: string): readonly ProdPattern[] => {
  const raw: unknown = JSON.parse(text)
  return Array.isArray(raw)
    ? raw
        .filter((s): s is string => typeof s === 'string')
        .map(parseProdPattern)
        .filter((p): p is ProdPattern => p !== undefined)
    : []
}

export const PROD_PATTERNS: readonly ProdPattern[] = compile(PROD_JSON)

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

/** The first shipped pattern the call matches, or undefined. */
export const matchProd = (tool: string, input: unknown, patterns: readonly ProdPattern[] = PROD_PATTERNS): ProdPattern | undefined => {
  const arg = argOf(tool, input)?.toLowerCase()
  return patterns.find(p => p.tool === tool && (p.inner === undefined || (arg !== undefined && globMatch(p.inner, arg))))
}
