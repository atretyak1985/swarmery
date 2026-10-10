// Sessions list — LAYOUT CONTRACT (redesign: day-first timeline).
//
// Structure. The ONLY top-level structure is the day timeline: `today · sat,
// aug 2`, `fri, aug 1`, … under mono eyebrow rules. Nothing is hoisted above it.
// The one exception is FileContentionBanner under the heading: an advisory that
// renders null unless two live sessions share a file, so it costs the common
// case nothing.
// A plan run does NOT get its own section: it renders INSIDE its day, positioned
// by its newest session, as ONE collapsed PlanRunCard row that fans out on
// click. Ordinary sessions render as flat SessionCard rows in the same day.
// (The old "plan groups on top + Other sessions below" split is gone: with a few
// plan runs it opened the page on a wall of boilerplate controller prompts and
// pushed the day list the operator wants far below the fold.)
//
// Views. `timeline` (default) is the above. `plan runs` is an audit view: only
// the run cards, still day-grouped, all expanded. It is offered only when the
// loaded window actually holds a plan run.
//
// Filters row (in page order): search · project scope chip · divider · status
// chips (+ account chips when the machine has >1 subscription) · view segment.
// The project scope chip is the SAME useScope() context the sidebar switcher
// used to drive — the control moved into the page, the scoping did not change.
//
// Data flow (unchanged). Only the project scope goes to the API (?project=);
// text search and status are filtered CLIENT-side so the chip counts always
// reflect the loaded, scoped list. WS keeps that window live; a request
// generation drops stale in-flight pages when the scope changes; an
// IntersectionObserver sentinel appends the next page. All arrangement is pure
// and lives in lib/sessionsView.ts (incl. merge-by-taskId, which is what keeps a
// fan-out split across a page boundary from becoming two cards).

import { Plural, Trans, useLingui } from '@lingui/react/macro';
import { useCallback, useEffect, useRef, useState } from 'react';
import { useParams } from 'react-router-dom';
import type { Session, SessionStatus, WSMessage } from '../api/types';
import { fetchSessions } from '../api';
import { liveActionText } from '../lib/payload';
import { usePageSearch } from '../lib/pageSearch';
import { useScope } from '../lib/scope';
import { accountKey, accountKeys } from '../lib/sessionAccount';
import {
  buildDays,
  countRuns,
  countSessions,
  matchesQuery,
  runsOnly,
  STATUS_LABELS,
  STATUSES,
  type DayBucket,
} from '../lib/sessionsView';
import { applySessionMessage, useLiveUpdates } from '../lib/ws';
import { ExplainPair } from '../components/Explain';
import { FileContentionBanner } from '../components/FileContentionBanner';
import { PageSearchInput } from '../components/PageSearchInput';
import { PlanRunCard } from '../components/PlanRunCard';
import { ScopeChip } from '../components/ScopeChip';
import { SessionCard } from '../components/SessionCard';
import { Empty, ErrorBox, GroupHeader, Loading } from '../components/ui';

const PAGE_LIMIT = 100;

/** How the list is arranged. `runs` is the audit view (run cards only). */
type View = 'timeline' | 'runs';

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
      className={`shrink-0 rounded-full border px-[11px] py-1 font-mono text-[10.5px] whitespace-nowrap transition-colors ${
        selected
          ? 'border-line-strong bg-surface2 text-ink'
          : 'border-line-strong text-ink-dim hover:text-ink'
      }`}
    >
      {children}
    </button>
  );
}

/** Status chip row — `all` clears the filter, then one chip per status. */
function StatusChips({
  status,
  onStatus,
  counts,
}: {
  status: SessionStatus | null;
  onStatus: (s: SessionStatus | null) => void;
  counts: Record<SessionStatus, number>;
}): JSX.Element {
  const { t } = useLingui();
  return (
    <>
      <FilterChip selected={status === null} onClick={() => onStatus(null)}>
        {t`all`}
      </FilterChip>
      {STATUSES.map((s) => (
        <FilterChip key={s} selected={status === s} onClick={() => onStatus(status === s ? null : s)}>
          {counts[s] > 0 ? `${STATUS_LABELS[s]} · ${String(counts[s])}` : STATUS_LABELS[s]}
        </FilterChip>
      ))}
    </>
  );
}

/** Subscription chips (migration 0047) — rendered ONLY when the loaded list
 * spans more than one Claude Code account. A one-subscription machine (the
 * common case) gets no extra control: a filter whose every value selects the
 * whole list is a decoration, not a filter.
 *
 * Client-side like the status chips, and for the same reason: the API's
 * ?account= filter would shrink the loaded window, and the chip counts are
 * computed over exactly the rows this page holds. */
function AccountChips({
  keys,
  account,
  onAccount,
  counts,
}: {
  keys: string[];
  account: string | null;
  onAccount: (a: string | null) => void;
  counts: Record<string, number>;
}): JSX.Element | null {
  if (keys.length < 2) return null;
  return (
    <>
      {keys.map((k) => (
        <FilterChip
          key={k}
          selected={account === k}
          onClick={() => onAccount(account === k ? null : k)}
        >
          {`${k} · ${String(counts[k] ?? 0)}`}
        </FilterChip>
      ))}
    </>
  );
}

/** Two-option segmented control: the day timeline vs the plan-run audit view. */
function ViewSegment({
  view,
  onView,
}: {
  view: View;
  onView: (v: View) => void;
}): JSX.Element {
  const { t } = useLingui();
  const seg = (v: View, text: string): JSX.Element => (
    <button
      type="button"
      onClick={() => onView(v)}
      aria-pressed={view === v}
      className={`shrink-0 rounded-full px-[11px] py-1 font-mono text-[10.5px] whitespace-nowrap transition-colors ${
        view === v ? 'bg-surface2 text-ink' : 'text-ink-dim hover:text-ink'
      }`}
    >
      {text}
    </button>
  );
  return (
    <div
      role="group"
      aria-label={t`list arrangement`}
      className="flex shrink-0 items-center gap-0.5 rounded-full border border-line-strong p-0.5"
    >
      {seg('timeline', t`timeline`)}
      {seg('runs', t`plan runs`)}
    </div>
  );
}

export function Sessions(): JSX.Element {
  const { t } = useLingui();
  // Project filtering comes from the in-page scope chip (global useScope
  // context); the title/plan filter comes from the in-page search input.
  const { scope, scopeProject } = useScope();
  const { slug } = useParams<{ slug?: string }>();
  // Project mode (/p/:slug/sessions) pins the scope to the URL project
  // (ProjectWorkspaceProvider), so reading the context covers both modes — and
  // the scope chip is hidden there, the URL already IS the project filter.
  const effectiveScope = scope;
  const query = usePageSearch();
  const [status, setStatus] = useState<SessionStatus | null>(null);
  // null = every subscription (migration 0047); a key narrows to one account.
  const [account, setAccount] = useState<string | null>(null);
  const [view, setView] = useState<View>('timeline');
  // EXPANDED, not collapsed: plan runs default to one collapsed row, so a run
  // that arrives over WS shows up folded like every other one.
  const [expanded, setExpanded] = useState<ReadonlySet<number>>(new Set());
  const [sessions, setSessions] = useState<Session[] | null>(null);
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [nowById, setNowById] = useState<Record<number, string>>({});
  const loadingMoreRef = useRef(false);
  const sentinelRef = useRef<HTMLDivElement>(null);
  // Request generation: bumped whenever the scope resets the list so stale
  // in-flight page responses (old scope) are dropped, not appended —
  // otherwise a slow page-2 fetch would leak old-scope rows and resurrect
  // the old cursor (scope filtering is server-side, not re-checked here).
  const genRef = useRef(0);

  // First page — also the WS-reconnect refetch: the live socket keeps the
  // loaded window fresh in between, so resetting to page 1 on reconnect is
  // the simplest correct behaviour (older pages reload on scroll).
  // Only the scope filter goes to the API — status stays client-side so
  // the chip counts can be computed over every status of the loaded list.
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

  // Infinite scroll: a sentinel row after the last day group fetches the next
  // page while one exists (rootMargin prefetches before it is visible).
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
        // step-10 contract: the payload carries sessionId → live "now" line.
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

  // Chip counts come from the scoped + text-filtered list (pre-status), over
  // the LOADED pages only — deeper history loads on scroll.
  const loaded = (sessions ?? []).filter((s) => matchesQuery(s, query));
  const counts: Record<SessionStatus, number> = {
    active: 0,
    waiting_approval: 0,
    awaiting_reply: 0,
    idle: 0,
    completed: 0,
    killed: 0,
  };
  for (const s of loaded) counts[s.status] += 1;

  // Subscription tallies over the same pre-status list, so switching status
  // never changes what the account chips offer.
  const accounts = accountKeys(loaded);
  const accountCounts: Record<string, number> = {};
  for (const s of loaded) {
    const k = accountKey(s);
    accountCounts[k] = (accountCounts[k] ?? 0) + 1;
  }

  // Account filtering happens here; STATUS filtering happens inside the
  // grouping pass, because a run card is kept whole when ANY of its rows
  // matches — filtering must never split a fan-out (lib/sessionsView).
  const sorted = loaded
    .filter((s) => account === null || accountKey(s) === account)
    .sort((a, b) => b.startedAt.localeCompare(a.startedAt));

  // Offered only when the loaded window actually holds a plan run — otherwise
  // the segment would promise an arrangement it cannot make. Computed
  // pre-status so a status chip never yanks the control out from under a click.
  const hasPlanGroups = sorted.some((s) => s.planGroup != null);
  const activeView: View = hasPlanGroups ? view : 'timeline';

  const timelineDays = buildDays(sorted, status);
  const days: DayBucket[] = activeView === 'runs' ? runsOnly(timelineDays) : timelineDays;
  const shownSessions = countSessions(days);
  const shownRuns = countRuns(days);
  const statusLabel = status !== null ? STATUS_LABELS[status] : '';

  const toggleRun = (taskId: number): void => {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(taskId)) next.delete(taskId);
      else next.add(taskId);
      return next;
    });
  };

  return (
    <div className="px-4 pt-6 pb-20 desk:px-10 desk:pt-[34px] desk:pb-28">
      {/* Stop vs Kill is explained once here, not beside each row's button:
          SessionCard's action slot renders for every live row and for every
          finished row with a known PID, so a per-row explainer would stamp a
          "?" down the whole list for a distinction read exactly once. */}
      {/* The h1 stays block-level and the pair does the flexing: an inline-flex
          heading shrink-wraps, and a heading that wraps then parks its trigger
          in the vertical middle of the right margin, touching neither line. */}
      <h1 className="font-display text-[26px] font-medium tracking-[-0.01em] desk:text-[30px]">
        <ExplainPair id="kill-vs-stop">
          <Trans>Sessions</Trans>
        </ExplainPair>
      </h1>
      <div className="mt-1.5 font-mono text-[11px] text-ink-dim">
        <Plural value={shownSessions} one="# sessions" few="# sessions" many="# sessions" other="# sessions" />
        {shownRuns > 0 && (
          <>
            {' · '}
            <Plural value={shownRuns} one="# plan runs" few="# plan runs" many="# plan runs" other="# plan runs" />
          </>
        )}
        {' · '}
        <Trans>newest first</Trans>
      </div>

      {/* Files two or more live sessions touched recently — null when none. */}
      <FileContentionBanner project={effectiveScope} />

      {/* search · project scope · | · status chips · view segment */}
      <div className="mt-5 flex flex-wrap items-center gap-2">
        <PageSearchInput />
        {/* Renders null in project mode — the guard lives inside ScopeChip. */}
        <ScopeChip />
        <span aria-hidden="true" className="hidden h-4 w-px shrink-0 bg-line sm:block" />
        <StatusChips status={status} onStatus={setStatus} counts={counts} />
        <AccountChips
          keys={accounts}
          account={account}
          onAccount={setAccount}
          counts={accountCounts}
        />
        {hasPlanGroups && <ViewSegment view={activeView} onView={setView} />}
      </div>

      {error !== null && <ErrorBox message={error} onRetry={load} />}
      {sessions === null && error === null && <Loading label={t`sessions…`} />}
      {sessions !== null && days.length === 0 && (
        <Empty>
          {query !== '' ? (
            <Trans>no sessions match the current filter — try a different search or clear it</Trans>
          ) : status !== null ? (
            <Trans>no {statusLabel} sessions — clear the status filter to see the rest</Trans>
          ) : account !== null ? (
            <Trans>no sessions from the {account} account — clear the account filter to see the rest</Trans>
          ) : (
            <Trans>
              no sessions yet — run{' '}
              <span className="font-mono text-ink">swarmery ingest &lt;file.jsonl&gt;</span>
            </Trans>
          )}
        </Empty>
      )}
      {days.map((day) => (
        <section key={day.label}>
          <GroupHeader>{day.label}</GroupHeader>
          {/* No `divide-y` on the container: a run renders as its OWN bordered
              card, so the row hairlines belong to the flat session rows only. */}
          <div>
            {day.items.map((item) =>
              item.kind === 'run' ? (
                <PlanRunCard
                  key={`run-${String(item.group.taskId)}`}
                  run={item.group}
                  // The audit view exists to READ the runs — everything open.
                  expanded={activeView === 'runs' || expanded.has(item.group.taskId)}
                  onToggle={() => toggleRun(item.group.taskId)}
                  slug={slug}
                  hideProject={effectiveScope !== null}
                  nowById={nowById}
                />
              ) : (
                <div
                  key={item.session.id}
                  className="border-b border-line-soft last:border-b-0"
                >
                  <SessionCard
                    session={item.session}
                    now={nowById[item.session.id] ?? null}
                    flat
                    hideProject={effectiveScope !== null}
                  />
                </div>
              ),
            )}
          </div>
        </section>
      ))}
      {nextCursor !== null && (
        <div ref={sentinelRef} className="py-6 text-center font-mono text-[11px] text-ink-faint">
          <Trans>loading more…</Trans>
        </div>
      )}
    </div>
  );
}
