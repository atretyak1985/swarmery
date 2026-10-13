// The session's allowed roots and the project config they come from. Pure:
// register.ts gathers the facts through `$` on session.start and hands them
// here as plain values.

import { normalize, splitList } from './paths'

export type ProjectConfig = {
  /** The directory holding `.claude/project.json`. */
  readonly dir: string
  readonly json: Readonly<Record<string, unknown>>
}

export type GuardContext = {
  readonly cwd: string
  readonly root: string
  readonly home: string | undefined
  readonly project: ProjectConfig | undefined
  /** Normalized absolute directories a file tool may touch. */
  readonly roots: readonly string[]
}

export type GuardOptions = {
  readonly isolation: boolean
  readonly redaction: boolean
  readonly prodGuard: boolean
  readonly agentOffer: boolean
  readonly deferHeavyTools: boolean
  readonly extraRoots: readonly string[]
}

/** What session.start reads through `$`, every field optional but cwd. */
export type SessionFacts = {
  readonly cwd: string
  readonly root?: string
  readonly home?: string
  readonly tmpdir?: string
  readonly workspaceRoot?: string
  readonly agentProject?: string
  readonly project?: ProjectConfig
}

const flag = (value: unknown, fallback: boolean): boolean =>
  typeof value === 'boolean' ? value : fallback

export const parseOptions = (options: Readonly<Record<string, unknown>>): GuardOptions => ({
  isolation: flag(options.isolation, true),
  redaction: flag(options.redaction, true),
  prodGuard: flag(options.prodGuard, true),
  agentOffer: flag(options.agentOffer, false),
  deferHeavyTools: flag(options.deferHeavyTools, false),
  extraRoots: typeof options.extraRoots === 'string' ? splitList(options.extraRoots) : [],
})

const isRecord = (v: unknown): v is Record<string, unknown> =>
  typeof v === 'object' && v !== null && !Array.isArray(v)

/** A project.json's text as config, or undefined when it is not a JSON object. */
export const parseProject = (dir: string, text: unknown): ProjectConfig | undefined => {
  if (typeof text !== 'string') return undefined
  try {
    const json: unknown = JSON.parse(text)
    return isRecord(json) ? { dir, json } : undefined
  } catch {
    return undefined
  }
}

const strings = (v: unknown): string[] =>
  Array.isArray(v) ? v.filter((s): s is string => typeof s === 'string' && s.trim() !== '') : []

const unique = (paths: readonly string[]): string[] => [...new Set(paths)]

const nonEmpty = (s: string | undefined): s is string => s !== undefined && s.trim() !== ''

/** Temp roots, spelled both ways macOS reaches them (`/tmp` → `/private/tmp`). */
const tmpRoots = (tmpdir: string | undefined): string[] => {
  const out = ['/tmp', '/private/tmp']
  if (tmpdir !== undefined && tmpdir.startsWith('/')) {
    out.push(tmpdir)
    if (tmpdir.startsWith('/var/')) out.push(`/private${tmpdir}`)
  }
  return out
}

/**
 * The roots: the session root and cwd; with a project.json its directory,
 * every `repos[]` entry (relative to that directory) and the workspace
 * `<AGENT_WORKSPACE_ROOT>/<AGENT_PROJECT or name>`; always `~/.claude`, the
 * temp dirs and `extraRoots` (relative to the project directory).
 */
export const buildContext = (facts: SessionFacts, options: GuardOptions): GuardContext => {
  const { cwd, home, project } = facts
  const root = facts.root ?? cwd
  const base = project?.dir ?? root
  const at = (p: string, from: string): string => normalize(p, from, home)

  const roots: string[] = [at(root, '/'), at(cwd, '/')]
  if (project !== undefined) {
    roots.push(at(project.dir, '/'))
    for (const repo of strings(project.json.repos)) roots.push(at(repo, project.dir))
  }
  const name = nonEmpty(facts.agentProject)
    ? facts.agentProject
    : typeof project?.json.name === 'string'
      ? project.json.name
      : undefined
  if (nonEmpty(facts.workspaceRoot) && nonEmpty(name)) roots.push(at(`${facts.workspaceRoot}/${name}`, base))
  if (nonEmpty(home)) roots.push(`${at(home, '/')}/.claude`)
  roots.push(...tmpRoots(facts.tmpdir).map(t => at(t, '/')))
  for (const extra of options.extraRoots) roots.push(at(extra, base))

  return { cwd, root, home, project, roots: unique(roots) }
}

/** "a, b, c (+N more)" for a deny reason. */
export const describeRoots = (roots: readonly string[], max = 3): string => {
  const shown = roots.slice(0, max).join(', ')
  return roots.length > max ? `${shown} (+${roots.length - max} more)` : shown
}
