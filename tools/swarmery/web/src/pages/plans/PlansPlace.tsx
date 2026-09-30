// Plans place (Canvas v3 phase 7): one place for everything before and around a
// run — New plan · Plans · Board · Playbooks — absorbing the old /planning,
// /board and /playbooks routes, which now redirect onto a tab here. The tab is
// `?tab=` (useTabParam), so a redirect or a shared link lands on a specific tab;
// the default (Plans) keeps the URL clean. Each body is lazy, as the routes were.
//
// A deep link INTO a plan (/plans/:plan/…, plans-deep-links phase 1) implies the
// Plans tab: the path names a plan, so a stray `?tab=` there is ignored and
// replaced away, and picking another top tab leaves the plan path for
// /plans?tab=<id>.

import { Suspense, lazy, useEffect } from 'react';
import { useLocation, useNavigate, useParams } from 'react-router-dom';
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
  const [paramTab, setParamTab] = useTabParam<PlansPlaceTab>('tab', IDS, 'plans');
  const { slug = '', plan } = useParams<{ slug: string; plan?: string }>();
  const location = useLocation();
  const navigate = useNavigate();
  const inPlan = plan !== undefined && plan !== '';
  const tab: PlansPlaceTab = inPlan ? 'plans' : paramTab;

  // A plan path is the Plans tab by definition — drop a PlansPlace `?tab=` that
  // rode along on it (replace: a correction, not a step), so the next plan-less
  // navigation cannot resurface a Board or Playbooks body.
  const strayTab = inPlan && (IDS as readonly string[]).includes(new URLSearchParams(location.search).get('tab') ?? '');
  useEffect(() => {
    if (!strayTab) return;
    const q = new URLSearchParams(location.search);
    q.delete('tab');
    const qs = q.toString();
    navigate(`${location.pathname}${qs === '' ? '' : `?${qs}`}`, { replace: true });
  }, [strayTab, location.pathname, location.search, navigate]);

  const setTab = (id: PlansPlaceTab): void => {
    if (!inPlan) {
      setParamTab(id);
      return;
    }
    if (id === 'plans') return;
    const q = new URLSearchParams(location.search);
    q.set('tab', id);
    navigate(`/p/${encodeURIComponent(slug)}/plans?${q.toString()}`);
  };

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
