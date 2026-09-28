// Plans place (Canvas v3 phase 7): one place for everything before and around a
// run — New plan · Plans · Board · Playbooks — absorbing the old /planning,
// /board and /playbooks routes, which now redirect onto a tab here. The tab is
// `?tab=` (useTabParam), so a redirect or a shared link lands on a specific tab;
// the default (Plans) keeps the URL clean. Each body is lazy, as the routes were.

import { Suspense, lazy } from 'react';
import { type TabItem, Tabs, useTabParam } from '../../components/Tabs';
import { Loading } from '../../components/ui';

const Plans = lazy(() => import('../Plans').then((m) => ({ default: m.Plans })));
const PlanningMode = lazy(() => import('../PlanningMode').then((m) => ({ default: m.PlanningMode })));
const Board = lazy(() => import('../Board').then((m) => ({ default: m.Board })));
const Playbooks = lazy(() => import('../Playbooks').then((m) => ({ default: m.Playbooks })));

export type PlansPlaceTab = 'new' | 'plans' | 'board' | 'playbooks';

export const PLANS_PLACE_TABS: readonly TabItem<PlansPlaceTab>[] = [
  { id: 'new', label: 'New plan' },
  { id: 'plans', label: 'Plans' },
  { id: 'board', label: 'Board' },
  { id: 'playbooks', label: 'Playbooks' },
];

const IDS = PLANS_PLACE_TABS.map((t) => t.id);

export function PlansPlace(): JSX.Element {
  const [tab, setTab] = useTabParam<PlansPlaceTab>('tab', IDS, 'plans');
  const label = PLANS_PLACE_TABS.find((t) => t.id === tab)?.label ?? tab;
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="px-3 pt-3 desk:px-6">
        <Tabs tabs={PLANS_PLACE_TABS} value={tab} onChange={setTab} ariaLabel="Plans" />
      </div>
      <div role="tabpanel" aria-label={label} className="flex min-h-0 flex-1 flex-col">
        <Suspense fallback={<Loading label={`${label.toLowerCase()}…`} />}>
          {tab === 'new' ? <PlanningMode /> : tab === 'board' ? <Board /> : tab === 'playbooks' ? <Playbooks /> : <Plans />}
        </Suspense>
      </div>
    </div>
  );
}
