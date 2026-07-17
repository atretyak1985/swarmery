// Session detail (Redesign parity): the header block (breadcrumb, title,
// facts row, TOKENS/COST/ERRORS numerals, tab strip) is PINNED — only the tab
// content (Chat | Timeline | Diffs, Chat first and default) scrolls, and the
// desktop right rail (agents / skills / files changed) scrolls in its own
// column. Tabs deep-link via ?tab=timeline|diffs. Live: session_updated
// merges header state; event_appended is attributed via its sessionId and
// appended (or, for refined durations, replaced in place) on the open detail.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link, useParams, useSearchParams } from 'react-router-dom';
import type { SessionDetail, SessionOutcome, SessionStatus, WSMessage } from '../api/types';
import { fetchSession, patchSessionOutcome, renameSession } from '../api';
import { fmtAgo, fmtCost, fmtSpan, fmtTokens } from '../lib/format';
import { useLiveUpdates } from '../lib/ws';
import { OutcomePicker } from '../components/OutcomePicker';
import { TaskChip } from '../components/TaskChip';
import { ProjectName } from '../components/ProjectName';
import { ErrorBox, Loading } from '../components/ui';
import { Timeline } from './detail/Timeline';
import { Diffs } from './detail/Diffs';
import { Chat } from './detail/Chat';
import { CommandInput } from './detail/CommandInput';
import { SummaryChips } from './detail/SummaryChips';
import { DetailRail } from './detail/DetailRail';

const STATUS_TONES: Record<SessionStatus, string> = {
  active: 'text-green',
  waiting_approval: 'text-amber',
  idle: 'text-ink-dim',
  completed: 'text-ink-dim',
  killed: 'text-red',
};

type Tab = 'chat' | 'timeline' | 'diffs';

/** ?tab=timeline|diffs deep-links; anything else (or absent) is the Chat default. */
function parseTab(value: string | null): Tab {
  return value === 'timeline' || value === 'diffs' ? value : 'chat';
}

function Kv({ label, value, tone = 'text-ink' }: { label: string; value: string; tone?: string }): JSX.Element {
  return (
    <span>
      {label} <b className={`font-medium ${tone}`}>{value}</b>
    </span>
  );
}

const TITLE_CLASS =
  'font-display text-[22px] leading-[1.2] font-medium tracking-[-0.01em] desk:text-[27px]';

/** Inline-editable session title. Click the ✎ (or the placeholder) to rename;
 * Enter/blur saves, Escape cancels. A blank value reverts to the ingested
 * title. */
function TitleEditor({
  title,
  onRename,
}: {
  title: string | null;
  onRename: (raw: string) => void;
}): JSX.Element {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState('');
  const inputRef = useRef<HTMLInputElement>(null);

  const begin = (): void => {
    setDraft(title ?? '');
    setEditing(true);
  };
  useEffect(() => {
    if (editing) inputRef.current?.select();
  }, [editing]);

  const commit = (): void => {
    setEditing(false);
    if (draft.trim() !== (title ?? '')) onRename(draft);
  };

  if (editing) {
    return (
      <input
        ref={inputRef}
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onBlur={commit}
        onKeyDown={(e) => {
          if (e.key === 'Enter') {
            e.preventDefault();
            commit();
          }
          if (e.key === 'Escape') {
            e.preventDefault();
            setEditing(false);
          }
        }}
        placeholder="session title…"
        aria-label="session title"
        maxLength={120}
        className={`w-full rounded-[8px] border border-line-strong bg-field px-2 py-1 text-ink outline-none focus:border-ink-dim ${TITLE_CLASS}`}
      />
    );
  }
  return (
    <div className="group flex items-start gap-2.5">
      <h1
        role="button"
        tabIndex={0}
        onClick={begin}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault();
            begin();
          }
        }}
        title="click to rename"
        className={`cursor-text rounded-[6px] transition-colors hover:text-ink ${TITLE_CLASS} ${title === null ? 'text-ink-faint italic' : ''}`}
      >
        {title ?? '(untitled session)'}
      </h1>
      <button
        type="button"
        onClick={begin}
        aria-label="rename session"
        title="rename session"
        className="mt-[7px] shrink-0 rounded-md border border-line px-1.5 py-0.5 font-mono text-[16px] leading-none text-ink-dim opacity-60 transition-all hover:border-line-strong hover:text-ink hover:opacity-100 group-hover:opacity-100 focus-visible:opacity-100"
      >
        ✎
      </button>
    </div>
  );
}

export function SessionDetailPage(): JSX.Element {
  const { id } = useParams<{ id: string }>();
  const [detail, setDetail] = useState<SessionDetail | null>(null);
  const [error, setError] = useState<string | null>(null);
  // Optimistic echo of messages sent via the composer — shown immediately as
  // pending user bubbles until the real turn is ingested (dropped on match).
  const [pending, setPending] = useState<string[]>([]);
  const [searchParams, setSearchParams] = useSearchParams();
  const tab = parseTab(searchParams.get('tab'));
  const setTab = (next: Tab): void => {
    setSearchParams(next === 'chat' ? {} : { tab: next }, { replace: true });
  };

  const load = useCallback((): void => {
    if (id === undefined) return;
    fetchSession(id)
      .then((d) => {
        setDetail(d);
        setError(null);
      })
      .catch((e: unknown) => setError(String(e)));
  }, [id]);

  // New turns (chat bubbles) are NOT carried on the WS bus — only session_updated
  // (header fields) and event_appended (timeline events) are. So a coalesced
  // refetch on any activity for THIS session pulls fresh turns within ~0.5s
  // instead of waiting for the 60s reconcile net.
  const detailIdRef = useRef<number | null>(null);
  useEffect(() => {
    detailIdRef.current = detail?.id ?? null;
  }, [detail]);
  const reloadTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const scheduleLoad = useCallback((): void => {
    if (reloadTimer.current !== null) clearTimeout(reloadTimer.current);
    reloadTimer.current = setTimeout(() => {
      reloadTimer.current = null;
      load();
    }, 500);
  }, [load]);
  useEffect(
    () => () => {
      if (reloadTimer.current !== null) clearTimeout(reloadTimer.current);
    },
    [],
  );

  useEffect(() => {
    setDetail(null);
    setPending([]);
    load();
  }, [load]);

  // Reconcile: drop any optimistic bubble once its real user turn is ingested.
  useEffect(() => {
    if (detail === null) return;
    setPending((prev) =>
      prev.filter(
        (t) =>
          !detail.turns.some(
            (turn) => turn.role === 'user' && (turn.text ?? '').trim() === t.trim(),
          ),
      ),
    );
  }, [detail]);

  // The newest activity lives at the bottom — on tab switch (and first data
  // load) jump the panel to the end so live sessions open on "now". When a
  // rail file row was clicked, scroll to that file's diff group instead.
  const panelRef = useRef<HTMLDivElement | null>(null);
  const diffTargetRef = useRef<string | null>(null);
  const loaded = detail !== null;
  useEffect(() => {
    const panel = panelRef.current;
    if (panel === null || !loaded) return;
    const target = diffTargetRef.current;
    if (tab === 'diffs' && target !== null) {
      diffTargetRef.current = null;
      const group = panel.querySelector(`[data-diff-path="${CSS.escape(target)}"]`);
      if (group !== null) {
        (group as HTMLElement).scrollIntoView({ block: 'start' });
        return;
      }
    }
    panel.scrollTop = panel.scrollHeight;
  }, [tab, loaded]);

  const showDiffs = (path?: string): void => {
    diffTargetRef.current = path ?? null;
    setTab('diffs');
  };

  const onMessage = useCallback((msg: WSMessage): void => {
    setDetail((prev) => {
      if (prev === null) return prev;
      if (msg.type === 'session_started') return prev;
      if (msg.type === 'session_updated') {
        return msg.payload.id === prev.id ? { ...prev, ...msg.payload } : prev;
      }
      // Phase-2 permission_* messages are handled by the approvals UI (2.4),
      // not this detail view.
      if (msg.type !== 'event_appended') return prev;
      // event_appended: attributed directly via the payload's sessionId
      // (step-10 contract change). A known id is a re-broadcast of a refined
      // row (async subagent duration reconcile) — replace it in place.
      const { sessionId, event } = msg.payload;
      if (sessionId !== prev.id) return prev;
      const idx = prev.events.findIndex((e) => e.id === event.id);
      if (idx >= 0) {
        const events = prev.events.slice();
        events[idx] = event;
        return { ...prev, events };
      }
      return { ...prev, events: [...prev.events, event] };
    });
    // Either message type signals activity on a session — refetch turns for the
    // open one so new chat bubbles (ours and the agent's replies) appear live.
    const forThis =
      (msg.type === 'event_appended' && msg.payload.sessionId === detailIdRef.current) ||
      (msg.type === 'session_updated' && msg.payload.id === detailIdRef.current);
    if (forThis) scheduleLoad();
  }, [scheduleLoad]);
  useLiveUpdates(onMessage, load);

  const onSent = useCallback((text: string): void => {
    setPending((p) => [...p, text]);
  }, []);

  // Optimistic outcome toggle; revert on API failure.
  const setOutcome = (next: SessionOutcome | null): void => {
    if (detail === null) return;
    const prev = detail.outcome ?? null;
    setDetail({ ...detail, outcome: next });
    patchSessionOutcome(detail.id, next).catch(() => {
      setDetail((d) => (d === null ? d : { ...d, outcome: prev }));
    });
  };

  // Rename: blank clears the override (reverts to the ingested title). The
  // server's session_updated frame reconciles the effective title (incl. a
  // clear back to the ingested name, which the client can't compute); a
  // failure refetches the authoritative row.
  const rename = (raw: string): void => {
    if (detail === null) return;
    const next = raw.trim() === '' ? null : raw.trim();
    if (next !== null) setDetail({ ...detail, title: next });
    renameSession(detail.id, next).catch(() => {
      void load();
    });
  };

  const facts = useMemo(() => {
    if (detail === null) return null;
    let tokens = 0;
    let cost: number | null = null;
    for (const turn of detail.turns) {
      tokens += (turn.tokensIn ?? 0) + (turn.tokensOut ?? 0);
      if (turn.costUsd !== null) cost = (cost ?? 0) + turn.costUsd;
    }
    const errors = detail.events.filter(
      (e) =>
        e.type === 'error' ||
        e.status === 'error' ||
        e.status === 'denied' ||
        e.status === 'timeout',
    ).length;
    return { tokens, cost, errors };
  }, [detail]);

  if (error !== null) {
    return (
      <>
        <BackLink />
        <ErrorBox message={error} onRetry={load} />
      </>
    );
  }
  if (detail === null || facts === null) {
    return (
      <>
        <BackLink />
        <Loading label="session…" />
      </>
    );
  }

  const lastEvent = detail.events.length > 0 ? detail.events[detail.events.length - 1] : undefined;
  const diffCount = detail.fileChanges.length;

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="shrink-0 border-b border-line px-4 pt-4 pb-4 desk:px-10 desk:pt-6">
        <div className="flex items-center gap-2 font-mono text-[11px] text-ink-faint">
          <Link to="/sessions" className="shrink-0 transition-colors hover:text-ink">
            ← sessions
          </Link>
          <span aria-hidden="true">/</span>
          <span className="truncate">
            <ProjectName name={detail.projectName} slug={detail.projectSlug} />
            {detail.gitBranch !== null ? ` · ${detail.gitBranch}` : ''}
          </span>
        </div>
        <div className="mt-2 flex flex-wrap items-start gap-x-6 gap-y-3">
          <div className="min-w-0 flex-1">
            <TitleEditor title={detail.title} onRename={rename} />
            <div className="mt-2 flex flex-wrap gap-x-3.5 gap-y-[5px] font-mono text-[11px] text-ink-dim">
              <Kv label="status" value={detail.status} tone={STATUS_TONES[detail.status]} />
              {detail.model !== null && <Kv label="model" value={detail.model} />}
              <Kv
                label={detail.endedAt !== null ? 'duration' : 'running'}
                value={fmtSpan(detail.startedAt, detail.endedAt)}
              />
              {lastEvent !== undefined && detail.endedAt === null && (
                <Kv label="last event" value={fmtAgo(lastEvent.ts)} />
              )}
              {detail.taskExternalId != null && (
                /* phase 3.5: workspaces — which task card this session worked on. */
                <TaskChip
                  externalId={detail.taskExternalId}
                  linkSource={detail.taskLinkSource}
                  confidence={detail.taskConfidence}
                />
              )}
              <OutcomePicker value={detail.outcome ?? null} onChange={setOutcome} />
            </div>
          </div>
          <div className="flex shrink-0 flex-wrap gap-[22px]">
            <HeadStat value={fmtTokens(facts.tokens)} label="tokens" />
            <HeadStat value={fmtCost(facts.cost)} label="cost" tone="text-brand" />
            <HeadStat
              value={String(facts.errors)}
              label="errors"
              tone={facts.errors > 0 ? 'text-red' : 'text-ink-dim'}
            />
            {detail.recovered > 0 && (
              /* errors a later same-tool success cleared (backend heuristic). */
              <HeadStat value={String(detail.recovered)} label="recovered" tone="text-green" />
            )}
          </div>
        </div>

        {/* Mobile at-a-glance strip; the desktop rail replaces it at ≥1280px. */}
        <div className="wide:hidden">
          <SummaryChips events={detail.events} />
        </div>

        <div className="mt-4 flex gap-1">
          <TabButton active={tab === 'chat'} onClick={() => setTab('chat')}>
            Chat
          </TabButton>
          <TabButton active={tab === 'timeline'} onClick={() => setTab('timeline')}>
            Timeline
          </TabButton>
          <TabButton active={tab === 'diffs'} onClick={() => setTab('diffs')}>
            {`Diffs${diffCount > 0 ? ` · ${diffCount}` : ''}`}
          </TabButton>
        </div>
      </div>

      {/* Only this region scrolls: the tab panel (and, ≥1280px, the rail in
          its own column) — breadcrumb/title/facts/tab strip stay pinned. */}
      <div className="flex min-h-0 flex-1 flex-col wide:grid wide:grid-cols-[minmax(0,1fr)_300px] wide:grid-rows-[minmax(0,1fr)] wide:gap-6 wide:px-10">
        {/* Left column: the scrolling tab panel with, on the Chat tab, a pinned
            composer footer below it (the panel scrolls, the composer stays). */}
        <div className="flex min-h-0 min-w-0 flex-1 flex-col">
          <div
            role="tabpanel"
            ref={panelRef}
            className="min-h-0 flex-1 overflow-y-auto px-4 pb-6 desk:px-10 wide:px-0 [-webkit-overflow-scrolling:touch]"
          >
            {tab === 'chat' && (
              <Chat
                detail={detail}
                pending={pending}
                onShowTimeline={() => setTab('timeline')}
              />
            )}
            {tab === 'timeline' && <Timeline detail={detail} />}
            {tab === 'diffs' && <Diffs changes={detail.fileChanges} />}
          </div>
          {tab === 'chat' && (
            <CommandInput
              sessionId={detail.id}
              procState={detail.procState}
              resumeInFlight={detail.resumeInFlight ?? false}
              onSent={onSent}
            />
          )}
        </div>

        <div className="hidden min-h-0 wide:block wide:overflow-y-auto wide:py-6">
          <DetailRail
            events={detail.events}
            fileChanges={detail.fileChanges}
            onShowDiffs={showDiffs}
          />
        </div>
      </div>
    </div>
  );
}

function HeadStat({
  value,
  label,
  tone = 'text-ink',
}: {
  value: string;
  label: string;
  tone?: string;
}): JSX.Element {
  return (
    <div>
      <div className={`font-display text-[20px] leading-none font-semibold ${tone}`}>{value}</div>
      <div className="mt-0.5 font-mono text-[9.5px] tracking-[0.06em] text-ink-faint uppercase">
        {label}
      </div>
    </div>
  );
}

function BackLink(): JSX.Element {
  return (
    <Link
      to="/sessions"
      className="mb-2 block pt-0.5 font-mono text-[11px] text-ink-faint hover:text-ink"
    >
      ← sessions
    </Link>
  );
}

function TabButton({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: string;
}): JSX.Element {
  return (
    <button
      type="button"
      role="tab"
      aria-selected={active}
      onClick={onClick}
      className={`-mb-px min-h-11 border-b-2 px-3.5 py-[7px] text-[12.5px] font-medium transition-colors focus-visible:rounded-t-sm focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand ${
        active ? 'border-brand text-brand' : 'border-transparent text-ink-dim hover:text-ink'
      }`}
    >
      {children}
    </button>
  );
}
