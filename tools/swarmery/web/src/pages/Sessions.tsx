// Sessions place (handoff §1, §5): the one list column beside the open session.
//
// Routes. /sessions, /sessions/:id and their /p/:slug twins all render this one
// component (pages/SessionDetail.tsx re-exports it under the route table's name),
// so the list keeps its state, its scroll and its loaded pages across a selection.
// With no :id the first row is open, as the design opens it, and the URL stays.
// An id may be a row id or a session uuid: the notch app opens /sessions/<uuid>.
//
// Structure. Groups: Needs you, Working, Done today, then Earlier (the design has
// no older sessions, the product does, and the infinite scroll stays there) and a
// closed Plan runs group, which keeps the fan-out audit view. FileContentionBanner
// stays at the top of the list: it renders null unless two live sessions share a
// file. The filter chips went into the field (`n:name s:state o:result`); the
// controls with no place in the field (accounts, bulk continue-phase, a leftover
// project scope) keep a row under it that appears only when one of them applies.
//
// Data flow (unchanged). Only the project scope goes to the API (?project=);
// filtering is client-side. WS keeps the window live; a request generation drops
// stale pages when the scope changes; an IntersectionObserver sentinel appends the
// next page. Arrangement is pure and lives in lib/sessionsView.ts.

import { plural } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import type { NeedsYouItem, Session, WSMessage } from '../api/types';
import { fetchSessions } from '../api';
import { liveActionText } from '../lib/payload';
import { useScope } from '../lib/scope';
import { accountKey, accountKeys } from '../lib/sessionAccount';
import { useSessionHref } from '../lib/sessionHref';
import {
  buildDays,
  buildGroups,
  gatesBySession,
  linkSelection,
  matchesFilter,
  parseListQuery,
  rowState,
  runsOnly,
  type SessionGroup,
} from '../lib/sessionsView';
import { useNowMs } from '../lib/sessionState';
import { SwarmConflictProvider } from '../lib/swarm';
import { useNeedsYou } from '../lib/useNeedsYou';
import { stepKey } from '../lib/useGlobalKeys';
import { applySessionMessage, useLiveUpdates } from '../lib/ws';
import { BULK_PROMPT_MAX, BulkPromptBar } from '../components/BulkPromptBar';
import { FileContentionBanner } from '../components/FileContentionBanner';
import { ListGroup, type GroupTone } from '../components/place/ListGroup';
import { ListPane } from '../components/place/ListPane';
import { PlaceLayout } from '../components/place/PlaceLayout';
import { PlanRunCard } from '../components/PlanRunCard';
import { ScopeChip } from '../components/ScopeChip';
import { SessionRow } from '../components/SessionRow';
import { Empty, ErrorBox, Loading } from '../components/ui';
import { SessionPane, type PaneActions } from './detail/SessionPane';

const PAGE_LIMIT = 100;

/** `+ new` is the palette's dispatch (plan D-5), and the Sidebar owns the palette. */
function openPalette(): void {
  window.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', metaKey: true, bubbles: true }));
}

function FilterChip({
  selected,
  onClick,
  children,
}: {
  selected: boolean;
  onClick: () => void;
  children: string;
}): JSX.Element {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={selected}
      className={`shrink-0 rounded-[6px] border px-2 py-[3px] font-mono text-[10.5px] leading-[normal] whitespace-nowrap transition-colors ${
        selected ? 'border-line-strong bg-surface2 text-ink' : 'border-line text-ink-dim hover:text-ink'
      }`}
    >
      {children}
    </button>
  );
}

/** The list, wrapped so every row and the detail share ONE claims fetch. */
export function Sessions(): JSX.Element {
  const { scopeProject } = useScope();
  return (
    <SwarmConflictProvider projectId={scopeProject?.id}>
      <SessionsPlace />
    </SwarmConflictProvider>
  );
}

function SessionsPlace(): JSX.Element {
  const { t } = useLingui();
  const { scope, scopeProject } = useScope();
  const { id, slug } = useParams<{ id?: string; slug?: string }>();
  const navigate = useNavigate();
  const sessionHref = useSessionHref();
  const nowMs = useNowMs();
  // Project mode (/p/:slug/sessions) pins the scope to the URL project, so reading
  // the context covers both modes.
  const effectiveScope = scope;
  // Local, not the page-search context: that one clears on every pathname change,
  // and j/k moves the pathname.
  const [query, setQuery] = useState('');
  // null = every subscription (migration 0047); a key narrows to one account.
  const [account, setAccount] = useState<string | null>(null);
  const [runsOpen, setRunsOpen] = useState(false);
  const [expanded, setExpanded] = useState<ReadonlySet<number>>(new Set());
  const [sessions, setSessions] = useState<Session[] | null>(null);
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [nowById, setNowById] = useState<Record<number, string>>({});
  const [selectMode, setSelectMode] = useState(false);
  const [selected, setSelected] = useState<number[]>([]);
  const loadingMoreRef = useRef(false);
  const sentinelRef = useRef<HTMLDivElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  // What `e` and `x` do on the open session: its gate's actions, or the composer.
  const paneActions = useRef<PaneActions>({});
  // Request generation: bumped whenever the scope resets the list so stale
  // in-flight page responses (old scope) are dropped, not appended.
  const genRef = useRef(0);

  const needsYou = useNeedsYou(effectiveScope);
  const gates = useMemo(() => gatesBySession(needsYou.items), [needsYou.items]);

  // First page, and the WS-reconnect refetch: the live socket keeps the loaded
  // window fresh in between.
  const load = useCallback((): void => {
    const gen = genRef.current;
    const filters: { project?: string } = {};
    if (effectiveScope !== null) filters.project = effectiveScope;
    fetchSessions(filters, { limit: PAGE_LIMIT })
      .then((page) => {
        if (gen !== genRef.current) return; // stale — filter changed mid-flight
        setSessions(page.sessions);
        setNextCursor(page.nextCursor);
        setError(null);
      })
      .catch((e: unknown) => {
        if (gen !== genRef.current) return;
        setError(String(e));
      });
  }, [effectiveScope]);

  useEffect(() => {
    genRef.current += 1; // invalidate in-flight responses for the old filter
    setSessions(null);
    setNextCursor(null);
    load();
  }, [load]);

  // Next page: append, dedup by id (a WS prepend may already hold a row).
  const loadMore = useCallback((): void => {
    if (nextCursor === null || loadingMoreRef.current) return;
    loadingMoreRef.current = true;
    const gen = genRef.current;
    const filters: { project?: string } = {};
    if (effectiveScope !== null) filters.project = effectiveScope;
    fetchSessions(filters, { limit: PAGE_LIMIT, cursor: nextCursor })
      .then((page) => {
        if (gen !== genRef.current) return; // stale — would leak old-project rows
        setSessions((prev) => {
          const seen = new Set((prev ?? []).map((s) => s.id));
          return [...(prev ?? []), ...page.sessions.filter((s) => !seen.has(s.id))];
        });
        setNextCursor(page.nextCursor);
      })
      .catch((e: unknown) => {
        if (gen !== genRef.current) return;
        setError(String(e));
      })
      .finally(() => {
        loadingMoreRef.current = false;
      });
  }, [nextCursor, effectiveScope]);

  // Infinite scroll: a sentinel after the last group fetches the next page while
  // one exists (rootMargin prefetches before it is visible).
  useEffect(() => {
    const el = sentinelRef.current;
    if (el === null || nextCursor === null) return undefined;
    const io = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting)) loadMore();
      },
      { rootMargin: '400px' },
    );
    io.observe(el);
    return () => io.disconnect();
  }, [nextCursor, loadMore]);

  // Rows carry the DB path slug, while scope may be the pretty name slug —
  // compare against the resolved project's slug (raw scope as fallback).
  const matchesProject = useCallback(
    (s: Session): boolean =>
      effectiveScope === null || s.projectSlug === (scopeProject?.slug ?? effectiveScope),
    [effectiveScope, scopeProject],
  );

  const onMessage = useCallback(
    (msg: WSMessage): void => {
      if (msg.type === 'event_appended') {
        const text = liveActionText(msg.payload.event);
        if (text !== null) {
          const { sessionId } = msg.payload;
          setNowById((prev) => ({ ...prev, [sessionId]: text }));
        }
        return;
      }
      setSessions((prev) => {
        if (prev === null) return prev;
        const next = applySessionMessage(prev, msg);
        return next.filter(matchesProject);
      });
    },
    [matchesProject],
  );
  useLiveUpdates(onMessage, load);

  const filter = parseListQuery(query);
  const loaded = (sessions ?? []).filter((s) => matchesFilter(s, filter, rowState(s, nowMs, gates.get(s.id))));

  // Subscription tallies over the same list, so filtering by account never
  // changes what the account chips offer.
  const accounts = accountKeys(loaded);
  const accountCounts: Record<string, number> = {};
  for (const s of loaded) {
    const k = accountKey(s);
    accountCounts[k] = (accountCounts[k] ?? 0) + 1;
  }

  const sorted = loaded
    .filter((s) => account === null || accountKey(s) === account)
    .sort((a, b) => b.startedAt.localeCompare(a.startedAt));
  const groups = buildGroups(sorted, nowMs, gates);
  const ordered = [...groups.needs, ...groups.working, ...groups.done, ...groups.earlier];
  const rowKeys = ordered.map((s) => String(s.id));
  const days = buildDays(sorted);
  const runs = runsOnly(days).flatMap((d) => d.items.flatMap((it) => (it.kind === 'run' ? [it.group] : [])));

  // No :id opens the first row, and keeps it open while the list re-sorts under
  // it: a WS frame must not switch the session the operator is reading.
  const [autoId, setAutoId] = useState<string | null>(null);
  const openId = id ?? (autoId !== null && rowKeys.includes(autoId) ? autoId : (rowKeys[0] ?? null));
  useEffect(() => {
    if (id === undefined) setAutoId(openId);
  }, [id, openId]);
  const openRow = ordered.find((s) => String(s.id) === openId || s.sessionUuid === openId);
  const openKey = openRow === undefined ? openId : String(openRow.id);

  // j/k moves the selection; keep the moved-to row in view.
  useEffect(() => {
    listRef.current?.querySelector('[aria-current="true"]')?.scrollIntoView?.({ block: 'nearest' });
  }, [openKey]);

  const step = (dir: 1 | -1): void => {
    const next = stepKey(rowKeys, openKey, dir);
    if (next !== undefined && next !== openKey) navigate(sessionHref(next));
  };

  // Bulk "continue phase" (link control channel): offered while any loaded session
  // has a link mod; only rows whose mod polls for commands get a checkbox. A
  // selection a later filter hides is kept but never sent.
  const hasLinked = loaded.some((s) => s.link != null);
  const {
    selectable,
    visible: visibleSelected,
    hidden: hiddenSelected,
    notPolling: notPollingSelected,
  } = linkSelection(days, selected);
  const byId = new Map(loaded.map((s) => [s.id, s] as const));
  const toggleSelected = (rowId: number): void => {
    setSelected((prev) => (prev.includes(rowId) ? prev.filter((x) => x !== rowId) : [...prev, rowId]));
  };

  const toggleRun = (taskId: number): void => {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(taskId)) next.delete(taskId);
      else next.add(taskId);
      return next;
    });
  };

  const row = (s: Session): JSX.Element => {
    const body = (
      <SessionRow
        session={s}
        now={nowById[s.id] ?? null}
        gate={gates.get(s.id)}
        selected={String(s.id) === openKey}
        hideProject={effectiveScope !== null}
      />
    );
    if (!selectMode) return <div key={s.id}>{body}</div>;
    return (
      <div key={s.id} className="flex items-start">
        <span className="flex w-6 shrink-0 justify-center pt-[11px]">
          {s.link?.controllable === true && s.planGroup == null && (
            <input
              type="checkbox"
              checked={selected.includes(s.id)}
              onChange={() => toggleSelected(s.id)}
              aria-label={t`select ${s.title ?? s.sessionUuid} for continue phase`}
            />
          )}
        </span>
        <div className="min-w-0 flex-1">{body}</div>
      </div>
    );
  };

  const group = (key: SessionGroup, label: string, tone: GroupTone): JSX.Element | null =>
    groups[key].length === 0 ? null : (
      <ListGroup key={key} label={label} count={groups[key].length} tone={tone}>
        {groups[key].map(row)}
      </ListGroup>
    );

  const showFilters = accounts.length >= 2 || hasLinked || selectMode || (scope !== null && slug === undefined);
  const needCount = groups.needs.length;
  const countLine = plural(loaded.length, {
    one: '# session',
    few: '# sessions',
    many: '# sessions',
    other: '# sessions',
  });

  const emptyList =
    error !== null ? (
      <ErrorBox message={error} onRetry={load} />
    ) : sessions === null ? (
      <Loading label={t`sessions…`} />
    ) : ordered.length === 0 ? (
      <Empty>
        {query !== '' ? (
          <Trans>no sessions match the current filter — try a different search or clear it</Trans>
        ) : account !== null ? (
          <Trans>no sessions from the {account} account — clear the account filter to see the rest</Trans>
        ) : (
          <Trans>Nothing here. Dispatch a task with + new, or widen the project scope.</Trans>
        )}
      </Empty>
    ) : undefined;

  const openGate: NeedsYouItem | undefined = openRow === undefined ? undefined : gates.get(openRow.id);
  const list = (
    <ListPane
      title={t`Sessions`}
      sub={needCount > 0 ? t`${countLine} · ${needCount} need you` : countLine}
      query={query}
      onQueryChange={setQuery}
      searchHint={t`filter · n:name s:state o:result`}
      onNew={openPalette}
      tabs={
        showFilters ? (
          <>
            {/* Renders null in project mode — the guard lives inside ScopeChip. */}
            <ScopeChip />
            {accounts.length >= 2 &&
              accounts.map((k) => (
                <FilterChip key={k} selected={account === k} onClick={() => setAccount(account === k ? null : k)}>
                  {`${k} · ${String(accountCounts[k] ?? 0)}`}
                </FilterChip>
              ))}
            {(hasLinked || selectMode) && (
              <FilterChip
                selected={selectMode}
                onClick={() => {
                  setSelectMode((on) => !on);
                  setSelected([]);
                }}
              >
                {t`continue phase…`}
              </FilterChip>
            )}
          </>
        ) : undefined
      }
      empty={emptyList}
      keys={{
        onNext: () => step(1),
        onPrev: () => step(-1),
        onPrimary: () => paneActions.current.primary?.(),
        onSecondary: () => paneActions.current.secondary?.(),
      }}
    >
      <div ref={listRef}>
        <div className="px-[14px] empty:hidden">
          {/* Files two or more live sessions touched recently — null when none. */}
          <FileContentionBanner project={effectiveScope} />
        </div>
        {selectMode && (
          <div className="px-[14px]">
            <BulkPromptBar
              selected={visibleSelected}
              hiddenCount={hiddenSelected}
              notPollingCount={notPollingSelected}
              selectableCount={selectable.length}
              byId={byId}
              onSelectAll={() => setSelected(selectable.slice(0, BULK_PROMPT_MAX).map((s) => s.id))}
              onClear={() => setSelected([])}
            />
          </div>
        )}
        {group('needs', t`Needs you`, 'amber')}
        {group('working', t`Working`, 'green')}
        {group('done', t`Done today`, 'default')}
        {runs.length > 0 && (
          <ListGroup
            label={t`Plan runs`}
            count={runs.length}
            open={runsOpen}
            onToggle={() => setRunsOpen((on) => !on)}
          >
            <div className="px-[14px] pt-1">
              {runs.map((run) => (
                <PlanRunCard
                  key={run.taskId}
                  run={run}
                  expanded={expanded.has(run.taskId)}
                  onToggle={() => toggleRun(run.taskId)}
                  slug={slug}
                  hideProject={effectiveScope !== null}
                  nowById={nowById}
                />
              ))}
            </div>
          </ListGroup>
        )}
        {group('earlier', t`Earlier`, 'default')}
        {nextCursor !== null && (
          <div ref={sentinelRef} className="py-6 text-center font-mono text-[11px] text-ink-faint">
            <Trans>loading more…</Trans>
          </div>
        )}
      </div>
    </ListPane>
  );

  return (
    <PlaceLayout
      narrow={id === undefined ? 'list' : 'detail'}
      list={list}
      detail={
        openId === null ? (
          sessions === null ? undefined : (
            <Empty>
              <Trans>Pick a session on the left.</Trans>
            </Empty>
          )
        ) : (
          <SessionPane
            key={openId}
            id={openId}
            listHref={slug !== undefined ? `/p/${slug}/sessions` : '/sessions'}
            gate={openGate}
            onGateChanged={needsYou.reload}
            actions={paneActions}
            now={openRow === undefined ? null : (nowById[openRow.id] ?? null)}
          />
        )
      }
    />
  );
}
