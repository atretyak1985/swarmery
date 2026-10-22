// The session's allowed roots and the project config they come from. Pure:
// register.ts gathers the facts through `$` on session.start and hands them
// here as plain values.

import { isUnder, normalize, splitList } from './paths'

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
  /** One line per declared root left out because it would widen the fence. */
  readonly ignored: readonly string[]
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

const sameDir = (a: string, b: string): boolean => a.toLowerCase() === b.toLowerCase()

/**
 * Why a declared root would widen the fence past the project, or undefined
 * when it may stay: the file-system root, the home directory or a directory
 * above it, or a directory above one of `anchors` (the project directory and
 * the session root). Every path is already normalized.
 */
const widening = (dir: string, home: string | undefined, anchors: readonly string[]): string | undefined => {
  if (dir === '/') return 'is the file-system root'
  if (home !== undefined && isUnder(home, dir)) {
    return sameDir(home, dir) ? 'is the home directory' : 'contains the home directory'
  }
  const below = anchors.find(a => isUnder(a, dir) && !sameDir(a, dir))
  return below === undefined ? undefined : `contains the project directory ${below}`
}

/** A workspace name is one path segment: no separator, not `.` or `..`. */
const isSegment = (name: string): boolean => !name.includes('/') && name !== '.' && name !== '..'

/**
 * The roots: the session root and cwd; with a project.json its directory,
 * every `repos[]` entry (relative to that directory) and the workspace
 * `<AGENT_WORKSPACE_ROOT>/<AGENT_PROJECT or name>`; always `~/.claude`, the
 * temp dirs and `extraRoots` (relative to the project directory).
 *
 * A declared root (`repos[]`, `extraRoots`) is dropped when it is `/`, the
 * home directory or a directory above it, or a directory above the project
 * directory or the session root; a project `name` that is not one path
 * segment adds no workspace root. A declaration can add the project's own
 * subtrees and its siblings, never a tree that holds the project or home.
 * Each drop is reported in `ignored`.
 */
export const buildContext = (facts: SessionFacts, options: GuardOptions): GuardContext => {
  const { cwd, home, project } = facts
  const root = facts.root ?? cwd
  const base = project?.dir ?? root
  const at = (p: string, from: string): string => normalize(p, from, home)
  const homeDir = nonEmpty(home) ? at(home, '/') : undefined

  const roots: string[] = [at(root, '/'), at(cwd, '/')]
  const ignored: string[] = []
  const anchors = unique([at(root, '/'), ...(project !== undefined ? [at(project.dir, '/')] : [])])
  const declare = (dir: string, from: string): void => {
    const why = widening(dir, homeDir, anchors)
    if (why === undefined) roots.push(dir)
    else ignored.push(`guard-pack: ignored root ${dir} from ${from} (${why})`)
  }

  if (project !== undefined) {
    roots.push(at(project.dir, '/'))
    for (const repo of strings(project.json.repos)) declare(at(repo, project.dir), 'repos[]')
  }
  const fromEnv = nonEmpty(facts.agentProject)
  const name = fromEnv
    ? facts.agentProject
    : typeof project?.json.name === 'string'
      ? project.json.name
      : undefined
  if (nonEmpty(facts.workspaceRoot) && nonEmpty(name)) {
    if (fromEnv || isSegment(name.trim())) roots.push(at(`${facts.workspaceRoot}/${name}`, base))
    else ignored.push(`guard-pack: ignored workspace root (project name ${JSON.stringify(name)} is not one path segment)`)
  }
  if (homeDir !== undefined) roots.push(`${homeDir}/.claude`)
  roots.push(...tmpRoots(facts.tmpdir).map(t => at(t, '/')))
  for (const extra of options.extraRoots) declare(at(extra, base), 'extraRoots')

  return { cwd, root, home, project, roots: unique(roots), ignored }
}

/** "a, b, c (+N more)" for a deny reason. */
export const describeRoots = (roots: readonly string[], max = 3): string => {
  const shown = roots.slice(0, max).join(', ')
  return roots.length > max ? `${shown} (+${roots.length - max} more)` : shown
}
