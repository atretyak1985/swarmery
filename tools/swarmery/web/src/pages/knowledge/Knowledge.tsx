// Knowledge (Canvas v3 phase 8, artboard 2a): one project-only place for what a
// project KNOWS — Memory · Architecture · Serena · Graphify — replacing four
// sidebar rows. The active tab lives in `?tab=` so the retired
// /p/:slug/{memory,architecture,serena,graphify} routes redirect onto it.
//
// Bodies are the existing project-scoped pages, lazy, unchanged. The route is a
// fill route (lib/fillRoute.ts): this shell takes the leftover height, the tab
// bar stays put, and each body scrolls inside its own pane — Architecture/
// Serena/Graphify are fill pages already, Memory is document-shaped and
// gets an overflow pane here.
//
// The fleet /serena, /graphify, /architecture pages are NOT redirected here:
// Knowledge needs a project. Docs is not a tab: it documents swarmery itself,
// not the project, so it is its own sidebar place (lib/nav.ts).

import { Suspense, lazy } from 'react';
import { type TabItem, Tabs, useTabParam } from '../../components/Tabs';
import { Loading } from '../../components/ui';

const Memory = lazy(() => import('../Memory').then((m) => ({ default: m.Memory })));
const ScopedArchitecture = lazy(() =>
  import('../../workspace/ScopedPages').then((m) => ({ default: m.ScopedArchitecture })),
);
const ScopedSerena = lazy(() =>
  import('../../workspace/ScopedPages').then((m) => ({ default: m.ScopedSerena })),
);
const ScopedGraphify = lazy(() =>
  import('../../workspace/ScopedPages').then((m) => ({ default: m.ScopedGraphify })),
);

export type KnowledgeTab = 'memory' | 'architecture' | 'serena' | 'graphify';

export const KNOWLEDGE_TABS: readonly KnowledgeTab[] = [
  'memory',
  'architecture',
  'serena',
  'graphify',
];

const TAB_ITEMS: readonly TabItem<KnowledgeTab>[] = [
  { id: 'memory', label: 'Memory' },
  { id: 'architecture', label: 'Architecture' },
  { id: 'serena', label: 'Serena' },
  { id: 'graphify', label: 'Graphify' },
];

function KnowledgeBody({ tab }: { tab: KnowledgeTab }): JSX.Element {
  switch (tab) {
    case 'memory':
      return (
        <div className="h-full overflow-y-auto">
          <Memory />
        </div>
      );
    case 'architecture':
      return <ScopedArchitecture />;
    case 'serena':
      return <ScopedSerena />;
    case 'graphify':
      return <ScopedGraphify />;
  }
}

export function Knowledge(): JSX.Element {
  const [tab, setTab] = useTabParam<KnowledgeTab>('tab', KNOWLEDGE_TABS, 'memory');
  const label = TAB_ITEMS.find((t) => t.id === tab)?.label ?? 'Memory';
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="shrink-0 px-4 pt-6 desk:px-10 desk:pt-[34px]">
        <h1 className="mb-3 font-display text-[30px] leading-tight font-medium tracking-[-0.01em]">
          Knowledge
        </h1>
        <Tabs tabs={TAB_ITEMS} value={tab} onChange={setTab} ariaLabel="Knowledge" />
      </div>
      <div role="tabpanel" aria-label={label} className="min-h-0 flex-1">
        <Suspense fallback={<Loading label={`${label.toLowerCase()}…`} />}>
          <KnowledgeBody tab={tab} />
        </Suspense>
      </div>
    </div>
  );
}
