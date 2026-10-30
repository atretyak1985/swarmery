// The Inbox (handoff §1, §8): what waits on the operator without blocking a
// session, fleet (/inbox) or one project (/p/:slug/inbox). A list grouped by kind,
// one decision in the detail; j/k move, e is the primary, x the secondary, and an
// item leaves once it is decided. Approvals and questions are decided in Needs you
// (`?tab=approvals` goes there); rules and history stay at approvals/manage.
//
// The triage banner is gone (handoff §11): the agent only proposes here, as an
// `agent: <value>` tag on the row and a suggestion in the detail. What it closed on
// its own stays reachable as the last group, each with an undo.

import { Trans, useLingui } from '@lingui/react/macro';
import { useEffect, useMemo, useState } from 'react';
import { Navigate, useParams, useSearchParams } from 'react-router-dom';
import { ListGroup, type GroupTone } from '../../components/place/ListGroup';
import { ListPane } from '../../components/place/ListPane';
import { ListRow } from '../../components/place/ListRow';
import { PlaceLayout } from '../../components/place/PlaceLayout';
import { StateGlyph } from '../../components/place/StateGlyph';
import { Loading } from '../../components/ui';
import { TriageConflictError } from '../../api/triage';
import { compactSpan } from '../../lib/sessionsView';
import { stepKey } from '../../lib/useGlobalKeys';
import { HandledList } from './HandledList';
import { InboxDetail, denyAction, primaryAction } from './InboxDetail';
import {
  INBOX_TABS,
  KIND_META,
  expiresInLabel,
  filterItems,
  groupByKind,
  reviewable,
  suggestionActionLabel,
  valueWording,
  type InboxItem,
  type InboxKind,
} from './inboxModel';
import { FLEET_WIDE_KINDS, useInboxItems } from './useInboxItems';

/** The row that opens the agent's closed items in the detail. */
const HANDLED = 'handled';

/** A group's label takes its kind's colour, as the row's dot does. */
const GROUP_TONE: Record<InboxKind, GroupTone> = {
  approval: 'amber',
  lesson: 'purple',
  advisor: 'blue',
  proposal: 'default',
  classifier: 'green',
  retire: 'red',
  alert: 'red',
  review: 'default',
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
  const { items, loading, errors, reload, triage } = useInboxItems(scope, true);
  const [query, setQuery] = useState('');
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);
  const now = useNow(1000);

  const open = useMemo(() => reviewable(items), [items]);
  const groups = useMemo(() => groupByKind(filterItems(open, query)), [open, query]);
  const ordered = useMemo(() => groups.flatMap((g) => g.items), [groups]);
  const handledCount = triage.handled.length;
  const showHandled = handledCount > 0 || tab === HANDLED;
  const keysInList = [...ordered.map((i) => i.key), ...(showHandled ? [HANDLED] : [])];

  // Old links name a kind: they open on it, and the rest of the list stays in view.
  const tabKind = INBOX_TABS.find((tabDef) => tabDef.id === tab)?.kind;
  const onHandled = selectedId === HANDLED || (selectedId === null && tab === HANDLED);
  const selected = onHandled
    ? undefined
    : (ordered.find((i) => i.key === selectedId) ?? ordered.find((i) => i.kind === tabKind) ?? ordered[0]);
  const currentKey = onHandled ? HANDLED : (selected?.key ?? null);

  const select = (key: string | undefined): void => {
    if (key === undefined) return;
    setSelectedId(key);
    setActionError(null);
  };

  const neighbour = (item: InboxItem): string | null => {
    const idx = ordered.findIndex((i) => i.key === item.key);
    return ordered[idx + 1]?.key ?? ordered[idx - 1]?.key ?? null;
  };

  const run = (item: InboxItem, action: () => Promise<unknown>): void => {
    if (busy) return;
    const next = neighbour(item);
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

  const primaryHint =
    selected?.suggestion !== undefined && !selected.suggestion.sample
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
          sub={loading ? t`loading…` : t`${openCount} to review · nothing here blocks a session`}
          query={query}
          onQueryChange={setQuery}
          searchHint={t`filter`}
          keys={{
            onNext: () => select(stepKey(keysInList, currentKey, 1)),
            onPrev: () => select(stepKey(keysInList, currentKey, -1)),
            onPrimary: () => {
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
            <ListGroup key={g.kind} label={KIND_META[g.kind].label} count={g.items.length} tone={GROUP_TONE[g.kind]}>
              {g.items.map((item) => (
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
            </ListGroup>
          ))}
          {showHandled && (
            <ListGroup label={t`handled by agent`} count={handledCount}>
              <ListRow
                glyph={<StateGlyph state="done" size={11} />}
                title={t`closed by the agent`}
                summary={t`the last 7 days, each with an undo`}
                selected={onHandled}
                onSelect={() => select(HANDLED)}
              />
            </ListGroup>
          )}
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
          {onHandled ? (
            <div className="px-7">
              <HandledList
                verdicts={triage.handled}
                onChanged={() => {
                  reload();
                  triage.reload();
                }}
                now={now}
              />
            </div>
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
            !loading && (
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
