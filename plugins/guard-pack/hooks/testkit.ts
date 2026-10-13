// Shared world for guard-pack's tests. The test kit has no file system
// beneath the plugin (`fs.read` must be answered by a test hook), so the
// shipped pattern files are mirrored here; the runtime reads the JSON files
// themselves, and the daemon-side parity tests pin those to the Go lists.
// Not imported by register.ts, so it never loads in a session.

import type { On } from 'claude-code'

export const ROOT = '/work/apps/main-app'
export const HOME = '/home/dev'
export const TMP = '/var/folders/xy/T'
export const WORKSPACE = '/work/agent-workspace'

/** Mirror of hooks/patterns/prod.json. */
export const PROD_JSON = JSON.stringify([
  'Bash(*deploy*prod*)',
  'Bash(*release*prod*)',
  'Bash(terraform apply*)',
  'Bash(*terraform*apply*prod*)',
  'Bash(*kubectl*prod*)',
  'Bash(*helm*upgrade*prod*)',
  'Bash(*git push*prod*)',
])

/** Mirror of hooks/patterns/secrets.json (the rules the tests exercise). */
export const SECRETS_JSON = JSON.stringify([
  { name: 'anthropic_key', pattern: 'sk-ant-[A-Za-z0-9_-]+' },
  { name: 'aws_access_key', pattern: '(?:AKIA|ASIA)[A-Z0-9]{16}' },
  { name: 'bearer', pattern: '(bearer\\s+)[A-Za-z0-9._-]+', flags: 'i', replace: '$1{mark}' },
  {
    name: 'generic_key_value',
    pattern: '((?:token|secret|passwd|password|api[_-]?key|credential)[A-Za-z0-9_-]*\\s*[=:]\\s*)\\S+',
    flags: 'i',
    replace: '$1{mark}',
  },
  { name: 'url_userinfo', pattern: '([a-z][a-z0-9+.-]*://[^/\\s:@]+:)[^/\\s@]+@', replace: '$1{mark}@' },
])

export const PROJECT_JSON = JSON.stringify({
  name: 'main-app',
  repos: ['packages/shared', '../infrastructure'],
  enabledPacks: ['web-pack'],
  approvals: { prodDeployPatterns: ['Bash(appctl ship*)'] },
})

export type World = {
  /** Files `$.fs.read` answers, by absolute path; others are missing. */
  readonly files?: Readonly<Record<string, string>>
  /** `$.settings.read({ source })` answers, by source. */
  readonly settings?: Readonly<Record<string, Readonly<Record<string, unknown>>>>
  readonly env?: Readonly<Record<string, string>>
  /** The verdict beneath the plugin for a Bash command; `allow` when absent. */
  readonly verdict?: (command: string) => 'allow' | 'ask' | 'deny'
}

/**
 * Answers everything guard-pack reads through `$` and the engine's bottom of
 * each event it hooks, then records what it shows. Call before the test's
 * first `$` call.
 */
export const world = (on: On, w: World = {}) => {
  const statuses: (string | undefined)[] = []
  const env: Readonly<Record<string, string>> = w.env ?? {
    HOME,
    TMPDIR: TMP,
    AGENT_WORKSPACE_ROOT: WORKSPACE,
  }
  const files: Readonly<Record<string, string>> = w.files ?? { [`${ROOT}/.claude/project.json`]: PROJECT_JSON }

  on('session.start', ($, e) => ({ cwd: e.cwd }))
  on('session.cwd', () => ({ value: ROOT }))
  on('session.root', () => ({ value: ROOT }))
  on('env.get', ($, e) => ({ value: env[e.name] }))
  on('fs.read', ($, e) => {
    if (e.path.endsWith('/hooks/patterns/prod.json')) return { value: PROD_JSON }
    if (e.path.endsWith('/hooks/patterns/secrets.json')) return { value: SECRETS_JSON }
    const text = files[e.path]
    return text === undefined ? { deny: `ENOENT: ${e.path}` } : { value: text }
  })
  on('settings.read', ($, e) => ({ value: (e.source !== undefined ? w.settings?.[e.source] : undefined) ?? {} }))
  on('ui.status', ($, e) => {
    statuses.push(e.text)
    return { value: undefined }
  })
  on('ui.notice', () => ({ value: undefined }))
  on('tool.call', () => ({ result: 'ran' }))
  on('tool.check', ($, e) => {
    const input = e.input as { readonly command?: unknown } | undefined
    const command = typeof input?.command === 'string' ? input.command : ''
    return { decision: w.verdict?.(command) ?? 'allow' }
  })
  on('agent.offer', () => ({ isOffered: true }))
  on('tool.describe', ($, e) => ({ description: e.description }))

  return { statuses }
}
