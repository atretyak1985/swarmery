// Lexical path handling for the isolation guard. Nothing here touches the
// file system: no symlink is resolved, so a path is judged by its spelling
// after `~`, `.`, `..` and duplicate separators are folded.

/** Joins `base` and a relative `path`, then folds `.`/`..`/`//` lexically. */
export const normalize = (path: string, base: string, home: string | undefined): string => {
  let p = path.trim()
  if (home !== undefined && (p === '~' || p.startsWith('~/'))) p = home + p.slice(1)
  if (!p.startsWith('/')) p = `${base.replace(/\/+$/, '')}/${p}`
  const out: string[] = []
  for (const seg of p.split('/')) {
    if (seg === '' || seg === '.') continue
    if (seg === '..') out.pop()
    else out.push(seg)
  }
  return `/${out.join('/')}`
}

/**
 * Whether `path` lies at or under `root`. Compared case-insensitively: on a
 * case-insensitive volume `/Work/x` and `/work/x` are one file, and a false
 * deny costs more than the miss this allows on a case-sensitive one.
 */
export const isUnder = (path: string, root: string): boolean => {
  const p = path.toLowerCase()
  const r = root.toLowerCase()
  return r === '/' || p === r || p.startsWith(`${r}/`)
}

export const isInsideAny = (path: string, roots: readonly string[]): boolean =>
  roots.some(root => isUnder(path, root))

/** Splits a comma-separated option value, dropping blanks. */
export const splitList = (value: string): string[] =>
  value
    .split(',')
    .map(s => s.trim())
    .filter(s => s !== '')

/** Paths a command may always touch: the null device and the std streams. */
export const isDevicePath = (path: string): boolean =>
  /^\/dev\/(null|zero|stdin|stdout|stderr|tty|fd\/\d+)$/.test(path)
