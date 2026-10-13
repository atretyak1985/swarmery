// The session's allowed roots and the project config they come from, read
// once per session (and again on every module reload) through `$` alone.

import type { EngineInterface } from 'claude-code'
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

const swallow = async <T>(p: Promise<T>): Promise<T | undefined> => {
  try {
    return await p
  } catch {
    return undefined
  }
}

const isRecord = (v: unknown): v is Record<string, unknown> =>
  typeof v === 'object' && v !== null && !Array.isArray(v)

/** Reads `<dir>/.claude/project.json` for the first of `dirs` that has one. */
const readProject = async (
  $: EngineInterface,
  dirs: readonly string[],
): Promise<ProjectConfig | undefined> => {
  for (const dir of dirs) {
    const text = await swallow($.fs.read(`${dir}/.claude/project.json`))
    if (typeof text !== 'string') continue
    try {
      const json: unknown = JSON.parse(text)
      if (isRecord(json)) return { dir, json }
    } catch {
      // malformed: try the next directory, then run without a project config
    }
  }
  return undefined
}

const strings = (v: unknown): string[] =>
  Array.isArray(v) ? v.filter((s): s is string => typeof s === 'string' && s.trim() !== '') : []

const unique = (paths: readonly string[]): string[] => [...new Set(paths)]

/** Temp roots, spelled both ways macOS reaches them (`/tmp` → `/private/tmp`). */
const tmpRoots = (tmpdir: string | undefined): string[] => {
  const out = ['/tmp', '/private/tmp']
  if (tmpdir !== undefined && tmpdir.startsWith('/')) {
    out.push(tmpdir)
    if (tmpdir.startsWith('/var/')) out.push(`/private${tmpdir}`)
  }
  return out
}

export const loadContext = async ($: EngineInterface, options: GuardOptions): Promise<GuardContext> => {
  const cwd = (await swallow($.session.cwd())) ?? '/'
  const root = (await swallow($.session.root())) ?? cwd
  const home = await swallow($.env.get('HOME'))
  const tmpdir = await swallow($.env.get('TMPDIR'))
  const workspaceRoot = await swallow($.env.get('AGENT_WORKSPACE_ROOT'))
  const envProject = await swallow($.env.get('AGENT_PROJECT'))

  const project = await readProject($, unique([cwd, root]))
  const base = project?.dir ?? root
  const at = (p: string, from: string): string => normalize(p, from, home)

  const roots: string[] = [at(root, '/'), at(cwd, '/')]
  if (project !== undefined) {
    roots.push(at(project.dir, '/'))
    for (const repo of strings(project.json.repos)) roots.push(at(repo, project.dir))
  }
  const projectName =
    envProject ?? (typeof project?.json.name === 'string' ? project.json.name : undefined)
  if (workspaceRoot !== undefined && workspaceRoot !== '' && projectName !== undefined && projectName !== '') {
    roots.push(at(`${workspaceRoot}/${projectName}`, base))
  }
  if (home !== undefined) roots.push(`${at(home, '/')}/.claude`)
  roots.push(...tmpRoots(tmpdir).map(t => at(t, '/')))
  for (const extra of options.extraRoots) roots.push(at(extra, base))

  return { cwd, root, home, project, roots: unique(roots) }
}

/** One memoized context per module load; `refresh` re-reads it. */
export const createContextCache = (options: GuardOptions) => {
  let pending: Promise<GuardContext> | undefined
  return {
    get: ($: EngineInterface): Promise<GuardContext> => (pending ??= loadContext($, options)),
    refresh: ($: EngineInterface): Promise<GuardContext> => (pending = loadContext($, options)),
  }
}

export type ContextCache = ReturnType<typeof createContextCache>

/** "a, b, c (+N more)" for a deny reason. */
export const describeRoots = (roots: readonly string[], max = 3): string => {
  const shown = roots.slice(0, max).join(', ')
  return roots.length > max ? `${shown} (+${roots.length - max} more)` : shown
}
