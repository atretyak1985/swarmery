import { expect, test } from 'claude-code/testing'
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
