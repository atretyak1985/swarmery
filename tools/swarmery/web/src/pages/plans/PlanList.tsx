// The list column of Plans (handoff §6, design update): the project's plans in three
// tabs — active, done, archived — so finished plans do not bury the running ones. The
// tab is the URL's: a named plan decides it (planFilterOf), otherwise `?status=`, and
// picking a tab lands on that tab's first plan (plansTarget.ts).

import { plural } from '@lingui/core/macro';
import { useLingui } from '@lingui/react/macro';
import { useMemo, useState } from 'react';
import type { Epic } from '../../api/types';
import { ListPane, type ListKeys } from '../../components/place/ListPane';
import { ListRow } from '../../components/place/ListRow';
import { ListEmpty, ListTabs } from '../../components/place/ListTabs';
import { StateGlyph } from '../../components/place/StateGlyph';
import { compactSpan } from '../../lib/sessionsView';
import { useNowMs } from '../../lib/sessionState';
import { computeResolvedSeqs, needYouCount, planGlyph, planSummary, planUpdatedAt } from './planModel';
import { planFilterOf, type EpicFilter } from './plansTarget';

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
  filter,
  onFilter,
  selectedTaskId,
  hrefOf,
  onStep,
  onPrimary,
  onNew,
}: {
  epics: Epic[];
  /** The tab on screen. */
  filter: EpicFilter;
  onFilter: (filter: EpicFilter) => void;
  selectedTaskId: number | null;
  /** A row's link. The selected row links to where we are, which keeps open details. */
  hrefOf: (epic: Epic) => string;
  /** `j/k`: open the plan a row away. */
  onStep: (epic: Epic) => void;
  /** `e`: the selected plan's details. */
  onPrimary: () => void;
  onNew: () => void;
}): JSX.Element {
  const { t } = useLingui();
  const nowMs = useNowMs(30000);
  const [query, setQuery] = useState('');
  const q = query.trim().toLowerCase();
  const visible = useMemo(() => epics.filter((e) => q === '' || e.title.toLowerCase().includes(q)), [epics, q]);
  const shown = useMemo(() => visible.filter((e) => planFilterOf(e) === filter), [visible, filter]);
  const countOf = (f: EpicFilter): number => visible.filter((e) => planFilterOf(e) === f).length;

  const step = (by: number): void => {
    if (shown.length === 0) return;
    const at = shown.findIndex((e) => e.taskId === selectedTaskId);
    const next = shown[Math.min(Math.max(at + by, 0), shown.length - 1)];
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
      strip={
        <ListTabs
          tabs={[
            { id: 'active', label: t`active`, count: countOf('active') },
            { id: 'done', label: t`done`, count: countOf('done') },
            { id: 'archived', label: t`archived`, count: countOf('archived') },
          ]}
          value={filter}
          onChange={onFilter}
          ariaLabel={t`Plans`}
        />
      }
      empty={
        epics.length === 0 ? (
          <div className="px-[14px] py-4 text-[12.5px] leading-[1.5] text-ink-dim">
            {t`No plans in this project yet. A plan/ dir with a phase table under the workspace becomes a plan here, or start one with + new.`}
          </div>
        ) : visible.length === 0 ? (
          <div className="px-[14px] py-4 text-[12.5px] leading-[1.5] text-ink-dim">{t`No plan matches this filter.`}</div>
        ) : shown.length === 0 ? (
          <ListEmpty>
            {filter === 'archived'
              ? t`Nothing archived.`
              : filter === 'done'
                ? t`No plan has landed every phase yet.`
                : t`No active plans. Start one with + new.`}
          </ListEmpty>
        ) : undefined
      }
    >
      <div data-plan-group={filter}>
        {shown.map((e) => {
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
      </div>
    </ListPane>
  );
}
