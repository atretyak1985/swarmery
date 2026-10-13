// What the model is offered: agents of packs this project has not enabled
// (agentOffer), and heavy MCP tools moved behind ToolSearch (deferHeavyTools).

import type { EngineInterface, On } from 'claude-code'
import type { ContextCache } from './roots'

/** An MCP tool description longer than this (in characters) is "heavy". */
export const HEAVY_DESCRIPTION = 2048

const SELF = 'guard-pack'

const pluginName = (key: string): string => key.split('@')[0] ?? key

/** Names a value lists: an array of strings, or an object's keys set to true. */
const enabledNames = (v: unknown): string[] => {
  if (Array.isArray(v)) return v.filter((s): s is string => typeof s === 'string').map(pluginName)
  if (typeof v === 'object' && v !== null) {
    return Object.entries(v as Readonly<Record<string, unknown>>)
      .filter(([, on]) => on === true)
      .map(([k]) => pluginName(k))
  }
  return []
}

export type PackPolicy = {
  /** False when the project declares nothing: then every agent stays offered. */
  readonly isDeclared: boolean
  readonly enabled: ReadonlySet<string>
}

const readSettingsPlugins = async ($: EngineInterface, source: 'project' | 'local'): Promise<unknown> => {
  try {
    return (await $.settings.read({ source })).enabledPlugins
  } catch {
    return undefined
  }
}

export const loadPackPolicy = async ($: EngineInterface, cache: ContextCache): Promise<PackPolicy> => {
  const ctx = await cache.get($)
  const json = ctx.project?.json
  const sources: unknown[] = [
    json?.enabledPacks,
    json?.enabledPlugins,
    await readSettingsPlugins($, 'project'),
    await readSettingsPlugins($, 'local'),
  ]
  const isDeclared = ctx.project !== undefined || sources.some(s => s !== undefined)
  return { isDeclared, enabled: new Set(sources.flatMap(enabledNames)) }
}

/** The pack an agent type belongs to, when it is a `<name>-pack:<agent>` of a plugin. */
export const packOf = (agent: string, provider: string | undefined): string | undefined => {
  const i = agent.indexOf(':')
  const name = provider !== undefined && provider !== 'engine' ? provider : i > 0 ? agent.slice(0, i) : undefined
  return name !== undefined && name.endsWith('-pack') ? name : undefined
}

export const isOffered = (policy: PackPolicy, agent: string, provider: string | undefined): boolean => {
  if (!policy.isDeclared) return true
  const pack = packOf(agent, provider)
  return pack === undefined || pack === SELF || policy.enabled.has(pack)
}

export const registerAgentOffer = (on: On, cache: ContextCache): void => {
  let policy: Promise<PackPolicy> | undefined
  on('agent.offer', async ($, e, next) => {
    let offered = true
    try {
      policy ??= loadPackPolicy($, cache)
      offered = isOffered(await policy, e.agent, e.source === 'plugin' ? e.provider.plugin : undefined)
    } catch {
      policy = undefined
      offered = true
    }
    return offered ? next(e) : { isOffered: false }
  }).catch(($, e, next) => next(e))
}

export const registerHeavyToolDeferral = (on: On): void => {
  on('tool.describe', async ($, e, next) => {
    const below = await next(e)
    const isMcp = e.tool.startsWith('mcp__') || e.provider.plugin.startsWith('mcp:')
    return isMcp && below.description.length > HEAVY_DESCRIPTION ? { ...below, isDeferred: true } : below
  }).catch(($, e, next) => next(e))
}
