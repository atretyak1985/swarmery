import { expect, test } from 'claude-code/testing'
import { type ModeBook, modeOf, noteMode } from './prodguard'
import { ROOT, world } from './testkit'

const START = { cwd: ROOT, surface: null, isInteractive: false } as const

test('a production-deploy command is put to the ask dialog', async ($, on) => {
  world(on)
  await $.session.start(START)
  for (const command of [
    'kubectl get pods --context prod',
    'terraform apply -auto-approve',
    'HELM_X=1 helm upgrade api ./chart -f values.PROD.yaml',
    'git push origin production',
  ]) {
    const r = await $.tool.check({ tool: 'Bash', input: { command } })
    expect(r.decision, command).toBe('ask')
    expect(r.reason, command).toContain('guard-pack: production deploy')
  }
})

test("the project's own approvals.prodDeployPatterns are added", async ($, on) => {
  world(on)
  await $.session.start(START)
  const r = await $.tool.check({ tool: 'Bash', input: { command: 'appctl ship --all' } })
  expect(r.decision).toBe('ask')
  expect(r.reason).toContain('Bash(appctl ship*)')
})

test('ordinary commands keep the verdict beneath; a deny beneath stays a deny', async ($, on) => {
  world(on, { verdict: command => (command.includes('--context prod') ? 'deny' : 'allow') })
  await $.session.start(START)
  for (const command of ['npm ci --production', 'kubectl get pods', 'terraform plan']) {
    expect((await $.tool.check({ tool: 'Bash', input: { command } })).decision, command).toBe('allow')
  }
  expect((await $.tool.check({ tool: 'Bash', input: { command: 'kubectl delete ns x --context prod' } })).decision).toBe(
    'deny',
  )
})

test('prodGuard off registers no tool.check hook', { options: { prodGuard: false } }, async ($, on) => {
  world(on)
  await $.session.start(START)
  expect((await $.tool.check({ tool: 'Bash', input: { command: 'kubectl apply --context prod' } })).decision).toBe(
    'allow',
  )
})

test('auto mode refuses a production-deploy command instead of asking', async ($, on) => {
  world(on)
  await $.session.start(START)
  await $.classic.UserPromptSubmit({ prompt: 'ship it', permission_mode: 'auto' })
  const r = await $.tool.check({ tool: 'Bash', input: { command: 'terraform apply -auto-approve' } })
  expect(r.decision).toBe('deny')
  expect(r.reason).toContain('guard-pack: production deploy (matches Bash(terraform apply*))')
  expect(r.reason).toContain('refused in auto permission mode')
  expect(r.reason).toContain('default permission mode')
})

test('dontAsk and bypassPermissions refuse too; default, acceptEdits and plan ask', async ($, on) => {
  world(on)
  await $.session.start(START)
  const command = 'kubectl rollout restart deploy/api --context prod'
  for (const [mode, decision] of [
    ['dontAsk', 'deny'],
    ['bypassPermissions', 'deny'],
    ['default', 'ask'],
    ['acceptEdits', 'ask'],
    ['plan', 'ask'],
  ] as const) {
    await $.classic.UserPromptSubmit({ prompt: 'go', permission_mode: mode })
    expect((await $.tool.check({ tool: 'Bash', input: { command } })).decision, mode).toBe(decision)
  }
})

test('the mode follows the session: SessionStart seeds it, a later event moves it', async ($, on) => {
  world(on)
  await $.session.start(START)
  const command = 'git push origin production'
  expect((await $.tool.check({ tool: 'Bash', input: { command } })).decision, 'no mode seen yet').toBe('ask')
  await $.classic.SessionStart({ source: 'startup', permission_mode: 'auto' })
  expect((await $.tool.check({ tool: 'Bash', input: { command } })).decision, 'auto at start').toBe('deny')
  await $.classic.PostToolUse({
    tool_name: 'Bash',
    tool_input: { command: 'ls' },
    tool_response: 'ok',
    tool_use_id: 'toolu_1',
    permission_mode: 'default',
  })
  expect((await $.tool.check({ tool: 'Bash', input: { command } })).decision, 'back to default').toBe('ask')
})

test("a subagent's own mode wins; one not yet heard from inherits the main loop's", async () => {
  const book: ModeBook = new Map()
  noteMode(book, { permission_mode: 'auto' })
  noteMode(book, { permission_mode: 'default', agent_id: 'a1' })
  noteMode(book, { permission_mode: 7, agent_id: 'a2' })
  expect(modeOf(book, undefined)).toBe('auto')
  expect(modeOf(book, 'a1')).toBe('default')
  expect(modeOf(book, 'a2')).toBe('auto')
  expect(modeOf(new Map(), 'a1')).toBe(undefined)
})

test('in auto mode ordinary commands keep the verdict beneath', async ($, on) => {
  world(on)
  await $.session.start(START)
  await $.classic.UserPromptSubmit({ prompt: 'go', permission_mode: 'auto' })
  for (const command of ['npm ci --production', 'kubectl get pods', 'terraform plan', 'make test']) {
    expect((await $.tool.check({ tool: 'Bash', input: { command } })).decision, command).toBe('allow')
  }
})
