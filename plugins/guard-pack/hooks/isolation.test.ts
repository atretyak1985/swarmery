import { expect, test } from 'claude-code/testing'
import { HOME, ROOT, TMP, WORKSPACE, world } from './testkit'

const START = { cwd: ROOT, surface: null, isInteractive: false } as const

test('a Read outside the project roots is denied, with the extraRoots hint', async ($, on) => {
  world(on)
  await $.session.start(START)
  const r = await $.tool.call({ tool: 'Read', file_path: '/work/apps/other-app/src/index.ts' })
  expect(r.deny).toBeDefined()
  expect(r.deny).toContain('/work/apps/other-app/src/index.ts is outside')
  expect(r.deny).toContain('extraRoots')
})

test('a Read inside the roots runs: root, relative, repos[], workspace, ~/.claude, tmp', async ($, on) => {
  world(on)
  await $.session.start(START)
  for (const file_path of [
    `${ROOT}/src/app.ts`,
    'src/app.ts',
    `${ROOT}/packages/shared/x.ts`,
    '/work/apps/infrastructure/main.tf',
    `${WORKSPACE}/main-app/workspace/plan.md`,
    `${HOME}/.claude/settings.json`,
    '~/.claude/CLAUDE.md',
    `${TMP}/scratch.txt`,
    `/private${TMP}/scratch.txt`,
    '/tmp/x.log',
  ]) {
    const r = await $.tool.call({ tool: 'Read', file_path })
    expect(r.deny, file_path).toBeUndefined()
    expect(r.result, file_path).toBe('ran')
  }
})

test('`..` cannot climb out of the root lexically', async ($, on) => {
  world(on)
  await $.session.start(START)
  const r = await $.tool.call({ tool: 'Edit', file_path: `${ROOT}/src/../../other-app/x.ts`, old_string: 'a', new_string: 'b' })
  expect(r.deny).toContain('/work/apps/other-app/x.ts is outside')
})

test('extraRoots widens the roots', { options: { extraRoots: '/data/shared, ~/notes' } }, async ($, on) => {
  world(on)
  await $.session.start(START)
  expect((await $.tool.call({ tool: 'Read', file_path: '/data/shared/a.csv' })).deny).toBeUndefined()
  expect((await $.tool.call({ tool: 'Read', file_path: `${HOME}/notes/todo.md` })).deny).toBeUndefined()
  expect((await $.tool.call({ tool: 'Read', file_path: '/data/private/a.csv' })).deny).toBeDefined()
})

test('Bash: rm of an absolute path outside the roots is denied; harmless commands run', async ($, on) => {
  world(on)
  await $.session.start(START)
  const rm = await $.tool.call({ tool: 'Bash', command: 'rm -rf /work/apps/other-app/dist' })
  expect(rm.deny).toContain('rm of /work/apps/other-app/dist is outside')

  const redirect = await $.tool.call({ tool: 'Bash', command: 'echo hi > /etc/hosts' })
  expect(redirect.deny).toContain('redirect to /etc/hosts')

  const cd = await $.tool.call({ tool: 'Bash', command: 'cd /work/apps/other-app && ls' })
  expect(cd.deny).toContain('cd into /work/apps/other-app')

  for (const command of [
    'ls /etc',
    'cat /usr/share/dict/words | head',
    'rm -rf dist',
    `rm -rf ${ROOT}/dist`,
    'make test 2>&1 | tail -5',
    'grep -r x . 2>/dev/null',
    'cp "$SRC" "$DEST"',
    `cd ${ROOT}/src && cp a.ts b.ts`,
  ]) {
    const r = await $.tool.call({ tool: 'Bash', command })
    expect(r.deny, command).toBeUndefined()
  }
})

test('no project.json: the guard still works from the session root', async ($, on) => {
  world(on, { files: {} })
  await $.session.start(START)
  expect((await $.tool.call({ tool: 'Read', file_path: `${ROOT}/README.md` })).deny).toBeUndefined()
  expect((await $.tool.call({ tool: 'Read', file_path: `${HOME}/.claude/settings.json` })).deny).toBeUndefined()
  expect((await $.tool.call({ tool: 'Read', file_path: `${TMP}/x` })).deny).toBeUndefined()
  // repos[] and the workspace come from project.json: without it they are outside
  expect((await $.tool.call({ tool: 'Read', file_path: `${ROOT}/../infrastructure/main.tf` })).deny).toBeDefined()
  expect((await $.tool.call({ tool: 'Read', file_path: `${HOME}/.ssh/id_ed25519` })).deny).toBeDefined()
})

test('isolation off registers no tool.call hook', { options: { isolation: false } }, async ($, on) => {
  world(on)
  await $.session.start(START)
  expect((await $.tool.call({ tool: 'Read', file_path: '/etc/passwd' })).deny).toBeUndefined()
})

test('before session.start has been read, the guard lets calls through', async ($, on) => {
  world(on)
  expect((await $.tool.call({ tool: 'Read', file_path: '/etc/passwd' })).deny).toBeUndefined()
})
