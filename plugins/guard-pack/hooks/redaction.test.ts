import { expect, mock, test } from 'claude-code/testing'
import { ROOT, world } from './testkit'

const START = { cwd: ROOT, surface: null, isInteractive: false } as const
const SECRET_LINE = 'Authorization: Bearer abc123def456ghi789'

test('a bearer token in a tool_result is redacted; a prompt row is untouched', async ($, on) => {
  const shown = world(on)
  const session = mock.session(on)
  await $.session.start(START)

  await $.session.append({
    message: { type: 'user', role: 'user', content: [{ type: 'text', text: `please echo ${SECRET_LINE}` }] },
    door: 'prompt',
    origin: { kind: 'composer' },
    uuid: 'row-prompt',
  })
  await $.session.append({
    message: {
      type: 'user',
      role: 'user',
      content: [{ type: 'tool_result', tool_use_id: 'toolu_1', content: `${SECRET_LINE}\n` }],
    },
    door: 'tool-result',
    origin: { kind: 'tool', tool: 'Bash' },
    uuid: 'row-result',
  })

  const rows = session.appended()
  const prompt = rows.find(r => r.uuid === 'row-prompt')
  const result = rows.find(r => r.uuid === 'row-result')
  expect(JSON.stringify(prompt?.message.content)).toContain('Bearer abc123def456ghi789')
  const stored = JSON.stringify(result?.message.content)
  expect(stored).toContain('Bearer [REDACTED:bearer]')
  expect(stored).not.toContain('abc123def456ghi789')
  expect(shown.statuses).toContain('guard: 1 secret redacted')
})

test('text blocks inside a tool_result are redacted, each rule by name', async ($, on) => {
  world(on)
  const session = mock.session(on)
  await $.session.start(START)
  await $.session.append({
    message: {
      type: 'user',
      role: 'user',
      content: [
        {
          type: 'tool_result',
          tool_use_id: 'toolu_2',
          content: [
            { type: 'text', text: 'key sk-ant-api03-AbCdEf_123 and AKIAABCDEFGHIJKLMNOP' },
            { type: 'text', text: 'db=postgres://app:hunter2@db.internal/app password = s3cr3t' },
          ],
        },
      ],
    },
    door: 'tool-result',
    origin: { kind: 'tool', tool: 'Read' },
    uuid: 'row-blocks',
  })
  const stored = JSON.stringify(session.appended().find(r => r.uuid === 'row-blocks')?.message.content)
  expect(stored).toContain('[REDACTED:anthropic_key]')
  expect(stored).toContain('[REDACTED:aws_access_key]')
  expect(stored).toContain('postgres://app:[REDACTED:url_userinfo]@db.internal')
  expect(stored).toContain('password = [REDACTED:generic_key_value]')
  expect(stored).not.toContain('hunter2')
  expect(stored).not.toContain('s3cr3t')
})

test('redaction off leaves tool results as they came', { options: { redaction: false } }, async ($, on) => {
  world(on)
  const session = mock.session(on)
  await $.session.start(START)
  await $.session.append({
    message: { type: 'user', role: 'user', content: [{ type: 'tool_result', tool_use_id: 'toolu_3', content: SECRET_LINE }] },
    door: 'tool-result',
    origin: { kind: 'tool', tool: 'Bash' },
    uuid: 'row-off',
  })
  expect(JSON.stringify(session.appended().find(r => r.uuid === 'row-off')?.message.content)).toContain(
    'abc123def456ghi789',
  )
})
