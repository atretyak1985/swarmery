// guard-pack: project isolation, secret redaction, the production-deploy
// guard and what the model is offered. Policy is read through `$` alone (no
// daemon) on session.start, which fires again on every module reload; the
// other hooks judge from that snapshot with pure helpers, return fast, and
// fall through to `next` when they cannot judge: a guard that fails never
// blocks the session. A userConfig option turned off registers none of its
// hooks. Every `$` call is spelled here, at its call site, as the engine
// requires.

import type { Register } from 'claude-code'
import { judgeToolCall } from './isolation'
import { type PackPolicy, isHeavyMcpTool, isOffered, packPolicy } from './offer'
import { type ProdPattern, type SecretRule, compileProd, compileSecrets, parseJson } from './patterns'
import { type ModeBook, matchProd, modeOf, noteMode, prodVerdict, projectProdPatterns } from './prodguard'
import { hasToolResult, redactBlocks, statusLine } from './redaction'
import { type GuardContext, type ProjectConfig, buildContext, parseOptions, parseProject } from './roots'

/** A `$` call's answer, or undefined when it rejects. */
const settle = async <T>(call: Promise<T>): Promise<T | undefined> => {
  try {
    return await call
  } catch {
    return undefined
  }
}

/** Swallows a display call's failure, sync or async: display is a courtesy. */
const ignore = (result: unknown): void => {
  if (result instanceof Promise) result.catch(() => undefined)
}

export const register: Register = (on, rawOptions) => {
  const options = parseOptions(rawOptions)

  let ctx: GuardContext | undefined
  let secrets: readonly SecretRule[] = []
  let prod: readonly ProdPattern[] = []
  let policy: PackPolicy | undefined
  let redactedTotal = 0

  on('session.start', async ($, e, next) => {
    const result = await next(e)
    try {
      const cwd = (await settle($.session.cwd())) ?? e.cwd
      const root = (await settle($.session.root())) ?? cwd
      const home = await settle($.env.get('HOME'))
      const tmpdir = await settle($.env.get('TMPDIR'))
      const workspaceRoot = await settle($.env.get('AGENT_WORKSPACE_ROOT'))
      const agentProject = await settle($.env.get('AGENT_PROJECT'))

      let project: ProjectConfig | undefined
      for (const dir of [...new Set([cwd, root])]) {
        project = parseProject(dir, await settle($.fs.read(`${dir}/.claude/project.json`)))
        if (project !== undefined) break
      }
      ctx = buildContext({ cwd, root, home, tmpdir, workspaceRoot, agentProject, project }, options)

      if (options.redaction) {
        secrets = compileSecrets(parseJson(await settle($.fs.read(`${$.plugin.root}/hooks/patterns/secrets.json`))))
      }
      if (options.prodGuard) {
        const shipped = compileProd(parseJson(await settle($.fs.read(`${$.plugin.root}/hooks/patterns/prod.json`))))
        prod = [...shipped, ...projectProdPatterns(project)]
      }
      if (options.agentOffer) {
        const projectSettings = await settle($.settings.read({ source: 'project' }))
        const localSettings = await settle($.settings.read({ source: 'local' }))
        policy = packPolicy(project, projectSettings?.enabledPlugins, localSettings?.enabledPlugins)
      }
    } catch {
      // keep whatever was read: an unread guard lets everything through
    }
    return result
  })

  if (options.isolation) {
    on('tool.call', async ($, e, next) => {
      let reason: string | undefined
      try {
        reason =
          ctx === undefined
            ? undefined
            : judgeToolCall(ctx, String(e.tool), e as unknown as Readonly<Record<string, unknown>>)
      } catch {
        reason = undefined
      }
      return reason === undefined ? next(e) : { deny: reason }
    }).catch(($, e, next) => next(e))
  }

  if (options.redaction) {
    on('session.append', async ($, e, next) => {
      const content: unknown = e.message.content
      if (secrets.length === 0 || !hasToolResult(content)) return next(e)
      let redacted: { blocks: unknown[]; count: number } | undefined
      try {
        redacted = redactBlocks(content, secrets)
      } catch {
        redacted = undefined
      }
      if (redacted === undefined || redacted.count === 0) return next(e)
      redactedTotal += redacted.count
      try {
        ignore($.ui.status(statusLine(redactedTotal)))
      } catch {
        // the redaction stands without its status line
      }
      return next({ ...e, message: { ...e.message, content: redacted.blocks as typeof e.message.content } })
    }).catch(($, e, next) => next(e))
  }

  if (options.prodGuard) {
    // tool.check carries no permission mode; these classic events do. Each
    // only records it and hands the event on unchanged.
    const modes: ModeBook = new Map()
    const record = (e: unknown): void => {
      try {
        noteMode(modes, e)
      } catch {
        // an unread mode keeps the ask
      }
    }
    on('classic.SessionStart', async ($, e, next) => {
      record(e)
      return next(e)
    }).catch(($, e, next) => next(e))
    on('classic.UserPromptSubmit', async ($, e, next) => {
      record(e)
      return next(e)
    }).catch(($, e, next) => next(e))
    on('classic.PostToolUse', async ($, e, next) => {
      record(e)
      return next(e)
    }).catch(($, e, next) => next(e))
    on('classic.PostToolUseFailure', async ($, e, next) => {
      record(e)
      return next(e)
    }).catch(($, e, next) => next(e))

    on('tool.check', async ($, e, next) => {
      const below = await next(e)
      if (below.decision === 'deny') return below
      let hit: ProdPattern | undefined
      try {
        hit = matchProd(prod, String(e.tool), e.input)
      } catch {
        hit = undefined
      }
      if (hit === undefined) return below
      const verdict = prodVerdict(hit, modeOf(modes, e.agentId))
      const id = e.tool_use_id
      if (verdict.decision === 'ask' && id !== undefined) {
        try {
          ignore($.ui.notice(id, verdict.reason))
        } catch {
          // no open dialog to annotate yet; the ask stands
        }
      }
      return verdict
    }).catch(($, e, next) => next(e))
  }

  if (options.agentOffer) {
    on('agent.offer', async ($, e, next) => {
      let offered = true
      try {
        offered = policy === undefined || isOffered(policy, e.agent, e.provider.plugin)
      } catch {
        offered = true
      }
      return offered ? next(e) : { isOffered: false }
    }).catch(($, e, next) => next(e))
  }

  if (options.deferHeavyTools) {
    on('tool.describe', async ($, e, next) => {
      const below = await next(e)
      return isHeavyMcpTool(e.tool, e.provider.plugin, below.description) ? { ...below, isDeferred: true } : below
    }).catch(($, e, next) => next(e))
  }
}
