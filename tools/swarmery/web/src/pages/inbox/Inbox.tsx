// The Inbox (handoff §1, §8): what waits on the operator without blocking a
// session, fleet (/inbox) or one project (/p/:slug/inbox). Tabs over the list: all,
// the kinds with something waiting, and what the agent closed. `all` groups by kind
// and shows the first rows of each with a way into that kind's tab; one decision in
// the detail; j/k move, e is the primary, x the secondary, and an item leaves once
// it is decided. The tab is `?tab=`, so old links land on it. Approvals and
// questions are decided in Needs you (`?tab=approvals` goes there); rules and
// history stay at approvals/manage.
//
// The triage banner is gone (handoff §11): the agent only proposes here, as an
// `agent: <value>` tag on the row and a suggestion in the detail. What it closed on
// its own is the last tab, each row with an undo as its main action.

import { Trans, useLingui } from '@lingui/react/macro';
import { useEffect, useMemo, useState } from 'react';
import { Navigate, useParams, useSearchParams } from 'react-router-dom';
import { ListGroup, type GroupTone } from '../../components/place/ListGroup';
import { ListPane } from '../../components/place/ListPane';
import { ListRow } from '../../components/place/ListRow';
import { ListEmpty, ListTabs } from '../../components/place/ListTabs';
import { PlaceLayout } from '../../components/place/PlaceLayout';
import { StateGlyph } from '../../components/place/StateGlyph';
import { Loading } from '../../components/ui';
import { TriageConflictError, undoTriageVerdict, type TriageVerdict } from '../../api/triage';
import { compactSpan } from '../../lib/sessionsView';
import { stepKey } from '../../lib/useGlobalKeys';
import { HandledDetail, verdictWording } from './HandledDetail';
import { InboxDetail, denyAction, primaryAction } from './InboxDetail';
import {
  INBOX_GROUP_CAP,
  INBOX_TABS,
  KIND_META,
  expiresInLabel,
  filterItems,
  filterTab,
  groupByKind,
  reviewable,
  suggestionActionLabel,
  tabCounts,
  valueWording,
  type InboxItem,
  type InboxKind,
  type InboxTabId,
} from './inboxModel';
import { FLEET_WIDE_KINDS, useInboxItems } from './useInboxItems';

const handledKey = (v: TriageVerdict): string => `handled:${String(v.id)}`;

/** A group's label takes its kind's colour, as the row's dot does. */
const GROUP_TONE: Record<InboxKind, GroupTone> = {
  approval: 'amber',
  lesson: 'purple',
  advisor: 'blue',
  proposal: 'default',
  classifier: 'green',
  retire: 'red',
  alert: 'red',
  review: 'amber',
};

/** Wall clock for the expiry countdowns. */
function useNow(periodMs: number): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), periodMs);
    return () => clearInterval(id);
  }, [periodMs]);
  return now;
}

/** The kind dot: 9px in the 18px glyph box, the kind's own colour. */
function KindDot({ item }: { item: InboxItem }): JSX.Element {
  return (
    <span
      aria-hidden="true"
      className={`inline-block w-[18px] text-center font-mono text-[9px] leading-[18px] ${KIND_META[item.kind].text}`}
    >
      ●
    </span>
  );
}

/** Under a capped group: how many rows the `all` tab left out, and the way to them. */
function MoreRow({ more, onClick }: { more: number; onClick: () => void }): JSX.Element {
  const { t } = useLingui();
  const count = String(more);
  return (
    <button
      type="button"
      onClick={onClick}
      className="block w-full cursor-pointer pt-1.5 pr-[14px] pb-2 pl-10 text-left font-mono text-[10.5px] leading-[normal] text-ink-faint transition-colors hover:text-brand"
    >
      {t`+ ${count} more →`}
    </button>
  );
}

export function Inbox(): JSX.Element {
  const { slug } = useParams<{ slug?: string }>();
  const [params] = useSearchParams();
  const tab = params.get('tab');
  if (tab === 'approvals') {
    const to = slug === undefined ? '/needs-you?tab=approvals' : `/p/${slug}/needs-you?tab=approvals`;
    return <Navigate to={to} replace />;
  }
  return <InboxPlace scope={slug ?? null} tab={tab} />;
}

function InboxPlace({ scope, tab }: { scope: string | null; tab: string | null }): JSX.Element {
  const { t } = useLingui();
  const [, setParams] = useSearchParams();
  const { items, loading, errors, reload, triage } = useInboxItems(scope, true);
  const [query, setQuery] = useState('');
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);
  const now = useNow(1000);

  const listTab = (INBOX_TABS.find((tabDef) => tabDef.id === tab)?.id ?? 'all') as InboxTabId;
  const onHandled = listTab === 'handled';
  const capped = listTab === 'all';
  const open = useMemo(() => reviewable(items), [items]);
  const groups = useMemo(
    () => groupByKind(filterTab(filterItems(open, query), listTab)),
    [open, query, listTab],
  );
  const ordered = useMemo(
    () => groups.flatMap((g) => (capped ? g.items.slice(0, INBOX_GROUP_CAP) : g.items)),
    [groups, capped],
  );
  const handled = useMemo(() => {
    const q = query.trim().toLowerCase();
    return triage.handled
      .filter((v) => q === '' || v.title.toLowerCase().includes(q))
      .sort((a, b) => b.createdAt.localeCompare(a.createdAt));
  }, [triage.handled, query]);
  const keysInList = onHandled ? handled.map(handledKey) : ordered.map((i) => i.key);

  const selectedVerdict = onHandled ? (handled.find((v) => handledKey(v) === selectedId) ?? handled[0]) : undefined;
  const selected = onHandled ? undefined : (ordered.find((i) => i.key === selectedId) ?? ordered[0]);
  const currentKey = selectedVerdict !== undefined ? handledKey(selectedVerdict) : (selected?.key ?? null);

  const select = (key: string | undefined): void => {
    if (key === undefined) return;
    setSelectedId(key);
    setActionError(null);
  };

  const pickTab = (next: InboxTabId): void => {
    setSelectedId(null);
    setActionError(null);
    setParams(
      (prev) => {
        const q = new URLSearchParams(prev);
        if (next === 'all') q.delete('tab');
        else q.set('tab', next);
        return q;
      },
      { replace: true },
    );
  };

  const neighbourKey = (key: string): string | null => {
    const idx = keysInList.indexOf(key);
    return keysInList[idx + 1] ?? keysInList[idx - 1] ?? null;
  };
  const neighbour = (item: InboxItem): string | null => neighbourKey(item.key);

  const run = (item: InboxItem, action: () => Promise<unknown>): void => runAction(neighbour(item), action);
  const undo = (v: TriageVerdict): void => runAction(neighbourKey(handledKey(v)), () => undoTriageVerdict(v.id));

  const runAction = (next: string | null, action: () => Promise<unknown>): void => {
    if (busy) return;
    setBusy(true);
    setActionError(null);
    action()
      .then(() => {
        setSelectedId(next);
        reload();
        triage.reload();
      })
      .catch((e: unknown) => {
        setActionError(e instanceof Error ? e.message : String(e));
        // The item changed under the agent's suggestion: show what is there now.
        if (e instanceof TriageConflictError) {
          reload();
          triage.reload();
        }
      })
      .finally(() => setBusy(false));
  };

  const skip = (item: InboxItem): void => select(neighbour(item) ?? item.key);

  const primaryHint = onHandled
    ? t`undo`
    : selected?.suggestion !== undefined && !selected.suggestion.sample
      ? suggestionActionLabel(selected.suggestion.value)
      : selected?.kind === 'review'
        ? t`ack`
        : selected?.kind === 'proposal'
          ? t`approve`
          : t`accept`;
  const secondaryHint =
    selected === undefined || selected.kind === 'review' || selected.kind === 'alert'
      ? undefined
      : selected.kind === 'classifier'
        ? t`skip`
        : t`dismiss`;
  const openCount = String(open.length);
  const handledCount = String(triage.handled.length);
  const counts = tabCounts(open, triage.handled.length);

  const summaryOf = (item: InboxItem): string => {
    const expires = item.urgent && item.expiresIso !== undefined ? expiresInLabel(item.expiresIso, now) : null;
    return [
      item.context,
      ...(expires === null ? [] : [t`expires ${expires}`]),
      ...(scope !== null && FLEET_WIDE_KINDS.has(item.kind) ? [t`fleet-wide`] : []),
    ].join(' · ');
  };

  const agentTag = (item: InboxItem): string | undefined => {
    if (item.suggestion === undefined) return undefined;
    const value = valueWording(item.suggestion.value);
    return t`agent: ${value}`;
  };

  return (
    <PlaceLayout
      list={
        <ListPane
          title={t`Inbox`}
          sub={
            loading
              ? t`loading…`
              : onHandled
                ? t`${handledCount} closed by the agent · 7 d · undo any of them`
                : t`${openCount} to review · nothing here blocks a session`
          }
          query={query}
          onQueryChange={setQuery}
          searchHint={t`filter`}
          strip={
            <ListTabs
              tabs={INBOX_TABS.filter(
                (tabDef) =>
                  tabDef.kind === undefined || counts[tabDef.id] > 0 || tabDef.id === listTab,
              ).map((tabDef) => ({ id: tabDef.id, label: tabDef.label, count: counts[tabDef.id] }))}
              value={listTab}
              onChange={pickTab}
              ariaLabel={t`Inbox`}
            />
          }
          {...(loading || keysInList.length > 0
            ? {}
            : {
                empty: (
                  <ListEmpty>
                    {onHandled
                      ? t`The agent has not closed anything in the last 7 days.`
                      : capped
                        ? t`All reviewed.`
                        : t`Nothing here.`}
                  </ListEmpty>
                ),
              })}
          keys={{
            onNext: () => select(stepKey(keysInList, currentKey, 1)),
            onPrev: () => select(stepKey(keysInList, currentKey, -1)),
            onPrimary: () => {
              if (selectedVerdict !== undefined) undo(selectedVerdict);
              if (selected === undefined) return;
              const a = primaryAction(selected);
              if (a !== null) run(selected, a);
            },
            onSecondary: () => {
              if (selected === undefined) return;
              const a = denyAction(selected);
              if (a !== null) run(selected, a);
              else if (selected.kind === 'classifier') skip(selected);
            },
          }}
          primaryHint={primaryHint}
          {...(secondaryHint === undefined ? {} : { secondaryHint })}
        >
          {loading && <Loading label={t`loading…`} />}
          {groups.map((g) => (
            <ListGroup
              key={g.kind}
              label={KIND_META[g.kind].label}
              count={g.items.length}
              tone={GROUP_TONE[g.kind]}
              hideHead={!capped}
            >
              {(capped ? g.items.slice(0, INBOX_GROUP_CAP) : g.items).map((item) => (
                <div key={item.key} data-testid="inbox-row" data-kind={item.kind}>
                  <ListRow
                    glyph={<KindDot item={item} />}
                    title={item.title}
                    tag={agentTag(item)}
                    summary={summaryOf(item)}
                    age={compactSpan(now - Date.parse(item.ageIso))}
                    selected={item.key === currentKey}
                    onSelect={() => select(item.key)}
                  />
                </div>
              ))}
              {capped && g.items.length > INBOX_GROUP_CAP && (
                <MoreRow
                  more={g.items.length - INBOX_GROUP_CAP}
                  onClick={() => {
                    const id = INBOX_TABS.find((tabDef) => tabDef.kind === g.kind)?.id;
                    if (id !== undefined) pickTab(id);
                  }}
                />
              )}
            </ListGroup>
          ))}
          {onHandled &&
            handled.map((v) => (
              <div key={v.id} data-testid="inbox-handled-row">
                <ListRow
                  glyph={<StateGlyph state="done" size={11} />}
                  title={v.title}
                  summary={v.reason === '' ? verdictWording(v) : `${verdictWording(v)} · ${v.reason}`}
                  age={compactSpan(now - Date.parse(v.createdAt))}
                  selected={handledKey(v) === currentKey}
                  onSelect={() => select(handledKey(v))}
                />
              </div>
            ))}
        </ListPane>
      }
      detail={
        <>
          {errors.length > 0 && (
            <div className="flex flex-wrap gap-3 border-b border-line px-7 py-2 font-mono text-[11px] text-red">
              {errors.map((k) => {
                const source = INBOX_TABS.find((tabDef) => tabDef.kind === k)?.label ?? k;
                return (
                  <span key={k} role="alert">
                    <Trans>couldn't load {source}</Trans>
                  </span>
                );
              })}
            </div>
          )}
          {selectedVerdict !== undefined ? (
            <HandledDetail
              key={selectedVerdict.id}
              verdict={selectedVerdict}
              busy={busy}
              error={actionError}
              onUndo={() => undo(selectedVerdict)}
              now={now}
            />
          ) : selected !== undefined ? (
            <InboxDetail
              key={selected.key}
              item={selected}
              fleetWide={scope !== null && FLEET_WIDE_KINDS.has(selected.kind)}
              busy={busy}
              error={actionError}
              onAct={(a) => run(selected, a)}
              onSkip={() => skip(selected)}
              now={now}
              audit={triage.audit}
            />
          ) : (
            !loading &&
            !onHandled && (
              <div className="flex h-full flex-col items-center justify-center px-7 text-center text-ink-3">
                <div className="text-[18px] font-medium text-ink-2">
                  <Trans>All reviewed.</Trans>
                </div>
                <div className="mt-1.5 max-w-[42ch] text-[12.5px] leading-[1.55]">
                  <Trans>
                    New items land here after runs are measured: lesson candidates, Advisor findings, classifier checks,
                    agent changes.
                  </Trans>
                </div>
              </div>
            )
          )}
        </>
      }
    />
  );
}
