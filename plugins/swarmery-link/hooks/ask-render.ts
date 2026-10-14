// The interview card (option `planBand`): an AskUserQuestion asked in the
// dashboard interview's shape gets a card above the dialog, with each option's
// description, pros (+) and cons (−) and the recommended one highlighted.
//
// The dialog's props carry no answer callback (RenderPropsOf.AskUserQuestion is
// `tool`, `questions`, `metadataSource`), so a tree of the plugin's own cannot
// answer the question. The card is drawn ABOVE the engine's own control
// (`next(e)`'s element, kept whole), which still takes the answer.

import type { EngineInterface, RenderElement, RenderInput } from 'claude-code'
import type { Interview, InterviewOption } from './lib/interview'

/** The card for `interviews` above `engine`, the dialog's own drawing. */
export function drawInterview(
  $: EngineInterface,
  e: RenderInput<'AskUserQuestion'>,
  interviews: readonly Interview[],
  engine: RenderElement,
): RenderElement {
  const { Box, Text } = $.ui.resolve(e)
  const option = (o: InterviewOption): RenderElement =>
    Box({
      flexDirection: 'column',
      children: [
        o.isRecommended
          ? Text({ bold: true, color: 'success', children: `★ ${o.label} (recommended)` })
          : Text({ children: `• ${o.label}` }),
        o.description === '' ? null : Text({ dimColor: true, children: `  ${o.description}` }),
        o.pros.map(p => Text({ color: 'success', children: `  + ${p}` })),
        o.cons.map(c => Text({ color: 'error', children: `  − ${c}` })),
      ],
    })
  const card = (q: Interview): RenderElement =>
    Box({
      flexDirection: 'column',
      marginBottom: 1,
      children: [
        Text({ bold: true, children: q.question }),
        q.description === '' ? null : Text({ dimColor: true, children: q.description }),
        q.options.map(option),
      ],
    })
  return Box({ flexDirection: 'column', children: [interviews.map(card), engine] })
}
