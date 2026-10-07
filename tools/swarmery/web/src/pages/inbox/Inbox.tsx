// The Inbox (Canvas v3 phase 3, artboards 1b + 2b): one place for every
// decision that waits on the operator, fleet (/inbox) or one project
// (/p/:slug/inbox). Header per 1b, tabs per kind (?tab=), then the phase-2
// SplitPane — list left, detail right, j/k move, e primary, x deny/dismiss,
// s skip. The old Approvals page stays reachable at approvals/manage for
// rules and history.

import { useEffect, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { SplitPane, type SplitPaneKey } from '../../components/SplitPane';
import { Tabs, useTabParam } from '../../components/Tabs';
import { TriageConflictError, acceptTriageVerdict } from '../../api/triage';
import { HandledList } from './HandledList';
import { InboxDetail, denyAction, primaryAction } from './InboxDetail';
import { TriageBanner } from './TriageBanner';
import {
  INBOX_TABS,
  agentOffer,
  KIND_META,
  ageLabel,
  expiresInLabel,
  filterTab,
  suggestionActionLabel,
  suggestionBreakdown,
  tabCounts,
  waitingLine,
  type InboxItem,
  type InboxTabId,
} from './inboxModel';
import { FLEET_WIDE_KINDS, useInboxItems } from './useInboxItems';

const TAB_IDS: readonly InboxTabId[] = INBOX_TABS.map((t) => t.id);

/** Wall clock for the expiry countdowns. */
function useNow(periodMs: number): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), periodMs);
    return () => clearInterval(id);
  }, [periodMs]);
  return now;
}

function GroupLabel({ urgent }: { urgent: boolean }): JSX.Element {
  return (
    <div
      className={`px-3.5 pt-2 pb-[3px] font-mono text-[9.5px] tracking-[0.12em] uppercase ${urgent ? 'text-amber' : 'text-ink-faint'}`}
    >
      {urgent ? 'expires soon' : 'when you have a minute'}
    </div>
  );
}

export function Inbox(): JSX.Element {
  const { slug } = useParams<{ slug?: string }>();
  const scope = slug ?? null;
  const { items, loading, errors, reload, triage } = useInboxItems(scope, true);
  const [tab, setTab] = useTabParam<InboxTabId>('tab', TAB_IDS, 'all');
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);
  const [dismissedRunId, setDismissedRunId] = useState<number | null>(null);
  const [acceptResult, setAcceptResult] = useState<string | null>(null);
  const [accepting, setAccepting] = useState(false);
  const [dismissedError, setDismissedError] = useState<string | null>(null);
  const now = useNow(1000);

  const visible = filterTab(items, tab);
  const selected = visible.find((i) => i.key === selectedId) ?? visible[0];
  const counts = tabCounts(items, triage.handled.length);
  const firstUrgent = visible.find((i) => i.urgent)?.key;
  const firstCalm = visible.find((i) => !i.urgent)?.key;

  const neighbour = (item: InboxItem): string | null => {
    const idx = visible.findIndex((i) => i.key === item.key);
    return visible[idx + 1]?.key ?? visible[idx - 1]?.key ?? null;
  };

  const run = (item: InboxItem, action: () => Promise<unknown>): void => {
    // One write at a time: a single-item action and "accept all" never overlap.
    if (busy || accepting) return;
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

  const breakdown = suggestionBreakdown(triage.open);

  // One after another, never stopping at a failure. Fix-task suggestions are not
  // in `breakdown.acceptable`: the operator opens and reads those one by one.
  const acceptAll = (): void => {
    if (accepting || busy) return;
    const todo = breakdown.acceptable;
    setAccepting(true);
    setAcceptResult(null);
    setActionError(null);
    void (async () => {
      let accepted = 0;
      let failed = 0;
      let stale = 0;
      for (const [i, v] of todo.entries()) {
        setAcceptResult(`accepting ${String(i + 1)} of ${String(todo.length)}…`);
        try {
          await acceptTriageVerdict(v.id);
          accepted += 1;
        } catch (e) {
          if (e instanceof TriageConflictError) stale += 1;
          else failed += 1;
        }
      }
      const parts = [`accepted ${String(accepted)}`];
      if (failed > 0) parts.push(`${String(failed)} failed`);
      if (stale > 0) parts.push(`${String(stale)} changed meanwhile`);
      setAcceptResult(parts.join(' · '));
      reload();
      triage.reload();
      setAccepting(false);
    })();
  };

  // The error line has one identity (this start attempt, or that failed run), so
  // dismissing it hides exactly that error and a later one shows again.
  const lastRun = triage.lastRun;
  const runFailed = lastRun?.status === 'failed';
  const runError = triage.startError ?? (runFailed ? lastRun.error || 'The triage run failed.' : null);
  const errorKey =
    triage.startError !== null ? `start:${triage.startError}` : runFailed ? `run:${String(lastRun.id)}` : null;
  const bannerError = errorKey !== null && dismissedError === errorKey ? null : runError;

  const primaryLabel =
    selected?.suggestion !== undefined && !selected.suggestion.sample
      ? suggestionActionLabel(selected.suggestion.value)
      : 'approve';

  const keymap: SplitPaneKey<InboxItem>[] = [
    {
      key: 'e',
      label: primaryLabel,
      run: (item) => {
        const a = primaryAction(item);
        if (a !== null) run(item, a);
      },
    },
    {
      key: 'x',
      label: 'deny',
      run: (item) => {
        const a = denyAction(item);
        if (a !== null) run(item, a);
      },
    },
    { key: 's', label: 'skip', run: (item) => setSelectedId(neighbour(item) ?? item.key) },
  ];

  return (
    <div className="flex h-full min-h-0 flex-col">
      <header className="px-9 pt-[30px] pb-0">
        <div className="flex items-baseline gap-2.5">
          <h1 className="m-0 font-display text-[30px] leading-[1.15] font-medium tracking-[-0.01em] text-ink">
            Inbox
          </h1>
          <span className="font-mono text-[11px] text-ink-faint">
            {loading ? 'loading…' : waitingLine(items, now)}
          </span>
          {tab === 'approvals' && (
            <Link to="../approvals/manage" relative="path" className="ml-auto font-mono text-[10.5px] text-ink-faint hover:text-ink-dim">
              rules &amp; history →
            </Link>
          )}
        </div>
        <p className="mt-1.5 max-w-[66ch] text-[13px] text-ink-dim">
          Decisions that wait on you. An agent can label the classifier's guesses and close what is only
          informational; approvals, agent changes and alerts always wait for you.
        </p>
        <div className="mt-4">
          <Tabs
            ariaLabel="inbox kinds"
            tabs={INBOX_TABS.map((t) => ({ id: t.id, label: t.label, count: counts[t.id] }))}
            value={tab}
            onChange={(id) => {
              setTab(id);
              setSelectedId(null);
            }}
          />
        </div>
      </header>

      <TriageBanner
        offer={agentOffer(items, now)}
        running={triage.run === null ? null : { done: triage.run.done, total: triage.run.total }}
        summary={
          lastRun === null || runFailed
            ? null
            : { applied: lastRun.applied, suggested: lastRun.suggested, failed: lastRun.failed }
        }
        error={bannerError}
        suggestions={{
          total: breakdown.acceptable.length + breakdown.fixTasks,
          acceptable: breakdown.acceptable.length,
          fixTasks: breakdown.fixTasks,
          parts: breakdown.parts,
        }}
        acceptResult={acceptResult}
        dismissed={lastRun !== null && dismissedRunId === lastRun.id}
        busy={accepting}
        onStart={() => {
          setAcceptResult(null);
          setDismissedError(null);
          void triage.start();
        }}
        onAcceptAll={acceptAll}
        onOpenHandled={() => {
          setTab('handled');
          setSelectedId(null);
        }}
        onDismiss={() => setDismissedRunId(lastRun?.id ?? null)}
        onDismissError={() => setDismissedError(errorKey)}
      />


      {errors.length > 0 && (
        <div className="flex flex-wrap gap-3 border-b border-line px-9 py-2 font-mono text-[11px] text-red">
          {errors.map((k) => (
            <span key={k} role="alert">
              couldn't load {INBOX_TABS.find((t) => t.kind === k)?.label ?? k}
            </span>
          ))}
        </div>
      )}

      <div className="min-h-0 flex-1 px-9">
        {tab === 'handled' ? (
          <HandledList
            verdicts={triage.handled}
            onChanged={() => {
              reload();
              triage.reload();
            }}
            now={now}
          />
        ) : !loading && visible.length === 0 ? (
          <div className="py-10 text-[13px] text-ink-dim">Nothing is waiting on you.</div>
        ) : (
          <SplitPane
            ariaLabel="waiting decisions"
            items={visible}
            getId={(i) => i.key}
            selectedId={selected?.key ?? null}
            onSelect={(id) => {
              setSelectedId(id);
              setActionError(null);
            }}
            keymap={keymap}
            renderRow={(item, isSelected) => (
              <>
                {item.key === firstUrgent && <GroupLabel urgent />}
                {item.key === firstCalm && firstUrgent !== undefined && <GroupLabel urgent={false} />}
                <div className="flex gap-2.5 px-3.5 py-2.5">
                  <span className={`mt-[5px] h-[7px] w-[7px] shrink-0 rounded-full ${KIND_META[item.kind].dot}`} />
                  <div className="min-w-0">
                    <div className={`truncate text-[12.5px] ${isSelected ? 'font-medium text-ink' : 'text-ink-2'}`}>
                      {item.title}
                    </div>
                    <div className="mt-0.5 truncate font-mono text-[10.5px] text-ink-faint">
                      {item.context} · {ageLabel(item.ageIso, now)}
                      {item.urgent && item.expiresIso !== undefined && ` · expires ${expiresInLabel(item.expiresIso, now)}`}
                      {scope !== null && FLEET_WIDE_KINDS.has(item.kind) && ' · fleet-wide'}
                    </div>
                  </div>
                </div>
              </>
            )}
            renderDetail={(item) => (
              <InboxDetail
                key={item.key}
                item={item}
                fleetWide={scope !== null && FLEET_WIDE_KINDS.has(item.kind)}
                busy={busy}
                error={actionError}
                onAct={(a) => run(item, a)}
                now={now}
                audit={triage.audit}
              />
            )}
          />
        )}
      </div>
    </div>
  );
}
