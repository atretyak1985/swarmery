// The open session, the detail column of the Sessions place (handoff §5). Top to
// bottom: eyebrow, title, the state in one sentence, the numbers strip, the gate
// card (only while it needs you), the tabs Steps · Chat · Diff with the actions
// for this state on the same line, and the tab.
//
// The sentence is the only place the state is said. What the old header scattered
// went to a fixed place (plan D-4, the report lists every move): tokens, cost,
// context, elapsed, errors, recovered and cold restarts to the numbers strip;
// model, branch, account and the task to the eyebrow; outcome, extract tasks, Kill,
// the labels, the link mod's usage and remote control to the actions and `⋯`; the
// Swarm tab and the whole right rail to folds under the Steps ledger.
//
// The whole column scrolls; the composer sticks to its foot on the Chat tab. Tabs
// deep-link through ?tab=chat|diff; the old ?tab=timeline|diffs|swarm keep working.
// Live: session_updated merges header state; event_appended is attributed via its
// sessionId and appended (or, for refined durations, replaced in place).

import { plural } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type MutableRefObject,
  type ReactNode,
} from 'react';
import { Link, useLocation, useSearchParams } from 'react-router-dom';
import type { NeedsYouItem, PendingSession, SessionDetail, SessionOutcome, WSMessage } from '../../api/types';
import {
  MOCK,
  extractSessionTasks,
  fetchSession,
  isPendingSession,
  patchSessionOutcome,
  renameSession,
  sendSessionMessage,
} from '../../api';
import { fmtCost, fmtTokens, projectLabel } from '../../lib/format';
import { useProjectColor } from '../../lib/projectColors';
import { safeHref } from '../../lib/safeHref';
import { accountLabel } from '../../lib/sessionAccount';
import { modelShortName, sessionModelFallback } from '../../lib/sessionModelChip';
import { compactSpan, rowState, stateSentence, type RowState } from '../../lib/sessionsView';
import { useNowMs } from '../../lib/sessionState';
import { ConflictBadge } from '../../lib/swarm';
import { useLiveUpdates } from '../../lib/ws';
import { ExplainPair } from '../../components/Explain';
import { KillButton, killSlotKind } from '../../components/KillButton';
import { OUTCOME_GLYPH, OutcomePicker } from '../../components/OutcomePicker';
import { NumbersStrip, type NumberItem } from '../../components/place/NumbersStrip';
import { ProcBadge } from '../../components/ProcBadge';
import { SessionLabelChips } from '../../components/SessionLabelChips';
import { SessionControls } from '../../components/SessionControls';
import { SessionLinkChips } from '../../components/SessionLinkChips';
import { StopButton } from '../../components/StopButton';
import { TaskChip } from '../../components/TaskChip';
import { ErrorBox, Loading } from '../../components/ui';
import { Timeline } from './Timeline';
import { Diffs } from './Diffs';
import { CacheColdBadge } from '../sessions/CacheColdBadge';
import { ModelSwitchBadge } from '../sessions/ModelSwitchBadge';
import { SwarmPanel } from '../sessions/SwarmPanel';
import { scrollToDiffGroup } from './diffJump';
import { Chat, type PendingSend } from './Chat';
import { CommandInput } from './CommandInput';
import { redirectChannel } from './redirectChannel';
import { SummaryChips } from './SummaryChips';
import { DetailRail } from './DetailRail';
import { PendingRunNotice } from './PendingRunNotice';
import { gateTier, SessionGate } from './SessionGate';

/** What `e` and `x` do on the open session, read by the list's keys. */
export interface PaneActions {
  primary?: () => void;
  secondary?: () => void;
}

type Tab = 'steps' | 'chat' | 'diff';

/** ?tab= deep links, the v3 names included; absent is the Steps default. */
export function parseTab(value: string | null): Tab {
  if (value === 'chat') return 'chat';
  if (value === 'diff' || value === 'diffs') return 'diff';
  return 'steps';
}

const TERM_FOCUS_SCHEMES: readonly string[] = ['warp:'];

/** Context occupancy (tokens), matching advisor R9's danger line: a fat context is
 * the fat-session cost driver. */
const CONTEXT_WARN = 150_000;
const CONTEXT_DANGER = 300_000;

const TITLE_CLASS = 'text-[22px] leading-[1.25] font-semibold tracking-[-0.015em]';

const BUTTON =
  'rounded-[7px] border border-line-strong bg-transparent px-3 py-1.5 font-mono text-[11.5px] leading-[normal] whitespace-nowrap text-ink-2 transition-colors hover:border-ink-dim';
const BUTTON_RED =
  'rounded-[7px] border border-red bg-transparent px-3 py-1.5 font-mono text-[11.5px] leading-[normal] font-medium whitespace-nowrap text-red transition-colors hover:bg-red-bg';
const BUTTON_ACCENT =
  'rounded-[7px] border border-brand bg-transparent px-3 py-1.5 font-mono text-[11.5px] leading-[normal] font-medium whitespace-nowrap text-brand transition-colors hover:bg-brand-bg';

const STATE_TONE: Record<RowState, { text: string; dot: string }> = {
  needs: { text: 'text-brand', dot: 'animate-blink-dot bg-amber' },
  working: { text: 'text-green', dot: 'animate-blink-dot bg-green' },
  done: { text: 'text-ink-3', dot: 'bg-ink-3' },
  failed: { text: 'text-red', dot: 'bg-red' },
  stopped: { text: 'text-ink-faint', dot: 'bg-ink-faint' },
  pending: { text: 'text-brand', dot: 'bg-amber' },
};

/** Inline-editable session title. Click it to rename; Enter/blur saves, Escape
 * cancels. A blank value reverts to the ingested title. */
function TitleEditor({
  title,
  onRename,
}: {
  title: string | null;
  onRename: (raw: string) => void;
}): JSX.Element {
  const { t } = useLingui();
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
        placeholder={t`session title…`}
        aria-label={t`session title`}
        maxLength={120}
        className={`mt-2 w-full rounded-[8px] border border-line-strong bg-field px-2 py-1 text-ink outline-none focus:border-ink-dim ${TITLE_CLASS}`}
      />
    );
  }
  return (
    <div className="group mt-2 flex items-start gap-2">
      <h2
        role="button"
        tabIndex={0}
        onClick={begin}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault();
            begin();
          }
        }}
        data-tip={t`click to rename`}
        className={`m-0 cursor-text ${TITLE_CLASS} ${title === null ? 'text-ink-faint italic' : 'text-ink'}`}
      >
        {title ?? t`(untitled session)`}
      </h2>
      <button
        type="button"
        onClick={begin}
        aria-label={t`rename session`}
        data-tip={t`rename session`}
        className="mt-1 shrink-0 rounded-md border border-line px-1.5 py-0.5 font-mono text-[12px] leading-none text-ink-dim opacity-0 transition-opacity group-hover:opacity-100 focus-visible:opacity-100"
      >
        ✎
      </button>
    </div>
  );
}

/** A button that opens a panel of its own; closes on Escape and outside clicks. */
function Menu({
  label,
  ariaLabel,
  children,
}: {
  label: string;
  ariaLabel: string;
  children: ReactNode;
}): JSX.Element {
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLSpanElement>(null);
  useEffect(() => {
    if (!open) return undefined;
    const onDown = (e: MouseEvent): void => {
      if (e.target instanceof Node && root.current?.contains(e.target) !== true) setOpen(false);
    };
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') setOpen(false);
    };
    document.addEventListener('mousedown', onDown);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onDown);
      document.removeEventListener('keydown', onKey);
    };
  }, [open]);
  return (
    <span ref={root} className="relative inline-flex">
      <button type="button" aria-expanded={open} aria-label={ariaLabel} onClick={() => setOpen((v) => !v)} className={BUTTON}>
        {label}
      </button>
      {open && (
        <div
          role="group"
          aria-label={ariaLabel}
          className="absolute top-[calc(100%+6px)] right-0 z-30 flex w-[320px] flex-col gap-2.5 rounded-[9px] border border-line-strong bg-surface p-3 shadow-[0_12px_30px_rgba(0,0,0,0.35)]"
        >
          {children}
        </div>
      )}
    </span>
  );
}

function MenuLine({ label, children }: { label: string; children: ReactNode }): JSX.Element {
  return (
    <div className="flex flex-col gap-1">
      <span className="font-mono text-[10px] tracking-[0.08em] text-ink-faint uppercase">{label}</span>
      <div className="flex flex-wrap items-center gap-1.5 empty:hidden">{children}</div>
    </div>
  );
}

/** A section under the ledger, closed until asked for: it fetches when opened. */
function Fold({
  label,
  open,
  onToggle,
  children,
}: {
  label: string;
  open: boolean;
  onToggle: () => void;
  children: ReactNode;
}): JSX.Element {
  return (
    <div className="border-b border-line">
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        className="flex w-full items-center justify-between py-2 font-mono text-[10px] tracking-[0.12em] text-ink-faint uppercase transition-colors hover:text-ink"
      >
        <span>{label}</span>
        <span aria-hidden="true">{open ? '▾' : '▸'}</span>
      </button>
      {open && <div className="pb-3">{children}</div>}
    </div>
  );
}

function TabButton({
  active,
  onClick,
  count,
  children,
}: {
  active: boolean;
  onClick: () => void;
  count: number;
  children: string;
}): JSX.Element {
  return (
    <button
      type="button"
      role="tab"
      aria-selected={active}
      onClick={onClick}
      className={`-mb-px inline-flex items-center gap-1.5 border-b-2 px-3 py-2 text-[12.5px] leading-[normal] font-medium transition-colors focus-visible:rounded-t-sm focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand ${
        active ? 'border-brand text-ink' : 'border-transparent text-ink-3 hover:text-ink'
      }`}
    >
      {children}
      {count > 0 && <span className="font-mono text-[10px] text-ink-faint">{count}</span>}
    </button>
  );
}

function BackLink({ to }: { to: string }): JSX.Element {
  return (
    <Link to={to} className="mb-2 block pt-0.5 font-mono text-[11px] text-ink-faint hover:text-ink desk:hidden">
      <Trans>← sessions</Trans>
    </Link>
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
    // i18n-ignore — demo fixture (VITE_MOCK): a sample operator message, not UI copy
    { key: 'mock-pending', text: 'Also add a --dry-run flag to the backfill command.', state: 'pending', sentAt: now },
    // i18n-ignore — demo fixture (VITE_MOCK): a sample operator message, not UI copy
    { key: 'mock-failed', text: 'Re-run the migration against the staging snapshot.', state: 'failed', sentAt: now },
  ];
}

export function SessionPane({
  id,
  listHref,
  gate,
  onGateChanged,
  actions,
  now,
}: {
  /** Row id or session uuid: GET /api/sessions/{id} takes either. */
  id: string;
  /** Where the narrow back link returns to. */
  listHref: string;
  /** The queue's blocker for this session. */
  gate: NeedsYouItem | undefined;
  onGateChanged: () => void;
  actions: MutableRefObject<PaneActions>;
  /** The live last action of this session, from the list's WS feed. */
  now: string | null;
}): JSX.Element {
  const { t } = useLingui();
  const nowMs = useNowMs(15_000);
  const { hash } = useLocation();
  const colorFor = useProjectColor();
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
  const setTab = useCallback(
    (next: Tab): void => {
      setSearchParams(next === 'steps' ? {} : { tab: next }, { replace: true });
    },
    [setSearchParams],
  );
  // The folds under the ledger; the rail's links reach the handoff brief by hash.
  const [swarmOpen, setSwarmOpen] = useState(searchParams.get('tab') === 'swarm');
  const [usageOpen, setUsageOpen] = useState(hash === '#handoff');

  const load = useCallback((): void => {
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
    const timer = setTimeout(load, PENDING_RUN_POLL_MS);
    return () => clearTimeout(timer);
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
      const nowAt = Date.now();
      let changed = false;
      const next: PendingSend[] = [];
      for (const p of prev) {
        const matched = nowAt - p.sentAt < PENDING_STALE_MS && userTexts.has(p.text.trim());
        if (matched) {
          changed = true; // reconciled away
          continue;
        }
        if (p.state === 'pending' && nowAt - p.sentAt >= PENDING_STALE_MS) {
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
    const timer = setTimeout(() => {
      const nowAt = Date.now();
      setPending((prev) =>
        prev.map((p) =>
          p.state === 'pending' && nowAt - p.sentAt >= PENDING_STALE_MS
            ? { ...p, state: 'failed' }
            : p,
        ),
      );
    }, PENDING_STALE_MS + 500);
    return () => clearTimeout(timer);
  }, [pending]);

  // The newest chat lives at the bottom: on switching to Chat (and on first data
  // load there) jump to the end, so a live session opens on "now". A file link in
  // the usage fold scrolls to that file's row on the Diff tab instead.
  const panelRef = useRef<HTMLDivElement | null>(null);
  const diffTargetRef = useRef<string | null>(null);
  const [openDiff, setOpenDiff] = useState<string | null>(null);
  const loaded = detail !== null;
  useEffect(() => {
    const panel = panelRef.current;
    if (panel === null || !loaded) return;
    const target = diffTargetRef.current;
    if (tab === 'diff' && target !== null) {
      diffTargetRef.current = null;
      if (scrollToDiffGroup(panel, target)) return;
    }
    if (tab === 'chat') panel.scrollTop = panel.scrollHeight;
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
    // Already on the Diff tab: setTab is a no-op and the effect above never
    // re-runs, so jump straight away. Any other tab: park the target for the
    // effect that fires once the Diff panel is rendered.
    setOpenDiff(path ?? null);
    const panel = panelRef.current;
    if (tab === 'diff' && path !== undefined && panel !== null) {
      scrollToDiffGroup(panel, path);
      return;
    }
    diffTargetRef.current = path ?? null;
    setTab('diff');
  };

  // `redirect`, `resume with a reply` and `write my own` all land in the composer,
  // which is mounted only on the Chat tab: the effect finishes what a tab switch
  // starts.
  const composerRef = useRef<HTMLTextAreaElement>(null);
  const wantComposer = useRef(false);
  const openComposer = useCallback((): void => {
    wantComposer.current = true;
    if (tab === 'chat') composerRef.current?.focus();
    else setTab('chat');
  }, [tab, setTab]);
  useEffect(() => {
    if (tab !== 'chat' || !wantComposer.current || composerRef.current === null) return;
    wantComposer.current = false;
    composerRef.current.focus();
  }, [tab, loaded]);
  // With the composer off, `redirect` goes through the link mod's prompt instead.
  const [linkPrompt, setLinkPrompt] = useState(false);

  // `e` with no gate is the composer; SessionGate takes both keys while it is up.
  useEffect(() => {
    if (gate === undefined) actions.current = { primary: openComposer };
  }, [gate, openComposer, actions]);

  const onMessage = useCallback(
    (msg: WSMessage): void => {
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
    },
    [scheduleLoad, load, id],
  );
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

  // "Extract tasks": one paid model pass over this session, turning what it left
  // behind into suggested Triage cards. Manual by design — nothing here is
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
              ? t`No new tasks — this session had nothing left to suggest.`
              : plural(inserted, {
                  one: '# task suggested on the board.',
                  few: '# tasks suggested on the board.',
                  many: '# tasks suggested on the board.',
                  other: '# tasks suggested on the board.',
                }),
        });
      })
      .catch((e: unknown) => {
        setExtractNote({ tone: 'err', text: e instanceof Error ? e.message : String(e) });
      })
      .finally(() => setExtracting(false));
  };

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
      <div className="px-4 pt-[22px] desk:px-7">
        <BackLink to={listHref} />
        <ErrorBox message={error} onRetry={load} />
      </div>
    );
  }
  if (detail === null && pendingRun !== null) {
    return (
      <div className="px-4 pt-[22px] desk:px-7">
        <BackLink to={listHref} />
        <PendingRunNotice run={pendingRun} onRetry={load} />
      </div>
    );
  }
  if (detail === null || facts === null) {
    return (
      <div className="px-4 pt-[22px] desk:px-7">
        <BackLink to={listHref} />
        <Loading label={t`session…`} />
      </div>
    );
  }

  const state = rowState(detail, nowMs, gate);
  const lastEvent = detail.events[detail.events.length - 1];
  const sentence = stateSentence(detail, state, nowMs, lastEvent?.ts ?? null, now, gate);
  const tone = STATE_TONE[state];
  const plan = detail.planGroup != null;
  const stepCount = detail.events.length;
  const chatCount = detail.turns.filter((turn) => turn.text !== null && turn.text !== '').length;
  const diffCount = new Set(detail.fileChanges.map((c) => c.filePath)).size;
  const model = detail.modelLast ?? detail.model;
  const moved = sessionModelFallback(detail);
  const account = accountLabel(detail);
  const contextPct = detail.link?.contextPct ?? null;
  const elapsedMs = new Date(detail.endedAt ?? nowMs).getTime() - new Date(detail.startedAt).getTime();

  const numbers: NumberItem[] = [
    { label: t`tokens`, value: fmtTokens(facts.tokens) },
    { label: t`cost`, value: fmtCost(facts.cost), tone: 'brand' },
  ];
  if (contextPct !== null) {
    numbers.push({
      label: t`context`,
      value: `${String(Math.round(contextPct))} %`,
      tone: contextPct >= 55 ? 'red' : 'default',
      barPercent: contextPct,
    });
  } else if (detail.contextTokens != null) {
    // No link mod: the window size is unknown, so no percentage and no bar. The
    // fat-session thresholds still warn: every turn re-reads this context.
    const heavy = detail.contextTokens >= CONTEXT_DANGER ? 'red' : detail.contextTokens >= CONTEXT_WARN ? 'amber' : 'default';
    numbers.push({ label: t`context`, value: fmtTokens(detail.contextTokens), tone: heavy });
  }
  numbers.push({ label: t`elapsed`, value: compactSpan(elapsedMs) });
  if (facts.errors > 0) numbers.push({ label: t`errors`, value: String(facts.errors), tone: 'red' });
  if (detail.recovered > 0) numbers.push({ label: t`recovered`, value: String(detail.recovered), tone: 'green' });
  if ((detail.coldRestarts ?? 0) > 0) numbers.push({ label: t`cold restarts`, value: String(detail.coldRestarts) });

  const live = state === 'working' || state === 'pending';
  const redirect = redirectChannel(detail);
  const killable = killSlotKind(detail);
  const focusHref = safeHref(gate?.termFocusUrl ?? null, TERM_FOCUS_SCHEMES);
  const outcome = detail.outcome ?? null;
  const outcomeLabel = outcome === null ? t`outcome ▾` : t`outcome ${OUTCOME_GLYPH[outcome].glyph} ▾`;
  const extractButton = (
    <button
      type="button"
      onClick={runExtract}
      disabled={extracting}
      data-tip={t`run a model pass over this session and suggest board tasks from what it left behind`}
      className={`${BUTTON} disabled:cursor-progress disabled:opacity-60`}
    >
      {extracting ? t`extracting…` : t`extract tasks`}
    </button>
  );
  const outcomePicker = (
    <ExplainPair id="session-outcome">
      <OutcomePicker value={outcome} onChange={setOutcome} />
    </ExplainPair>
  );
  const placeholder =
    state === 'needs'
      ? t`reply · this resumes the session`
      : live
        ? t`queue a message · delivered after this step`
        : t`reply to resume from the saved conversation`;

  return (
    <div
      ref={panelRef}
      onScroll={tab === 'chat' ? onPanelScroll : undefined}
      className="h-full min-h-0 overflow-y-auto [-webkit-overflow-scrolling:touch]"
    >
      <div data-testid="session-detail" className="max-w-[1040px] px-4 pt-[22px] pb-10 desk:px-7">
        <BackLink to={listHref} />

        <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 font-mono text-[10.5px] leading-[normal] text-ink-faint">
          <span className="inline-flex items-center gap-[5px]">
            <span
              aria-hidden="true"
              className="h-1.5 w-1.5 rounded-full"
              style={{ backgroundColor: colorFor(detail.projectSlug) }}
            />
            {projectLabel(detail.projectName, detail.projectSlug)}
          </span>
          <span aria-hidden="true">·</span>
          <span>{plan ? t`plan run` : t`session`}</span>
          {model !== null && (
            <>
              <span aria-hidden="true">·</span>
              {/* Both ends of a move: amber and "fell back to" are kept for a move
                  down, the thing that explains a weaker answer. */}
              {moved === null ? (
                <span>{model}</span>
              ) : (
                <span className={moved.fellBack ? 'text-amber' : undefined}>
                  {modelShortName(moved.from)} {moved.fellBack ? t`→ fell back to` : t`→ changed to`}{' '}
                  {modelShortName(moved.to)}
                </span>
              )}
            </>
          )}
          {detail.gitBranch !== null && (
            <>
              <span aria-hidden="true">·</span>
              <span>{detail.gitBranch}</span>
            </>
          )}
          {/* Only a non-default subscription: on a one-account machine it would
              read "default" on every session. */}
          {account !== null && (
            <>
              <span aria-hidden="true">·</span>
              <span>{account}</span>
            </>
          )}
          {detail.taskExternalId != null && (
            <TaskChip
              externalId={detail.taskExternalId}
              linkSource={detail.taskLinkSource}
              confidence={detail.taskConfidence}
            />
          )}
        </div>

        <TitleEditor title={detail.title} onRename={rename} />

        <div className="mt-2 flex items-start gap-2 text-[13.5px] leading-[1.5] text-ink-2">
          <span aria-hidden="true" className={`mt-[5px] inline-block h-[9px] w-[9px] shrink-0 rounded-full ${tone.dot}`} />
          <span>
            <b className={`font-semibold ${tone.text}`}>{sentence.lead}</b> {sentence.rest}
          </span>
        </div>

        {/* Alarms that act: renders nothing on most sessions. */}
        <div className="mt-2 flex flex-wrap items-center gap-2 empty:hidden">
          <ConflictBadge sessionId={detail.id} />
          <CacheColdBadge session={detail} />
          <ModelSwitchBadge session={detail} />
        </div>

        {extractNote !== null && (
          <div
            role="status"
            className={`mt-2 flex items-start gap-2 rounded-[6px] border px-2 py-1 font-mono text-[11px] ${
              extractNote.tone === 'ok' ? 'border-green text-green' : 'border-red text-red'
            }`}
          >
            <span className="min-w-0 flex-1 break-words">{extractNote.text}</span>
            <button
              type="button"
              onClick={() => setExtractNote(null)}
              aria-label={t`dismiss`}
              className="shrink-0 opacity-70 transition-opacity hover:opacity-100"
            >
              ×
            </button>
          </div>
        )}

        <div className="mt-4">
          <NumbersStrip items={numbers} />
        </div>

        {gate !== undefined && (
          <SessionGate
            key={`${gate.kind}:${String(gate.requestId)}`}
            item={gate}
            session={detail}
            onChanged={onGateChanged}
            onOpenComposer={openComposer}
            onSent={onSent}
            actions={actions}
          />
        )}

        <div role="tablist" className="mt-[22px] flex flex-wrap items-center gap-0.5 border-b border-line">
          <TabButton active={tab === 'steps'} onClick={() => setTab('steps')} count={stepCount}>
            {t`Steps`}
          </TabButton>
          <TabButton active={tab === 'chat'} onClick={() => setTab('chat')} count={chatCount}>
            {t`Chat`}
          </TabButton>
          <TabButton active={tab === 'diff'} onClick={() => setTab('diff')} count={diffCount}>
            {t`Diff`}
          </TabButton>
          <span className="min-w-2 flex-1" />
          <div className="inline-flex gap-1.5 py-1">
            {/* No `pause` (plan D-13): stopSession ends the process and records it
                as finished, so the word would lie. */}
            {state === 'working' && redirect !== null && (
              <button
                type="button"
                onClick={
                  redirect === 'composer'
                    ? openComposer
                    : () => {
                        setLinkPrompt(true);
                        setTab('chat');
                      }
                }
                className={BUTTON}
              >
                {t`redirect`}
              </button>
            )}
            {live &&
              (killable === 'killable' ? (
                <KillButton session={detail} label={t`stop`} className={BUTTON_RED} />
              ) : (
                <StopButton session={detail} label={t`stop`} className={BUTTON_RED} />
              ))}
            {state === 'done' && (
              <>
                <Menu label={outcomeLabel} ariaLabel={t`session outcome`}>
                  {outcomePicker}
                </Menu>
                {extractButton}
              </>
            )}
            {(state === 'failed' || state === 'stopped') && (
              <button type="button" onClick={openComposer} className={BUTTON_ACCENT}>
                {t`resume with a reply`}
              </button>
            )}
            {state === 'needs' && focusHref !== null && (
              <a href={focusHref} className={BUTTON}>
                {t`open in terminal ↗`}
              </a>
            )}
            <Menu label="⋯" ariaLabel={t`more for this session`}>
              {state !== 'done' && <MenuLine label={t`outcome`}>{outcomePicker}</MenuLine>}
              {state !== 'done' && <MenuLine label={t`board`}>{extractButton}</MenuLine>}
              {/* Stop vs Kill is explained once, here, next to the process. */}
              <MenuLine label={t`process`}>
                <ExplainPair id="kill-vs-stop">
                  <ProcBadge session={detail} />
                </ExplainPair>
                {!live && <KillButton session={detail} />}
              </MenuLine>
              <MenuLine label={t`live usage`}>
                <SessionLinkChips session={detail} />
              </MenuLine>
              <MenuLine label={t`labels`}>
                <SessionLabelChips uuid={detail.sessionUuid} />
              </MenuLine>
              {/* Link-mod remote control: always shown, disabled with the reason
                  when no mod polls for this session. */}
              <MenuLine label={t`remote control`}>
                <SessionControls session={detail} />
              </MenuLine>
            </Menu>
          </div>
        </div>

        <div role="tabpanel">
          {tab === 'steps' && (
            <>
              <Timeline detail={detail} live={state === 'working'} gateTier={gate === undefined ? undefined : gateTier(gate, null)} />
              <div className="mt-3 wide:hidden">
                <SummaryChips events={detail.events} />
              </div>
              <div className="mt-3 border-t border-line">
                <Fold label={t`swarm`} open={swarmOpen} onToggle={() => setSwarmOpen((on) => !on)}>
                  <SwarmPanel projectId={detail.projectId} projectSlug={detail.projectSlug} sender={detail} />
                </Fold>
                <Fold
                  label={t`usage · agents · skills · files`}
                  open={usageOpen}
                  onToggle={() => setUsageOpen((on) => !on)}
                >
                  <DetailRail
                    sessionId={detail.id}
                    handoff={detail.handoff}
                    turns={detail.turns}
                    events={detail.events}
                    fileChanges={detail.fileChanges}
                    onShowDiffs={showDiffs}
                  />
                </Fold>
              </div>
            </>
          )}
          {tab === 'chat' && <Chat detail={detail} pending={pending} onRetry={onRetry} gated={gate !== undefined} />}
          {tab === 'diff' && <Diffs changes={detail.fileChanges} openPath={openDiff} />}
        </div>
        {tab === 'chat' && !atBottom && (
          <button
            type="button"
            onClick={scrollToLatest}
            aria-label={t`Jump to latest`}
            className="sticky bottom-[72px] z-10 mx-auto flex w-max items-center gap-1.5 rounded-[8px] border border-line-strong bg-surface2 px-3 py-1.5 font-mono text-[11px] text-ink transition-colors hover:border-brand hover:text-brand focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand"
          >
            <span aria-hidden="true">↓</span>
            <Trans>Latest</Trans>
          </button>
        )}
        {tab === 'chat' && linkPrompt && redirect === 'link' && <SessionControls session={detail} promptOpen />}
        {tab === 'chat' && (
          <CommandInput
            sessionId={detail.id}
            procState={detail.procState}
            resumeInFlight={detail.resumeInFlight ?? false}
            onSent={onSent}
            onSendFailed={onSendFailed}
            placeholder={placeholder}
            inputRef={composerRef}
          />
        )}
      </div>
    </div>
  );
}
