// Knowledge (v4, handoff §3, §9): what a project KNOWS, in the frame every place
// shares. The accordion groups are Memory · Architecture · Tooling, and Docs is a
// sidebar place of its own because it documents swarmery, not the project.
//
// `?tab=` keeps its four old values so the retired /p/:slug/{memory,architecture,
// serena,graphify} routes redirect onto a group: serena and graphify are the two
// rows of Tooling. Memory lists the project's memory files and opens the editor;
// the other three stay the project-scoped pages they were, lazy, filling the detail
// (lib/fillRoute.ts). The fleet /serena, /graphify, /architecture pages are NOT
// redirected here: Knowledge needs a project.

import { useLingui } from '@lingui/react/macro';
import { Suspense, lazy, useCallback, useEffect, useState } from 'react';
import { useParams } from 'react-router-dom';
import { fetchMemoryList } from '../../api';
import type { MemoryFile } from '../../api/types';
import { PageDetail, PagePlace, type PlaceGroup, usePlaceParams } from '../../components/place/PageDetail';
import { Loading } from '../../components/ui';
import { fmtAgo } from '../../lib/format';
import { KIND_LABELS, Memory, MemoryEditor, fmtBytes } from '../Memory';

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

export const KNOWLEDGE_TABS: readonly KnowledgeTab[] = ['memory', 'architecture', 'serena', 'graphify'];

type KnowledgeGroup = 'memory' | 'architecture' | 'tooling';

function groupOf(tab: KnowledgeTab): KnowledgeGroup {
  return tab === 'serena' || tab === 'graphify' ? 'tooling' : tab;
}

export function Knowledge(): JSX.Element {
  const { t, i18n } = useLingui();
  const { slug = '' } = useParams<{ slug: string }>();
  const { tab, item, pick } = usePlaceParams<KnowledgeTab>(KNOWLEDGE_TABS, 'memory');
  const [files, setFiles] = useState<MemoryFile[] | null>(null);

  const loadFiles = useCallback((): void => {
    if (slug === '') return;
    fetchMemoryList(slug)
      .then((r) => setFiles(r.files))
      .catch(() => setFiles(null));
  }, [slug]);
  useEffect(loadFiles, [loadFiles]);

  const kindLabel = (kind: MemoryFile['kind']): string => i18n._(KIND_LABELS[kind]);

  const groups: PlaceGroup[] = [
    {
      key: 'memory',
      label: t`Memory`,
      tone: 'blue',
      count: files?.length ?? 0,
      items: [
        ...(files ?? []).map((file) => ({
          key: `file:${file.path}`,
          glyph: '▪',
          glyphTone: 'blue' as const,
          title: file.name,
          summary: `${kindLabel(file.kind)} · ${fmtBytes(file.sizeBytes)}`,
          age: fmtAgo(file.updatedAt),
        })),
        { key: 'panels', glyph: '◇', title: t`Index and stale facts`, summary: t`consolidate · lines that went stale` },
      ],
    },
    {
      key: 'architecture',
      label: t`Architecture`,
      items: [{ key: 'map', glyph: '❖', title: t`Architecture map`, summary: t`modules · flows · entry points` }],
    },
    {
      key: 'tooling',
      label: t`Tooling`,
      items: [
        { key: 'serena', glyph: '◆', title: t`Serena`, summary: t`symbol index` },
        { key: 'graphify', glyph: '◆', title: t`Graphify`, summary: t`dependency graph` },
      ],
    },
  ];

  const group = groupOf(tab);
  const open = groups.find((g) => g.key === group) ?? groups[0];
  const selected =
    group === 'tooling'
      ? tab
      : (open?.items.find((i) => i.key === item)?.key ?? open?.items[0]?.key ?? null);

  const openFile = useCallback((path: string): void => pick('memory', `file:${path}`), [pick]);

  const detail = (): JSX.Element => {
    if (tab === 'architecture') return <ScopedArchitecture />;
    if (tab === 'serena') return <ScopedSerena />;
    if (tab === 'graphify') return <ScopedGraphify />;
    const file = files?.find((f) => `file:${f.path}` === selected);
    if (file !== undefined) {
      return (
        <PageDetail
          kind={t`memory`}
          kindTone="blue"
          meta={`${kindLabel(file.kind)} · ${fmtBytes(file.sizeBytes)}`}
          title={file.name}
          lead={t`A file the project remembers and loads when a run needs it. Edit it if it has gone stale.`}
          kv={[
            { k: t`last changed`, v: fmtAgo(file.updatedAt) },
            { k: t`scope`, v: slug },
            { k: t`source`, v: kindLabel(file.kind) },
            ...(file.writable ? [] : [{ k: t`access`, v: t`read-only`, tone: 'amber' as const }]),
          ]}
        >
          <div className="mt-[18px]">
            <MemoryEditor key={file.path} project={slug} path={file.path} onSaved={loadFiles} />
          </div>
        </PageDetail>
      );
    }
    return (
      <PageDetail
        kind={t`memory`}
        kindTone="blue"
        title={t`Everything this project remembers.`}
        lead={t`Its instructions, Claude Code auto-memory and Serena notes, editable with a versioned backup on every save.`}
      >
        <Memory panels onOpenFile={openFile} />
      </PageDetail>
    );
  };

  return (
    <PagePlace
      title={t`Knowledge`}
      sub={t`what this project remembers`}
      searchHint={t`search memory, modules, symbols`}
      groups={groups}
      openGroup={group}
      selected={selected}
      onOpenGroup={(key) => pick(key === 'tooling' ? 'serena' : (key as KnowledgeTab))}
      onSelect={(key) => {
        if (key === 'map') pick('architecture');
        else if (key === 'serena' || key === 'graphify') pick(key);
        else pick('memory', key === open?.items[0]?.key ? null : key);
      }}
      detail={
        <div className="h-full min-h-0">
          <Suspense fallback={<Loading label={t`loading…`} />}>{detail()}</Suspense>
        </div>
      }
    />
  );
}
