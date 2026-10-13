import { expect, test } from 'claude-code/testing'
import { ROOT, world } from './testkit'

const START = { cwd: ROOT, surface: null, isInteractive: false } as const

const agent = (name: string, plugin: string) =>
  ({ agent: name, description: 'an agent', source: 'plugin', provider: { plugin, tier: 'user' } }) as const

test('an agent of a pack this project has not enabled is not offered', { options: { agentOffer: true } }, async (
  $,
  on,
) => {
  world(on, { settings: { local: { enabledPlugins: { 'infra-pack@market': true, 'iot-pack@market': false } } } })
  await $.session.start(START)
  expect((await $.agent.offer(agent('uav-pack:mission-planner', 'uav-pack'))).isOffered).toBe(false)
  expect((await $.agent.offer(agent('iot-pack:data-specialist', 'iot-pack'))).isOffered).toBe(false)
  // enabledPacks in project.json, and enabledPlugins in local settings
  expect((await $.agent.offer(agent('web-pack:seo-specialist', 'web-pack'))).isOffered).toBe(true)
  expect((await $.agent.offer(agent('infra-pack:sre', 'infra-pack'))).isOffered).toBe(true)
  // not a pack: core and built-ins stay offered
  expect((await $.agent.offer(agent('core:planner', 'core'))).isOffered).toBe(true)
  expect(
    (await $.agent.offer({ agent: 'Explore', description: 'x', source: 'built-in', provider: { plugin: 'engine', tier: 'core' } }))
      .isOffered,
  ).toBe(true)
})

test('agentOffer is off by default: every agent stays offered', async ($, on) => {
  world(on)
  await $.session.start(START)
  expect((await $.agent.offer(agent('uav-pack:mission-planner', 'uav-pack'))).isOffered).toBe(true)
})

const HEAVY = { tool: 'mcp__docs__search', description: 'd'.repeat(3000), provider: { plugin: 'mcp:docs', tier: 'user' } } as const
const LIGHT = { tool: 'mcp__docs__get', description: 'short', provider: { plugin: 'mcp:docs', tier: 'user' } } as const

test('heavy MCP tools are deferred when deferHeavyTools is on', { options: { deferHeavyTools: true } }, async (
  $,
  on,
) => {
  world(on)
  await $.session.start(START)
  expect((await $.tool.describe(HEAVY)).isDeferred).toBe(true)
  expect((await $.tool.describe(LIGHT)).isDeferred).toBeUndefined()
  const builtin = await $.tool.describe({ tool: 'Bash', description: 'b'.repeat(5000), provider: { plugin: 'engine', tier: 'core' } })
  expect(builtin.isDeferred).toBeUndefined()
})

test('heavy MCP tools are left in place by default', async ($, on) => {
  world(on)
  await $.session.start(START)
  expect((await $.tool.describe(HEAVY)).isDeferred).toBeUndefined()
})
