// Plans place: everything before and around a run — New plan · Plans · Board ·
// Playbooks — absorbing the old /planning, /board and /playbooks routes, which
// redirect onto a tab here. The tab is `?tab=` (useTabParam), so a redirect or a
// shared link lands on a specific tab; the default (Plans) keeps the URL clean. Each
// body is lazy, as the routes were.
//
// Plans itself is a list and a plan (handoff §6) and draws none of these tabs: `+ new`
// in its list opens New plan, and Board and Playbooks are reached through ⌘K (plan D-3).
// The strip exists only on those other bodies, as their way back to Plans; a parked
// tab stays out of it unless it is the one you are on.
//
// A deep link INTO a plan (/plans/:plan/…) implies the Plans tab: the path names a
// plan, so a stray `?tab=` there is ignored and replaced away.
//
// The tabs are real links and switch with PUSH (SC-10): New plan → Plans → Back
// returns to New plan. `?scope=` rides along on every href. The title names the tab
// while a non-Plans body is showing; Plans titles itself.

import type { MessageDescriptor } from '@lingui/core';
import { msg } from '@lingui/core/macro';
import { useLingui } from '@lingui/react/macro';
import { Suspense, lazy, useEffect } from 'react';
import { useLocation, useNavigate, useParams } from 'react-router-dom';
import { Tabs, tabParamHref, useTabParam } from '../../components/Tabs';
import { Loading } from '../../components/ui';
import { useDocumentTitle } from '../../lib/useDocumentTitle';
import { useProjectWorkspace } from '../../workspace/ProjectContext';
import { PARKED_PLANS_TABS } from '../../lib/parked';

const Plans = lazy(() => import('../Plans').then((m) => ({ default: m.Plans })));
const PlanningMode = lazy(() => import('../PlanningMode').then((m) => ({ default: m.PlanningMode })));
const Board = lazy(() => import('../Board').then((m) => ({ default: m.Board })));
const Playbooks = lazy(() => import('../Playbooks').then((m) => ({ default: m.Playbooks })));

export type PlansPlaceTab = 'new' | 'plans' | 'board' | 'playbooks';

/** The top tabs; each label is a message, rendered with i18n._ at render time. */
export const PLANS_PLACE_TABS: readonly { id: PlansPlaceTab; label: MessageDescriptor }[] = [
  { id: 'new', label: msg`New plan` },
  { id: 'plans', label: msg`Plans` },
  { id: 'board', label: msg`Board` },
  { id: 'playbooks', label: msg`Playbooks` },
];

const IDS = PLANS_PLACE_TABS.map((t) => t.id);

export function PlansPlace(): JSX.Element {
  const { i18n, t } = useLingui();
  const [paramTab, setParamTab] = useTabParam<PlansPlaceTab>('tab', IDS, 'plans', { history: 'push' });
  const { slug = '', plan } = useParams<{ slug: string; plan?: string }>();
  const { project } = useProjectWorkspace();
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

  // Each tab's URL: this path with `?tab=` set (dropped for Plans). The strip only
  // shows off a plan path, so there is no plan here to keep.
  const place = `/p/${encodeURIComponent(slug)}/plans`;
  const tabs = PLANS_PLACE_TABS.filter((item) => !PARKED_PLANS_TABS.has(item.id) || item.id === tab).map((item) => ({
    id: item.id,
    label: i18n._(item.label),
    href: `${place}${tabParamHref(location.search, 'tab', item.id, 'plans')}`,
  }));

  const current = PLANS_PLACE_TABS.find((item) => item.id === tab);
  const label = current === undefined ? tab : i18n._(current.label);
  useDocumentTitle(tab === 'plans' ? null : `${label} · ${project?.name ?? slug} — Swarmery`);
  if (tab === 'plans') {
    return (
      <div className="flex min-h-0 flex-1 flex-col">
        <Suspense fallback={<Loading label={`${label.toLowerCase()}…`} />}>
          <Plans />
        </Suspense>
      </div>
    );
  }
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="px-3 pt-3 desk:px-6">
        <Tabs tabs={tabs} value={tab} onChange={setParamTab} ariaLabel={t`Plans`} />
      </div>
      <div role="tabpanel" aria-label={label} className="flex min-h-0 flex-1 flex-col">
        <Suspense fallback={<Loading label={`${label.toLowerCase()}…`} />}>
          {tab === 'new' ? <PlanningMode /> : tab === 'board' ? <Board /> : <Playbooks />}
        </Suspense>
      </div>
    </div>
  );
}
