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

// Verbatim copies of the two pattern files, one String.raw literal each, so
// the text between the markers is byte-for-byte the file's JSON. The daemon's
// parity tests (tools/swarmery/internal/approvals/guardpack_parity_test.go,
// tools/swarmery/internal/api/redact_parity_test.go) decode each region and
// fail when it differs from its file: edit a pattern file, paste it here.
// BEGIN mirror: hooks/patterns/prod.json
export const PROD_JSON = String.raw`[
  "Bash(*deploy*prod*)",
  "Bash(*release*prod*)",
  "Bash(terraform apply*)",
  "Bash(*terraform*apply*prod*)",
  "Bash(*kubectl*prod*)",
  "Bash(*helm*upgrade*prod*)",
  "Bash(*git push*prod*)"
]`
// END mirror: hooks/patterns/prod.json

// BEGIN mirror: hooks/patterns/secrets.json
export const SECRETS_JSON = String.raw`[
  { "name": "anthropic_key", "pattern": "sk-ant-[A-Za-z0-9_-]+" },
  { "name": "github_token", "pattern": "(?:gh[pousr]_[A-Za-z0-9]+|github_pat_[A-Za-z0-9_]+)" },
  { "name": "gitlab_token", "pattern": "glpat-[A-Za-z0-9_-]+" },
  { "name": "aws_access_key", "pattern": "(?:AKIA|ASIA)[A-Z0-9]{16}" },
  { "name": "slack_token", "pattern": "xox[baprs]-[A-Za-z0-9-]+" },
  { "name": "google_api_key", "pattern": "AIza[A-Za-z0-9_-]{35}" },
  { "name": "npm_token", "pattern": "npm_[A-Za-z0-9]{36}" },
  { "name": "jwt", "pattern": "eyJ[A-Za-z0-9_-]{10,}\\.[A-Za-z0-9_-]+\\.[A-Za-z0-9_-]+" },
  { "name": "bearer", "pattern": "(bearer\\s+)[A-Za-z0-9._-]+", "flags": "i", "replace": "$1{mark}" },
  { "name": "generic_key_value", "pattern": "((?:token|secret|passwd|password|api[_-]?key|credential)[A-Za-z0-9_-]*\\s*[=:]\\s*)\\S+", "flags": "i", "replace": "$1{mark}" },
  { "name": "url_userinfo", "pattern": "([a-z][a-z0-9+.-]*://[^/\\s:@]+:)[^/\\s@]+@", "replace": "$1{mark}@" }
]`
// END mirror: hooks/patterns/secrets.json

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
  // The classic events the production guard reads the permission mode from;
  // no settings hook is configured beneath, so each answers nothing.
  on('classic.SessionStart', () => ({}))
  on('classic.UserPromptSubmit', () => ({}))
  on('classic.PostToolUse', () => ({}))
  on('classic.PostToolUseFailure', () => ({}))
  on('agent.offer', () => ({ isOffered: true }))
  on('tool.describe', ($, e) => ({ description: e.description }))

  return { statuses }
}
