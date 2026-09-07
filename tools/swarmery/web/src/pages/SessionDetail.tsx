// Session detail (Redesign parity): the header block (breadcrumb, title,
// facts row, TOKENS/COST/ERRORS numerals, tab strip) is PINNED — only the tab
// content (Chat | Timeline | Diffs, Chat first and default) scrolls, and the
// desktop right rail (agents / skills / files changed) scrolls in its own
// column. Tabs deep-link via ?tab=timeline|diffs. Live: session_updated
// merges header state; event_appended is attributed via its sessionId and
// appended (or, for refined durations, replaced in place) on the open detail.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link, useParams, useSearchParams } from 'react-router-dom';
import type { PendingSession, SessionDetail, SessionOutcome, SessionStatus, WSMessage } from '../api/types';
import {
  MOCK,
  extractSessionTasks,
  fetchSession,
  isPendingSession,
  patchSessionOutcome,
  renameSession,
  sendSessionMessage,
} from '../api';
import { fmtAgo, fmtCost, fmtSpan, fmtTokens } from '../lib/format';
import { accountLabel } from '../lib/sessionAccount';
import { sessionState, useNowMs } from '../lib/sessionState';
import { useLiveUpdates } from '../lib/ws';
import { ExplainPair } from '../components/Explain';
import { OutcomePicker } from '../components/OutcomePicker';
import { TaskChip } from '../components/TaskChip';
import { ProjectName } from '../components/ProjectName';
import { ErrorBox, Loading } from '../components/ui';
import { Timeline } from './detail/Timeline';
import { Diffs } from './detail/Diffs';
import { Chat, type PendingSend } from './detail/Chat';
import { CommandInput } from './detail/CommandInput';
import { SummaryChips } from './detail/SummaryChips';
import { DetailRail } from './detail/DetailRail';
import { PendingRunNotice } from './detail/PendingRunNotice';

/** Header liveness chip — the same tri-state the sessions list speaks (green
 * pulsing dot = the session produced transcript activity recently; amber =
 * quiet past the stuck window). Inside the detail page the list's dot was
 * missing, so an open detail view couldn't tell "working" from "silent". */
function LiveStateChip({ session }: { session: SessionDetail }): JSX.Element | null {
  const now = useNowMs(15_000);
  const state = sessionState(session, now);
  if (state === 'done') return null;
  if (state === 'running') {
    return (
      <span
        className="inline-flex items-center gap-1.5 rounded border border-green/40 bg-green/10 px-1.5 py-px font-mono text-[10px] text-green"
        data-tip="transcript activity within the stuck window — the session is working"
      >
        <span className="inline-block h-1.5 w-1.5 animate-pulse-dot rounded-full bg-green" />
        working · {fmtAgo(session.endedAt ?? session.startedAt)}
      </span>
    );
  }
  return (
    <span
      className="inline-flex items-center gap-1.5 rounded border border-amber/40 bg-amber/10 px-1.5 py-px font-mono text-[10px] text-amber"
      data-tip="no transcript activity past the stuck window — the session may have died silently"
    >
      <span className="inline-block h-1.5 w-1.5 rounded-full bg-amber" />
      quiet · {fmtSpan(session.endedAt ?? session.startedAt, null)}
    </span>
  );
}

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
        data-tip="click to rename"
        className={`cursor-text rounded-[6px] transition-colors hover:text-ink ${TITLE_CLASS} ${title === null ? 'text-ink-faint italic' : ''}`}
      >
        {title ?? '(untitled session)'}
      </h1>
      <button
        type="button"
        onClick={begin}
        aria-label="rename session"
        data-tip="rename session"
        className="mt-[7px] shrink-0 rounded-md border border-line px-1.5 py-0.5 font-mono text-[16px] leading-none text-ink-dim opacity-60 transition-all hover:border-line-strong hover:text-ink hover:opacity-100 group-hover:opacity-100 focus-visible:opacity-100"
      >
        ✎
      </button>
    </div>
  );
}

/** A pending bubble is abandoned to `failed` if no matching turn arrives within
 * this window (also the spec's reconcile horizon). */
const PENDING_STALE_MS = 120_000;
/** Re-poll cadence while a spawned run has not written its transcript yet. */
const PENDING_RUN_POLL_MS = 2_000;

/** Demo-only (VITE_MOCK=1) seed so the offline Chat tab shows the two optimistic
 * states — one still sending, one failed with a retry affordance. `sentAt: now`
 * keeps the pending one from immediately going stale; the failed one is seeded
 * directly. Never used when MOCK is false (real sessions start with []). */
function mockPending(): PendingSend[] {
  const now = Date.now();
  return [
    { key: 'mock-pending', text: 'Also add a --dry-run flag to the backfill command.', state: 'pending', sentAt: now },
    { key: 'mock-failed', text: 'Re-run the migration against the staging snapshot.', state: 'failed', sentAt: now },
  ];
}

export function SessionDetailPage(): JSX.Element {
  // `slug` is present only on the project mount (/p/:slug/sessions/:id) — the
  // back link has to return to the list in the SAME mode, otherwise the header
  // and sidebar flip out of the project workspace.
  const { id, slug } = useParams<{ id: string; slug?: string }>();
  const sessionsHref = slug != null ? `/p/${slug}/sessions` : '/sessions';
  const [detail, setDetail] = useState<SessionDetail | null>(null);
  // The 202 answer: a run the daemon spawned whose transcript is not ingested
  // yet. Rendered as "starting…" (and re-polled) instead of a 404 error.
  const [pendingRun, setPendingRun] = useState<PendingSession | null>(null);
  const [error, setError] = useState<string | null>(null);
  // Optimistic echo of messages sent via the composer — shown immediately as
  // pending user bubbles until the real turn is ingested (dropped on match) or
  // the send fails (flipped to a retry affordance).
  const [pending, setPending] = useState<PendingSend[]>([]);
  const [searchParams, setSearchParams] = useSearchParams();
  const tab = parseTab(searchParams.get('tab'));
  const setTab = (next: Tab): void => {
    setSearchParams(next === 'chat' ? {} : { tab: next }, { replace: true });
  };

  const load = useCallback((): void => {
    if (id === undefined) return;
    fetchSession(id)
      .then((d) => {
        if (isPendingSession(d)) {
          setPendingRun(d);
          setError(null);
          return;
        }
        setPendingRun(null);
        setDetail(d);
        setError(null);
      })
      .catch((e: unknown) => setError(String(e)));
  }, [id]);

  // While the run is still starting, poll: the session_started frame below is the
  // fast path, this is the safety net (a WS reconnect can drop that one frame).
  // Stops on its own once the run ends without a transcript (`running` false).
  useEffect(() => {
    if (detail !== null || pendingRun === null || !pendingRun.running) return;
    const t = setTimeout(load, PENDING_RUN_POLL_MS);
    return () => clearTimeout(t);
  }, [detail, pendingRun, load]);

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
    setPendingRun(null);
    // Real sessions always start clean; demo mode seeds two optimistic bubbles
    // (one sending, one failed) so the offline Chat tab shows both states.
    setPending(MOCK ? mockPending() : []);
    load();
  }, [load]);

  // Reconcile: drop any optimistic bubble once its real user turn is ingested —
  // matching role+text within a 2-minute window (Fusion reconcile-on-fetch). An
  // entry still unmatched past that window is flipped to `failed` (its retry
  // affordance re-sends): a resume that returned 202 but then died leaves no
  // turn to match, so the window is the only signal it never landed.
  useEffect(() => {
    if (detail === null) return;
    const userTexts = new Set(
      detail.turns
        .filter((turn) => turn.role === 'user' && turn.text !== null)
        .map((turn) => turn.text!.trim()),
    );
    setPending((prev) => {
      const now = Date.now();
      let changed = false;
      const next: PendingSend[] = [];
      for (const p of prev) {
        const matched = now - p.sentAt < PENDING_STALE_MS && userTexts.has(p.text.trim());
        if (matched) {
          changed = true; // reconciled away
          continue;
        }
        if (p.state === 'pending' && now - p.sentAt >= PENDING_STALE_MS) {
          changed = true;
          next.push({ ...p, state: 'failed' });
          continue;
        }
        next.push(p);
      }
      return changed ? next : prev;
    });
  }, [detail]);

  // Drive the stale→failed transition even without new detail frames (an idle
  // failed resume produces no WS activity), re-evaluating once a pending bubble
  // could cross the window. Cheap: only runs while something is pending.
  useEffect(() => {
    if (!pending.some((p) => p.state === 'pending')) return;
    const t = setTimeout(() => {
      const now = Date.now();
      setPending((prev) =>
        prev.map((p) =>
          p.state === 'pending' && now - p.sentAt >= PENDING_STALE_MS
            ? { ...p, state: 'failed' }
            : p,
        ),
      );
    }, PENDING_STALE_MS + 500);
    return () => clearTimeout(t);
  }, [pending]);

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

  // Jump-to-latest (Chat tab): auto-FOLLOW new content only while the reader is
  // already parked near the bottom (<48px), so scrolling up to read history is
  // never yanked back. `atBottom` is the sole state; the "↓ Latest" pill shows
  // its inverse. This is additive to the tab-switch jump above — it reads
  // scroll position and only scrolls on NEW content, so the two never fight.
  const [atBottom, setAtBottom] = useState(true);
  const scrollToLatest = useCallback((): void => {
    const panel = panelRef.current;
    if (panel !== null) panel.scrollTo({ top: panel.scrollHeight, behavior: 'smooth' });
  }, []);
  const onPanelScroll = useCallback((): void => {
    const panel = panelRef.current;
    if (panel === null) return;
    setAtBottom(panel.scrollHeight - panel.scrollTop - panel.clientHeight < 48);
  }, []);
  // Follow on new turns/events/pending — but only when parked at the bottom.
  const turnCount = detail?.turns.length ?? 0;
  const eventCount = detail?.events.length ?? 0;
  const pendingCount = pending.length;
  useEffect(() => {
    if (tab !== 'chat' || !atBottom) return;
    const panel = panelRef.current;
    if (panel !== null) panel.scrollTop = panel.scrollHeight;
    // `atBottom` intentionally omitted from deps: follow fires on CONTENT change,
    // reading the latest atBottom via closure would re-run on every scroll tick.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tab, turnCount, eventCount, pendingCount]);

  const showDiffs = (path?: string): void => {
    diffTargetRef.current = path ?? null;
    setTab('diffs');
  };

  const onMessage = useCallback((msg: WSMessage): void => {
    // The session this page is waiting for has just been ingested (the route
    // param is its uuid) — load the real detail. Any other session_started frame
    // carries nothing for an open detail view.
    if (msg.type === 'session_started') {
      if (msg.payload.sessionUuid === id || String(msg.payload.id) === id) load();
      return;
    }
    setDetail((prev) => {
      if (prev === null) return prev;
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
  }, [scheduleLoad, load, id]);
  useLiveUpdates(onMessage, load);

  const onSent = useCallback((text: string): void => {
    setPending((p) => [
      ...p,
      { key: crypto.randomUUID(), text, state: 'pending', sentAt: Date.now() },
    ]);
  }, []);

  // POST rejected: flip the most recent still-pending bubble with this text to
  // `failed` (match by text — the composer just created exactly one).
  const onSendFailed = useCallback((text: string, reason?: string): void => {
    setPending((p) => {
      const idx = p.map((e) => e.text === text && e.state === 'pending').lastIndexOf(true);
      const target = idx === -1 ? undefined : p[idx];
      if (target === undefined) return p;
      const next = p.slice();
      // Keep the server's reason on the bubble — it is the only place the
      // operator ever sees why the send was refused.
      next[idx] = { ...target, state: 'failed', reason };
      return next;
    });
  }, []);

  // Retry a failed bubble: reuse the SAME entry (reset to pending + fresh
  // window), re-POST, and re-fail it if the retry also rejects. Never appends a
  // second bubble.
  const onRetry = useCallback(
    (key: string): void => {
      let text: string | null = null;
      setPending((p) =>
        p.map((e) => {
          if (e.key !== key) return e;
          text = e.text;
          // Drop the stale reason while the retry is in flight, or the bubble
          // would read as failed-for-that-reason while it is actually sending.
          return { ...e, state: 'pending', sentAt: Date.now(), reason: undefined };
        }),
      );
      if (text === null || detail === null) return;
      sendSessionMessage(detail.id, text).catch((err: unknown) => {
        const reason = err instanceof Error ? err.message : String(err);
        setPending((p) => p.map((e) => (e.key === key ? { ...e, state: 'failed', reason } : e)));
      });
    },
    [detail],
  );

  // Optimistic outcome toggle; revert on API failure.
  // "Extract tasks": one paid model pass over this session, turning what it
  // left behind into suggested Triage cards. Manual by design — nothing here is
  // automatic, and the button is the only trigger the feature has.
  //
  // The request is held for the whole headless run (minutes), so `extracting`
  // drives a real pending state rather than an optimistic flash. The result is
  // a sentence, not a row the page can show: the cards land on the BOARD, and
  // they arrive there over the WS bus without this page doing anything.
  const [extracting, setExtracting] = useState(false);
  const [extractNote, setExtractNote] = useState<{ tone: 'ok' | 'err'; text: string } | null>(null);
  const runExtract = (): void => {
    if (detail === null || extracting) return;
    setExtracting(true);
    setExtractNote(null);
    extractSessionTasks(detail.id)
      .then((inserted) => {
        setExtractNote({
          tone: 'ok',
          text:
            inserted === 0
              ? 'No new tasks — this session had nothing left to suggest.'
              : `${String(inserted)} task${inserted === 1 ? '' : 's'} suggested on the board.`,
        });
      })
      .catch((e: unknown) => {
        setExtractNote({ tone: 'err', text: e instanceof Error ? e.message : String(e) });
      })
      .finally(() => setExtracting(false));
  };

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
        <BackLink to={sessionsHref} />
        <ErrorBox message={error} onRetry={load} />
      </>
    );
  }
  if (detail === null && pendingRun !== null) {
    return (
      <>
        <BackLink to={sessionsHref} />
        <PendingRunNotice run={pendingRun} onRetry={load} />
      </>
    );
  }
  if (detail === null || facts === null) {
    return (
      <>
        <BackLink to={sessionsHref} />
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
          <Link to={sessionsHref} className="shrink-0 transition-colors hover:text-ink">
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
              <LiveStateChip session={detail} />
              <Kv label="status" value={detail.status} tone={STATUS_TONES[detail.status]} />
              {detail.model !== null && <Kv label="model" value={detail.model} />}
              {/* Subscription this session ran under (migration 0047) — shown
                  only when it is NOT the default one, the same rule the list's
                  account badge follows: on a one-account machine the row would
                  read "account default" on every single session. */}
              {accountLabel(detail) !== null && (
                <Kv label="account" value={accountLabel(detail) ?? ''} />
              )}
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
              <ExplainPair id="session-outcome">
                <OutcomePicker value={detail.outcome ?? null} onChange={setOutcome} />
              </ExplainPair>
              <button
                type="button"
                onClick={runExtract}
                disabled={extracting}
                data-tip="run a model pass over this session and suggest board tasks from what it left behind"
                className="inline-flex items-center gap-1.5 rounded border border-line px-1.5 py-px font-mono text-[10px] text-ink-dim transition-colors hover:border-brand/50 hover:text-brand disabled:cursor-progress disabled:opacity-60"
              >
                {extracting ? 'extracting…' : 'extract tasks'}
              </button>
            </div>
            {extractNote !== null && (
              <div
                role="status"
                className={`mt-2 flex items-start gap-2 rounded border px-2 py-1 font-mono text-[11px] ${
                  extractNote.tone === 'ok'
                    ? 'border-green/40 bg-green/10 text-green'
                    : 'border-red/40 bg-red/10 text-red'
                }`}
              >
                <span className="min-w-0 flex-1 break-words">{extractNote.text}</span>
                <button
                  type="button"
                  onClick={() => setExtractNote(null)}
                  aria-label="dismiss"
                  className="shrink-0 opacity-70 transition-opacity hover:opacity-100"
                >
                  ×
                </button>
              </div>
            )}
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
        <div className="relative flex min-h-0 min-w-0 flex-1 flex-col">
          <div
            role="tabpanel"
            ref={panelRef}
            onScroll={tab === 'chat' ? onPanelScroll : undefined}
            className="min-h-0 flex-1 overflow-y-auto px-4 pb-6 desk:px-10 wide:px-0 [-webkit-overflow-scrolling:touch]"
          >
            {tab === 'chat' && (
              <Chat detail={detail} pending={pending} onRetry={onRetry} />
            )}
            {tab === 'timeline' && <Timeline detail={detail} />}
            {tab === 'diffs' && <Diffs changes={detail.fileChanges} />}
          </div>
          {tab === 'chat' && !atBottom && (
            <button
              type="button"
              onClick={scrollToLatest}
              aria-label="Jump to latest"
              className="absolute inset-x-0 bottom-3 mx-auto flex w-max items-center gap-1.5 rounded-full border border-line-strong bg-surface2/95 px-3.5 py-1.5 font-mono text-[11px] text-ink shadow-lg backdrop-blur transition-colors hover:border-brand hover:text-brand focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand"
            >
              <span aria-hidden="true">↓</span>
              Latest
            </button>
          )}
          {tab === 'chat' && (
            <CommandInput
              sessionId={detail.id}
              procState={detail.procState}
              resumeInFlight={detail.resumeInFlight ?? false}
              onSent={onSent}
              onSendFailed={onSendFailed}
            />
          )}
        </div>

        <div className="hidden min-h-0 wide:block wide:overflow-y-auto wide:py-6">
          <DetailRail
            sessionId={detail.id}
            handoff={detail.handoff}
            turns={detail.turns}
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

function BackLink({ to }: { to: string }): JSX.Element {
  return (
    <Link
      to={to}
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
