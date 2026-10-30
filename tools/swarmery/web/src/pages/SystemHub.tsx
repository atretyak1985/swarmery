// The catalog half of the System place (handoff §9): skills, commands, templates
// and hooks. `SystemHub` is what the old /system-hub(/:category(/:id)) routes still
// mount: it lands on /system/:category, keeping the id and `?tab=`. The rest is what
// the place needs per kind: the roster fetch, the list row, and the detail, whose
// blocks come from the roster row and whose sections stay the existing profile
// panels (the Skill Definition section embeds the versioned System editor).

import { t as tr } from '@lingui/core/macro';
import { useLingui } from '@lingui/react/macro';
import { useState } from 'react';
import { Navigate, useLocation, useParams } from 'react-router-dom';
import type { SystemCommand, SystemHook, SystemItem, SystemTemplate } from '../api/types';
import { fetchSystemCommands, fetchSystemHooks, fetchSystemItems, toggleSystemHook } from '../api/system';
import { fetchSystemTemplates } from '../api/systemHub';
import { type PageKv, PageDetail, type PageTone, type PlaceItem } from '../components/place/PageDetail';
import { ErrorBox } from '../components/ui';
import { fmtAgo } from '../lib/format';
import { DetailSections } from './system/SectionTabs';
import { CommandProfile, HookProfile, SkillProfile, TemplateProfile } from './system-hub/Profiles';

export type CatalogCategory = 'skills' | 'commands' | 'templates' | 'hooks';

export const CATALOG: readonly CatalogCategory[] = ['skills', 'commands', 'templates', 'hooks'];

/** A roster row is one of the four catalog item shapes, tagged by kind. */
export type RosterRow =
  | { kind: 'skills'; item: SystemItem }
  | { kind: 'commands'; item: SystemCommand }
  | { kind: 'templates'; item: SystemTemplate }
  | { kind: 'hooks'; item: SystemHook };

export function rowKey(r: RosterRow): string {
  return r.kind === 'templates' ? r.item.name : String(r.item.id);
}

/** Where the row is DEFINED, for the origin filter. Skills, commands and hooks carry
 * `scope` straight from the registry; templates predate that field, so their
 * plugin/project `source`, the same distinction, stands in for it. */
export function rowScope(r: RosterRow): 'global' | 'project' {
  return r.kind === 'templates' ? (r.item.source === 'project' ? 'project' : 'global') : r.item.scope;
}

/** The old routes land on the one place. */
export function SystemHub(): JSX.Element {
  const { slug, category, id } = useParams();
  const { search } = useLocation();
  const base = slug !== undefined ? `/p/${slug}/system` : '/system';
  const known = category === 'insights' || (CATALOG as readonly (string | undefined)[]).includes(category);
  const group = known ? category : 'skills';
  const pathname = id !== undefined ? `${base}/${String(group)}/${encodeURIComponent(id)}` : `${base}/${String(group)}`;
  return <Navigate to={{ pathname, search }} replace />;
}

export function fetchRoster(category: CatalogCategory, projectId: string | null): Promise<RosterRow[]> {
  // exactOptionalPropertyTypes: only set `project` when scoped (never undefined).
  const filters = projectId !== null ? { project: projectId } : {};
  switch (category) {
    case 'skills':
      return fetchSystemItems('skills', filters).then((rows) => rows.map((item) => ({ kind: 'skills', item })));
    case 'commands':
      return fetchSystemCommands(filters).then((rows) => rows.map((item) => ({ kind: 'commands', item })));
    case 'templates':
      return fetchSystemTemplates(projectId ?? undefined).then((rows) =>
        rows.map((item) => ({ kind: 'templates', item })),
      );
    case 'hooks':
      return fetchSystemHooks(filters).then((rows) => rows.map((item) => ({ kind: 'hooks', item })));
  }
}

const LINT_TONE: Record<string, PageTone> = { error: 'red', warn: 'amber' };

export function rosterItem(r: RosterRow): PlaceItem {
  const key = rowKey(r);
  switch (r.kind) {
    case 'skills':
      return {
        key,
        glyph: '✦',
        title: r.item.name,
        ...(r.item.pluginName !== null ? { tag: r.item.pluginName } : {}),
        summary: r.item.description ?? '',
        ...(r.item.lastUsed !== null ? { age: fmtAgo(r.item.lastUsed) } : {}),
        ...(r.item.lintMax !== null ? { right: r.item.lintMax, rightTone: LINT_TONE[r.item.lintMax] ?? 'faint' } : {}),
      };
    case 'commands':
      return {
        key,
        glyph: '/',
        title: `/${r.item.name}`,
        ...(r.item.pluginName !== null ? { tag: r.item.pluginName } : {}),
        summary: r.item.description ?? '',
      };
    case 'templates':
      return { key, glyph: '▤', title: r.item.name, tag: r.item.resolution, summary: r.item.fileName };
    case 'hooks':
      return {
        key,
        glyph: '⚓',
        glyphTone: r.item.enabled ? 'faint' : 'amber',
        title: `${r.item.event} · ${r.item.matcher ?? '*'}`,
        summary: r.item.command,
        ...(r.item.timeout === null ? { right: tr`no timeout`, rightTone: 'amber' as const } : {}),
      };
  }
}

function originKv(item: SystemItem | SystemCommand): PageKv[] {
  return [
    { k: tr`scope`, v: item.projectSlug ?? item.scope },
    { k: tr`origin`, v: item.pluginName ?? item.origin },
    { k: tr`path`, v: item.path },
  ];
}

interface DetailProps {
  tabParam: string | null;
  onTab: (tab: string | null) => void;
  scopeSlug: string | null;
  projectNames: Record<string, string>;
  /** Bumped on a registry edit, so the embedded editor refetches. */
  defRefresh: number;
  onChanged: () => void;
}

function SkillDetail({
  id,
  item,
  tabParam,
  onTab,
  scopeSlug,
  projectNames,
  defRefresh,
  onChanged,
}: DetailProps & { id: number; item: SystemItem | undefined }): JSX.Element {
  const { t } = useLingui();
  const tabs = ['overview', 'docs', 'usage', 'definition'] as const;
  const tab = tabs.find((value) => value === tabParam) ?? null;
  const used = String(item?.tasks30d ?? 0);
  const lastUsed = item?.lastUsed;
  return (
    <PageDetail
      kind={t`system · skill`}
      meta={t`used in ${used} sessions / 30 d`}
      title={item?.name ?? String(id)}
      lead={item?.description ?? null}
      kv={
        item === undefined
          ? []
          : [
              { k: t`last used`, v: lastUsed === null || lastUsed === undefined ? t`never used` : fmtAgo(lastUsed) },
              ...originKv(item),
            ]
      }
      actions={[{ key: 'edit', label: t`edit skill`, onClick: () => onTab('definition') }]}
    >
      <DetailSections
        idPrefix={`skill-${String(id)}`}
        ariaLabel={t`Skill sections`}
        tabs={tabs}
        labels={{ overview: t`Overview`, docs: t`Docs`, usage: t`Usage`, definition: t`Definition` }}
        active={tab}
        onSelect={onTab}
      >
        {tab !== null && (
          <SkillProfile
            id={id}
            tab={tab}
            projectId={scopeSlug}
            projectNames={projectNames}
            defRefresh={defRefresh}
            onDefinitionMutated={onChanged}
          />
        )}
      </DetailSections>
    </PageDetail>
  );
}

function CommandDetail({
  id,
  item,
  tabParam,
  onTab,
  scopeSlug,
}: DetailProps & { id: number; item: SystemCommand | undefined }): JSX.Element {
  const { t } = useLingui();
  const tabs = ['overview', 'docs', 'content'] as const;
  const tab = tabs.find((value) => value === tabParam) ?? null;
  return (
    <PageDetail
      kind={t`system · command`}
      title={item !== undefined ? `/${item.name}` : String(id)}
      lead={item?.description ?? null}
      kv={item === undefined ? [] : originKv(item)}
    >
      <DetailSections
        idPrefix={`command-${String(id)}`}
        ariaLabel={t`Command sections`}
        tabs={tabs}
        labels={{ overview: t`Overview`, docs: t`Docs`, content: t`Content` }}
        active={tab}
        onSelect={onTab}
      >
        {tab !== null && <CommandProfile id={id} tab={tab} projectId={scopeSlug} />}
      </DetailSections>
    </PageDetail>
  );
}

function HookDetail({ id, item, onChanged }: { id: number; item: SystemHook | undefined; onChanged: () => void }): JSX.Element {
  const { t } = useLingui();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const hash = item?.contentHash;
  const flip = (): void => {
    if (item === undefined || hash === undefined) return;
    setBusy(true);
    toggleSystemHook(item.id, !item.enabled, hash)
      .then(onChanged)
      .catch((e: unknown) => setError(String(e)))
      .finally(() => setBusy(false));
  };
  const managed = item?.managed ?? null;
  const code =
    item === undefined
      ? null
      : JSON.stringify({ event: item.event, matcher: item.matcher ?? '*', command: item.command }, null, 2);
  return (
    <PageDetail
      kind={t`system · hook`}
      {...(item?.statusMessage !== undefined && item.statusMessage !== null ? { meta: item.statusMessage } : {})}
      title={item !== undefined ? `${item.event} · ${item.matcher ?? '*'}` : String(id)}
      kv={
        item === undefined
          ? []
          : [
              item.timeout === null
                ? { k: t`timeout`, v: t`none`, tone: 'amber' }
                : { k: t`timeout`, v: `${String(item.timeout)}s` },
              { k: t`scope`, v: item.projectSlug ?? item.scope },
              { k: t`source`, v: item.sourceFile },
              ...(managed !== null ? [{ k: t`managed`, v: managed }] : []),
            ]
      }
      code={code}
      toggles={
        item === undefined
          ? []
          : [
              {
                key: 'enabled',
                label: t`Enabled`,
                hint: t`Disable to let runs skip this hook`,
                on: item.enabled,
                // No hash, no write: the daemon compares it before any change.
                disabled: busy || hash === undefined,
                onChange: flip,
              },
            ]
      }
    >
      {error !== null && <ErrorBox message={error} />}
      <HookProfile id={id} />
    </PageDetail>
  );
}

export function CatalogDetail({
  category,
  selectedKey,
  row,
  ...rest
}: DetailProps & { category: CatalogCategory; selectedKey: string; row: RosterRow | undefined }): JSX.Element {
  const { t } = useLingui();
  const id = /^\d+$/.test(selectedKey) ? Number(selectedKey) : NaN;
  if (category === 'templates') {
    const item = row?.kind === 'templates' ? row.item : undefined;
    return (
      <PageDetail
        kind={t`system · template`}
        {...(item !== undefined ? { meta: item.resolution } : {})}
        title={item?.name ?? selectedKey}
        kv={item === undefined ? [] : [{ k: t`file`, v: item.fileName }, { k: t`path`, v: item.path }]}
      >
        <div className="mt-[18px] max-w-[760px]">
          <TemplateProfile name={selectedKey} projectId={rest.scopeSlug} onCopied={rest.onChanged} />
        </div>
      </PageDetail>
    );
  }
  if (Number.isNaN(id)) {
    const title = category === 'skills' ? t`invalid skill` : category === 'commands' ? t`invalid command` : t`invalid hook`;
    return <PageDetail kind={t`system`} title={title} />;
  }
  if (category === 'skills') {
    return <SkillDetail id={id} item={row?.kind === 'skills' ? row.item : undefined} {...rest} />;
  }
  if (category === 'commands') {
    return <CommandDetail id={id} item={row?.kind === 'commands' ? row.item : undefined} {...rest} />;
  }
  return <HookDetail id={id} item={row?.kind === 'hooks' ? row.item : undefined} onChanged={rest.onChanged} />;
}
