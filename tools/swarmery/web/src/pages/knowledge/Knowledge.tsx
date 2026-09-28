// Knowledge (Canvas v3 phase 8, artboard 2a): one project-only place for what a
// project KNOWS — Memory · Architecture · Serena · Graphify · Docs — replacing
// five sidebar rows. The active tab lives in `?tab=` so the retired
// /p/:slug/{memory,architecture,serena,graphify} routes redirect onto it.
//
// Bodies are the existing project-scoped pages, lazy, unchanged. The route is a
// fill route (lib/fillRoute.ts): this shell takes the leftover height, the tab
// bar stays put, and each body scrolls inside its own pane — Architecture/
// Serena/Graphify/Docs are fill pages already, Memory is document-shaped and
// gets an overflow pane here.
//
// The fleet /docs, /serena, /graphify, /architecture pages are NOT redirected
// here: glossary and markdown deep links resolve to /docs/:slug, and Knowledge
// needs a project.

import { Suspense, lazy, useContext, useMemo } from 'react';
import { UNSAFE_RouteContext } from 'react-router-dom';
import { type TabItem, Tabs, useTabParam } from '../../components/Tabs';
import { Loading } from '../../components/ui';
import { Docs } from '../Docs';

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

export type KnowledgeTab = 'memory' | 'architecture' | 'serena' | 'graphify' | 'docs';

export const KNOWLEDGE_TABS: readonly KnowledgeTab[] = [
  'memory',
  'architecture',
  'serena',
  'graphify',
  'docs',
];

const TAB_ITEMS: readonly TabItem<KnowledgeTab>[] = [
  { id: 'memory', label: 'Memory' },
  { id: 'architecture', label: 'Architecture' },
  { id: 'serena', label: 'Serena' },
  { id: 'graphify', label: 'Graphify' },
  { id: 'docs', label: 'Docs' },
];

/** Docs reads its DOC slug from `useParams().slug`, which under /p/:slug is the
 * PROJECT slug — it would try to open a doc named after the project. Re-provide
 * the route context with `slug` dropped from the params (everything else —
 * pathnames for relative links, the data-router flag — passes through), so the
 * pane opens on the first doc. A descendant `<Routes>` cannot do this: its
 * params are merged OVER the parent's, and an absent optional param leaves the
 * parent's in place. Doc links inside still go to the fleet /docs/:slug. */
function ProjectDocs(): JSX.Element {
  const ctx = useContext(UNSAFE_RouteContext);
  const value = useMemo(
    () => ({
      ...ctx,
      matches: ctx.matches.map((m) => {
        const { slug: _projectSlug, ...params } = m.params;
        return { ...m, params };
      }),
    }),
    [ctx],
  );
  return (
    <UNSAFE_RouteContext.Provider value={value}>
      <Docs />
    </UNSAFE_RouteContext.Provider>
  );
}

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
    case 'docs':
      return <ProjectDocs />;
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
