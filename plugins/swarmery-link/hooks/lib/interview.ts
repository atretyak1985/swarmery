// Interview-style AskUserQuestion questions. Pure: tells a question asked in
// the dashboard interview's shape (internal/planning/prompt.go: a JSON
// `{"type":"question","data":{…}}`) or with a recommended option and pros and
// cons apart from a plain question, and reads it into what the card draws.

export type InterviewOption = {
  readonly label: string
  readonly description: string
  readonly pros: readonly string[]
  readonly cons: readonly string[]
  readonly isRecommended: boolean
}

export type Interview = {
  readonly question: string
  readonly description: string
  readonly options: readonly InterviewOption[]
}

const RECOMMENDED = /\s*\((recommended)\)\s*$/i
const PROS_CONS = /(^|[\s.;:])(pros|cons)\s*:/i

const isRecord = (v: unknown): v is Readonly<Record<string, unknown>> => typeof v === 'object' && v !== null && !Array.isArray(v)
const str = (v: unknown): string => (typeof v === 'string' ? v.trim() : '')
const strings = (v: unknown): readonly string[] =>
  Array.isArray(v) ? v.map(str).filter(s => s !== '') : typeof v === 'string' && v.trim() !== '' ? [v.trim()] : []

/** The items of a `Pros:`/`Cons:` run: split on `;`, newlines and list bullets. */
const items = (text: string): readonly string[] =>
  text
    .split(/;|\n/)
    .map(s => s.replace(/^\s*[-•*+−]\s*/, '').trim())
    .filter(s => s !== '')

/** A description split into its prose and the `Pros:` and `Cons:` runs inside it. */
const splitProsCons = (text: string): { readonly description: string; readonly pros: readonly string[]; readonly cons: readonly string[] } => {
  const pros: string[] = []
  const cons: string[] = []
  const parts = text.split(/\b(pros|cons)\s*:/i)
  const description = (parts[0] ?? '').trim()
  for (let i = 1; i + 1 < parts.length; i += 2) {
    const kind = (parts[i] ?? '').toLowerCase()
    const list = items(parts[i + 1] ?? '')
    if (kind === 'pros') pros.push(...list)
    else cons.push(...list)
  }
  return { description, pros, cons }
}

/** The dashboard interview's JSON, from a question's text (a ```json fence allowed), or undefined. */
const interviewJson = (text: string): Readonly<Record<string, unknown>> | undefined => {
  const body = text
    .trim()
    .replace(/^```(?:json)?\s*/i, '')
    .replace(/```\s*$/, '')
    .trim()
  if (!body.startsWith('{')) return undefined
  try {
    const raw: unknown = JSON.parse(body)
    return isRecord(raw) && raw.type === 'question' && isRecord(raw.data) ? raw.data : undefined
  } catch {
    return undefined
  }
}

const fromJson = (data: Readonly<Record<string, unknown>>): Interview | undefined => {
  const question = str(data.question)
  if (question === '') return undefined
  const options = (Array.isArray(data.options) ? data.options : [])
    .filter(isRecord)
    .filter(o => o.isOther !== true)
    .map(
      (o): InterviewOption => ({
        label: str(o.label),
        description: str(o.description),
        pros: strings(o.pros),
        cons: strings(o.cons),
        isRecommended: o.recommended === true,
      }),
    )
    .filter(o => o.label !== '')
  return { question, description: str(data.description), options }
}

const fromOptions = (q: Readonly<Record<string, unknown>>): Interview | undefined => {
  const raw = (Array.isArray(q.options) ? q.options : []).filter(isRecord)
  const isInterview = raw.some(o => RECOMMENDED.test(str(o.label)) || PROS_CONS.test(str(o.description)))
  if (!isInterview) return undefined
  const options = raw.map((o): InterviewOption => {
    const label = str(o.label)
    const split = splitProsCons(str(o.description))
    return { label: label.replace(RECOMMENDED, ''), ...split, isRecommended: RECOMMENDED.test(label) }
  })
  return { question: str(q.question), description: '', options }
}

/** One AskUserQuestion question read as an interview, or undefined for a plain one. */
export const parseInterview = (question: unknown): Interview | undefined => {
  if (!isRecord(question)) return undefined
  const json = interviewJson(str(question.question)) ?? interviewJson(str(question.description))
  if (json !== undefined) return fromJson(json)
  return fromOptions(question)
}

/** The interview questions of a dialog; empty when every one is plain. */
export const interviewsOf = (questions: readonly unknown[]): readonly Interview[] =>
  questions.map(parseInterview).filter((q): q is Interview => q !== undefined)
