import { expect, test } from 'claude-code/testing'
import { interviewsOf, parseInterview } from './lib/interview'
import { ENGINE_DRAWING, SURFACES } from './testkit'

/** How many engine nodes a drawn tree holds: the dialog the engine still draws and answers. */
const engineNodes = (tree: unknown): number => {
  if (typeof tree !== 'object' || tree === null) return 0
  const node = tree as { readonly type?: unknown; readonly children?: unknown }
  const own = node.type === 'engine' ? 1 : 0
  return own + (Array.isArray(node.children) ? node.children.reduce((n: number, c: unknown) => n + engineNodes(c), 0) : 0)
}

const ON = { options: { planBand: true } } as const
const PLUGIN = 'swarmery-link'

/** A dashboard interview question (internal/planning/prompt.go) asked through AskUserQuestion. */
const INTERVIEW_JSON = JSON.stringify({
  type: 'question',
  data: {
    id: 'storage',
    type: 'single_select',
    question: 'Where do line items live?',
    description: 'orders has no child table yet (src/db/schema.ts).',
    options: [
      { id: 'table', label: 'A line_items table', description: 'One row per item.', pros: ['indexed queries'], cons: ['a migration'], recommended: true },
      { id: 'json', label: 'A JSON column', description: 'Items inside the order row.', pros: ['no migration'], cons: ['no per-item index'] },
      { id: 'other', label: 'Other', isOther: true },
    ],
  },
})

const question = (text: string, options: ReadonlyArray<{ label: string; description: string }>) => ({
  question: text,
  header: 'Storage',
  options,
  multiSelect: false,
})

const PLAIN = question('Which branch should I use?', [
  { label: 'main', description: 'the default branch' },
  { label: 'dev', description: 'the integration branch' },
])

const props = (questions: readonly unknown[]) => ({ tool: 'AskUserQuestion', questions: [...questions] })

test('an interview question gets a card above the dialog, the recommended option highlighted', ON, async ($, on) => {
  on('ui.render', () => ENGINE_DRAWING)
  const options = [
    { label: 'A line_items table', description: 'One row per item.' },
    { label: 'A JSON column', description: 'Items inside the order row.' },
  ]
  const ui = await $.ui.mount({ plugin: PLUGIN, surface: 'terminal', component: 'AskUserQuestion', props: props([question(INTERVIEW_JSON, options)]) })
  const texts = (await ui.findAll({ type: 'Text' })).map(t => t.text)
  expect(texts).toEqual([
    'Where do line items live?',
    'orders has no child table yet (src/db/schema.ts).',
    '★ A line_items table (recommended)',
    '  One row per item.',
    '  + indexed queries',
    '  − a migration',
    '• A JSON column',
    '  Items inside the order row.',
    '  + no migration',
    '  − no per-item index',
  ])
  const recommended = await ui.find({ type: 'Text', text: '★ A line_items table (recommended)' })
  expect(recommended?.props).toMatchObject({ bold: true, color: 'success' })
  // the engine's dialog is kept whole, last, and still takes the answer
  const drawn = (await ui.drawn()) as { readonly children?: readonly unknown[] }
  expect(drawn.children?.at(-1)).toEqual(ENGINE_DRAWING)
  expect(engineNodes(drawn)).toBe(1)
})

test('options marked "(Recommended)" with Pros:/Cons: in their descriptions get the card too', ON, async ($, on) => {
  on('ui.render', () => ENGINE_DRAWING)
  const q = question('Which cache?', [
    { label: 'In-process LRU (Recommended)', description: 'Fast to ship. Pros: no new service; simple. Cons: per-instance only' },
    { label: 'Redis', description: 'Shared. Pros: shared across instances. Cons: one more service to run' },
  ])
  const ui = await $.ui.mount({ plugin: PLUGIN, surface: 'terminal', component: 'AskUserQuestion', props: props([q]) })
  const texts = (await ui.findAll({ type: 'Text' })).map(t => t.text)
  expect(texts).toEqual([
    'Which cache?',
    '★ In-process LRU (recommended)',
    '  Fast to ship.',
    '  + no new service',
    '  + simple.',
    '  − per-instance only',
    '• Redis',
    '  Shared.',
    '  + shared across instances.',
    '  − one more service to run',
  ])
  expect(engineNodes(await ui.drawn())).toBe(1)
})

test('a plain question draws the engine\'s dialog alone', ON, async ($, on) => {
  on('ui.render', () => ENGINE_DRAWING)
  const ui = await $.ui.mount({ plugin: PLUGIN, surface: 'terminal', component: 'AskUserQuestion', props: props([PLAIN]) })
  expect(await ui.drawn()).toEqual(ENGINE_DRAWING)
})

test('the card\'s tree validates on every surface', ON, async ($, on) => {
  on('ui.render', () => ENGINE_DRAWING)
  const q = question(INTERVIEW_JSON, [
    { label: 'A line_items table', description: 'One row per item.' },
    { label: 'A JSON column', description: 'Items inside the order row.' },
  ])
  for (const surface of SURFACES) {
    const ui = await $.ui.mount({ plugin: PLUGIN, surface, component: 'AskUserQuestion', props: props([q]) })
    expect(await ui.find({ text: '★ A line_items table (recommended)' }), surface).toBeDefined()
    expect(engineNodes(await ui.drawn()), surface).toBe(1)
    await ui.unmount()
  }
})

test('planBand off: no card, the engine\'s dialog alone', async ($, on) => {
  on('ui.render', () => ENGINE_DRAWING)
  const q = question(INTERVIEW_JSON, [
    { label: 'A line_items table', description: 'One row per item.' },
    { label: 'A JSON column', description: 'Items inside the order row.' },
  ])
  const ui = await $.ui.mount({ plugin: PLUGIN, surface: 'terminal', component: 'AskUserQuestion', props: props([q]) })
  expect(await ui.drawn()).toEqual(ENGINE_DRAWING)
})

test('parseInterview: the JSON in a ```json fence, the Other option left out; plain questions are none', () => {
  const fenced = parseInterview({ question: `\`\`\`json\n${INTERVIEW_JSON}\n\`\`\``, options: [] })
  expect(fenced?.options.map(o => o.label)).toEqual(['A line_items table', 'A JSON column'])
  expect(parseInterview(PLAIN)).toBeUndefined()
  expect(parseInterview('not a question')).toBeUndefined()
  expect(interviewsOf([PLAIN, PLAIN])).toHaveLength(0)
})
