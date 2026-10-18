// Interview-style AskUserQuestion questions. Pure: tells a question asked in
// the dashboard interview's shape (internal/planning/prompt.go: a JSON
// `{"type":"question","data":{…}}` as the question's text or description)
// apart from a plain question, and reads it into what the card draws. Nothing
// else makes a question an interview: Claude Code's own AskUserQuestion
// guidance has the model mark an option "(Recommended)" and weigh pros and
// cons in plain questions too, and those keep the engine's standard dialog.

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

const isRecord = (v: unknown): v is Readonly<Record<string, unknown>> => typeof v === 'object' && v !== null && !Array.isArray(v)
const str = (v: unknown): string => (typeof v === 'string' ? v.trim() : '')
const strings = (v: unknown): readonly string[] =>
  Array.isArray(v) ? v.map(str).filter(s => s !== '') : typeof v === 'string' && v.trim() !== '' ? [v.trim()] : []

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

/** One AskUserQuestion question read as an interview, or undefined for a plain one. */
export const parseInterview = (question: unknown): Interview | undefined => {
  if (!isRecord(question)) return undefined
  const json = interviewJson(str(question.question)) ?? interviewJson(str(question.description))
  return json === undefined ? undefined : fromJson(json)
}

/** The interview questions of a dialog; empty when every one is plain. */
export const interviewsOf = (questions: readonly unknown[]): readonly Interview[] =>
  questions.map(parseInterview).filter((q): q is Interview => q !== undefined)
