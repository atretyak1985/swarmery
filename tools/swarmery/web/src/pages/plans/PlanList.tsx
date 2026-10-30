// The list column of Plans (handoff §6): every plan of the project in one list,
// grouped, so a plan's place in its life is read off the list rather than chosen
// with a filter. Archived plans sit in their own group (§6.8). Done is a group of
// its own too: the design's fixture has no finished plan, and on a real project they
// would otherwise bury the running ones. `?status=` still works: it names the group
// `/plans` lands in (plansTarget.ts).

import type { MessageDescriptor } from '@lingui/core';
import { msg, plural } from '@lingui/core/macro';
import { useLingui } from '@lingui/react/macro';
import { useMemo, useState } from 'react';
import type { Epic } from '../../api/types';
import { ListGroup } from '../../components/place/ListGroup';
import { ListPane, type ListKeys } from '../../components/place/ListPane';
import { ListRow } from '../../components/place/ListRow';
import { StateGlyph } from '../../components/place/StateGlyph';
import { compactSpan } from '../../lib/sessionsView';
import { useNowMs } from '../../lib/sessionState';
import { computeResolvedSeqs, needYouCount, planGlyph, planSummary, planUpdatedAt } from './planModel';
import { epicFilterOf, type EpicFilter } from './plansTarget';

const GROUPS: readonly { key: EpicFilter; label: MessageDescriptor }[] = [
  { key: 'active', label: msg`Plans` },
  { key: 'done', label: msg`Done` },
  { key: 'archived', label: msg`Archived` },
];

/** The list's mono line: `2 plans · 3 phases need you`. */
function listSub(plans: number, phases: number): string {
  const head = plural(plans, { one: '# plan', few: '# plans', many: '# plans', other: '# plans' });
  if (phases === 0) return head;
  const tail = plural(phases, {
    one: '# phase needs you',
    few: '# phases need you',
    many: '# phases need you',
    other: '# phases need you',
  });
  return `${head} · ${tail}`;
}

export function PlanList({
  epics,
  selectedTaskId,
  hrefOf,
  onStep,
  onPrimary,
  onNew,
}: {
  epics: Epic[];
  selectedTaskId: number | null;
  /** A row's link. The selected row links to where we are, which keeps open details. */
  hrefOf: (epic: Epic) => string;
  /** `j/k`: open the plan a row away. */
  onStep: (epic: Epic) => void;
  /** `e`: the selected plan's details. */
  onPrimary: () => void;
  onNew: () => void;
}): JSX.Element {
  const { t, i18n } = useLingui();
  const nowMs = useNowMs(30000);
  const [query, setQuery] = useState('');
  const q = query.trim().toLowerCase();
  const visible = useMemo(() => epics.filter((e) => q === '' || e.title.toLowerCase().includes(q)), [epics, q]);
  const groups = useMemo(
    () =>
      GROUPS.map((g) => ({ ...g, epics: visible.filter((e) => epicFilterOf(e.status) === g.key) })).filter(
        (g) => g.epics.length > 0,
      ),
    [visible],
  );
  const ordered = groups.flatMap((g) => g.epics);

  const step = (by: number): void => {
    if (ordered.length === 0) return;
    const at = ordered.findIndex((e) => e.taskId === selectedTaskId);
    const next = ordered[Math.min(Math.max(at + by, 0), ordered.length - 1)];
    if (next !== undefined && next.taskId !== selectedTaskId) onStep(next);
  };
  const keys: ListKeys = { onNext: () => step(1), onPrev: () => step(-1), onPrimary };

  return (
    <ListPane
      title={t`Plans`}
      sub={listSub(
        visible.length,
        visible.reduce((n, e) => n + needYouCount(e), 0),
      )}
      query={query}
      onQueryChange={setQuery}
      searchHint={t`filter plans`}
      onNew={onNew}
      keys={keys}
      empty={
        epics.length === 0 ? (
          <div className="px-[14px] py-4 text-[12.5px] leading-[1.5] text-ink-dim">
            {t`No plans in this project yet. A plan/ dir with a phase table under the workspace becomes a plan here, or start one with + new.`}
          </div>
        ) : visible.length === 0 ? (
          <div className="px-[14px] py-4 text-[12.5px] leading-[1.5] text-ink-dim">{t`No plan matches this filter.`}</div>
        ) : undefined
      }
    >
      {groups.map((group) => (
        <div key={group.key} data-plan-group={group.key}>
          <ListGroup label={i18n._(group.label)} count={group.epics.length}>
            {group.epics.map((e) => {
              const resolved = computeResolvedSeqs(e.phases);
              const updated = planUpdatedAt(e);
              return (
                <ListRow
                  key={e.taskId}
                  glyph={<StateGlyph state={planGlyph(e, resolved)} />}
                  title={e.title}
                  // A micro-plan IS a dispatched board card: the header names the card.
                  tag={e.cardExternalId !== null ? t`card` : undefined}
                  summary={planSummary(e, resolved)}
                  summaryTone={needYouCount(e) > 0 ? 'needs' : e.planRun?.runState === 'failed' ? 'failed' : 'default'}
                  age={updated === null ? undefined : compactSpan(nowMs - new Date(updated).getTime())}
                  selected={e.taskId === selectedTaskId}
                  to={hrefOf(e)}
                />
              );
            })}
          </ListGroup>
        </div>
      ))}
    </ListPane>
  );
}
