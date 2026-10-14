// The tool-call input hash. Pure: takes a value, returns its digest. The link
// sends this in place of the input, which never leaves the session
// (mods-link-protocol.md, "tool.call carries no input").

/** Hex digits kept of a digest: 64 bits, enough to match two calls, too few to search. */
export const HASH_HEX = 16

const isRecord = (v: unknown): v is Readonly<Record<string, unknown>> =>
  typeof v === 'object' && v !== null && !Array.isArray(v)

/** JSON with object keys sorted at every depth, so equal inputs hash equal. */
export const canonicalJson = (value: unknown): string => {
  if (Array.isArray(value)) return `[${value.map(canonicalJson).join(',')}]`
  if (isRecord(value)) {
    const fields = Object.keys(value)
      .filter(k => value[k] !== undefined)
      .sort()
      .map(k => `${JSON.stringify(k)}:${canonicalJson(value[k])}`)
    return `{${fields.join(',')}}`
  }
  const text = JSON.stringify(value)
  return text === undefined ? 'null' : text
}

const toHex = (bytes: Uint8Array): string => Array.from(bytes, b => b.toString(16).padStart(2, '0')).join('')

/** FNV-1a, 64 bits, as 16 hex digits: the fallback where `crypto.subtle` is missing. */
export const fnv1a64 = (text: string): string => {
  const bytes = new TextEncoder().encode(text)
  let hash = 0xcbf29ce484222325n
  for (const b of bytes) {
    hash ^= BigInt(b)
    hash = (hash * 0x100000001b3n) & 0xffffffffffffffffn
  }
  return hash.toString(16).padStart(16, '0')
}

/** `HASH_HEX` hex digits of SHA-256 over the canonical JSON, FNV-1a where SHA-256 is unavailable. */
export const inputHash = async (input: unknown): Promise<string> => {
  const text = canonicalJson(input)
  try {
    const subtle = typeof crypto === 'undefined' ? undefined : crypto.subtle
    if (subtle !== undefined) {
      const digest = await subtle.digest('SHA-256', new TextEncoder().encode(text))
      return toHex(new Uint8Array(digest)).slice(0, HASH_HEX)
    }
  } catch {
    // fall through to the pure hash
  }
  return fnv1a64(text).slice(0, HASH_HEX)
}

/** The tool's arguments alone: the envelope fields the engine spreads beside them removed. */
export const toolInput = (e: Readonly<Record<string, unknown>>): Record<string, unknown> => {
  const { tool: _tool, tool_use_id: _id, agentId: _agent, requestMeta: _meta, ...args } = e
  return args
}
