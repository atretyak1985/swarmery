// What the model is offered: agents of packs this project has not enabled
// (agentOffer), and heavy MCP tools moved behind ToolSearch (deferHeavyTools).
// Pure: register.ts reads project.json and settings through `$`.

import type { ProjectConfig } from './roots'

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

/**
 * The packs this project enables: project.json `enabledPacks` (and an
 * `enabledPlugins` there, if a project keeps one), plus the project and local
 * settings' `enabledPlugins`. User-level settings are left out on purpose:
 * a pack enabled for every project is not this project's choice.
 */
export const packPolicy = (
  project: ProjectConfig | undefined,
  projectSettingsPlugins: unknown,
  localSettingsPlugins: unknown,
): PackPolicy => {
  const sources: unknown[] = [
    project?.json.enabledPacks,
    project?.json.enabledPlugins,
    projectSettingsPlugins,
    localSettingsPlugins,
  ]
  const isDeclared = project !== undefined || sources.some(s => s !== undefined)
  return { isDeclared, enabled: new Set(sources.flatMap(enabledNames)) }
}

/** The pack an agent type belongs to, when it is a `<name>-pack` plugin's. */
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

export const isHeavyMcpTool = (tool: string, providerPlugin: string, description: string): boolean =>
  (tool.startsWith('mcp__') || providerPlugin.startsWith('mcp:')) && description.length > HEAVY_DESCRIPTION
