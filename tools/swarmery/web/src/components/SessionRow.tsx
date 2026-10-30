// One session in the list: the shared ListRow fed from the session (handoff §1).
// A glyph, the title, a project or `plan run` tag, one sentence, and the age. The
// chips the old card carried (model, context, cost, handoff, proc) moved to the
// detail's numbers strip, tabs and ⋯; the row keeps only state.

import { useLingui } from '@lingui/react/macro';
import type { NeedsYouItem, Session } from '../api/types';
import { projectLabel } from '../lib/format';
import { useSessionHref } from '../lib/sessionHref';
import { planRowLabel, rowAge, rowState, rowSummary } from '../lib/sessionsView';
import { useNowMs } from '../lib/sessionState';
import { ListRow, type SummaryTone } from './place/ListRow';
import { StateGlyph } from './place/StateGlyph';

export function SessionRow({
  session,
  now = null,
  gate,
  selected = false,
  hideProject = false,
  label,
  tag,
  note,
}: {
  session: Session;
  /** Live last action, fed by event_appended WS messages. */
  now?: string | null;
  /** The blocker the needs-you queue holds for this session. */
  gate?: NeedsYouItem | undefined;
  selected?: boolean;
  /** The list is scoped to one project, so naming it says nothing. */
  hideProject?: boolean;
  /** A fan-out row names its role, not the run: `plan controller`, a phase. */
  label?: string;
  /** A fan-out row's role tag: `ctl`, `#5`. */
  tag?: string;
  /** Replaces the sentence: a controller explainer, a retry attempt. */
  note?: string | null;
}): JSX.Element {
  const { t } = useLingui();
  const nowMs = useNowMs();
  const sessionHref = useSessionHref();
  const state = rowState(session, nowMs, gate);
  const plan = session.planGroup != null;
  const heading = label ?? (plan ? planRowLabel(session) : session.title);
  const fanOut = label !== undefined;
  const summary = fanOut
    ? (note ?? (now !== null && state === 'working' ? now : undefined))
    : rowSummary(session, state, nowMs, now, gate);
  const tone: SummaryTone = state === 'needs' ? 'needs' : state === 'failed' ? 'failed' : 'default';
  const place = hideProject ? undefined : projectLabel(session.projectName, session.projectSlug);
  return (
    <ListRow
      glyph={<StateGlyph state={state} />}
      title={heading ?? t`(untitled session)`}
      untitled={heading == null}
      tag={tag ?? (plan ? t`plan run` : place)}
      summary={summary ?? undefined}
      summaryTone={tone}
      age={rowAge(session, state, nowMs, gate)}
      selected={selected}
      to={sessionHref(session.id)}
    />
  );
}
