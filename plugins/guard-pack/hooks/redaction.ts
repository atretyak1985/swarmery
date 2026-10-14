// Redaction: every secret-shaped value inside a tool_result block becomes
// [REDACTED:<rule>] before the row is stored and before the model reads it.
// Rows without a tool_result block, and every other block of a row, are
// handed on untouched.

import type { SecretRule } from './patterns'

export const MARK_PREFIX = '[REDACTED:'

/** Applies every rule in order; returns the new text and how many values it masked. */
export const redactText = (text: string, rules: readonly SecretRule[]): { text: string; count: number } => {
  let count = 0
  let out = text
  for (const rule of rules) {
    const keepsPrefix = rule.replace.includes('$1')
    out = out.replace(rule.re, (match: string, ...rest: unknown[]) => {
      const prefix = keepsPrefix && typeof rest[0] === 'string' ? rest[0] : ''
      if (match.slice(prefix.length).startsWith(MARK_PREFIX)) return match
      count++
      // replacer functions, so a `$` inside the kept prefix is never read as a pattern
      return rule.replace.replace('{mark}', () => `${MARK_PREFIX}${rule.name}]`).replace('$1', () => prefix)
    })
  }
  return { text: out, count }
}

type Block = Readonly<Record<string, unknown>>

const redactResultContent = (
  content: unknown,
  rules: readonly SecretRule[],
): { content: unknown; count: number } => {
  if (typeof content === 'string') {
    const r = redactText(content, rules)
    return { content: r.text, count: r.count }
  }
  if (!Array.isArray(content)) return { content, count: 0 }
  let count = 0
  const next = content.map((b: unknown) => {
    const block = b as Block
    if (typeof block !== 'object' || block === null || block.type !== 'text' || typeof block.text !== 'string') {
      return b
    }
    const r = redactText(block.text, rules)
    count += r.count
    return r.count === 0 ? b : { ...block, text: r.text }
  })
  return { content: count === 0 ? content : next, count }
}

/** The row's blocks with tool_result contents redacted; other blocks as they were. */
export const redactBlocks = (
  blocks: readonly unknown[],
  rules: readonly SecretRule[],
): { blocks: unknown[]; count: number } => {
  let count = 0
  const out = blocks.map((b: unknown) => {
    const block = b as Block
    if (typeof block !== 'object' || block === null || block.type !== 'tool_result') return b
    const r = redactResultContent(block.content, rules)
    count += r.count
    return r.count === 0 ? b : { ...block, content: r.content }
  })
  return { blocks: out, count }
}

export const hasToolResult = (content: unknown): content is readonly unknown[] =>
  Array.isArray(content) &&
  content.some(b => typeof b === 'object' && b !== null && (b as Block).type === 'tool_result')

export const statusLine = (total: number): string =>
  `guard: ${total} secret${total === 1 ? '' : 's'} redacted`
