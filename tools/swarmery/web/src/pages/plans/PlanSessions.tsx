// The sessions of a plan.

import { plural } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { fetchSessions } from '../../api';
import type { Epic, Session, SessionStatus } from '../../api/types';
import { Empty, ErrorBox, Loading } from '../../components/ui';
import { fmtCost, fmtDateTime } from '../../lib/format';
import { scopePlanSessions, type PlanSessionScope } from '../../lib/planSessionScope';
import { useSessionHref } from '../../lib/sessionHref';

const SESSION_DOT: Record<SessionStatus, string> = {
  active: 'bg-green',
  waiting_approval: 'bg-amber',
  awaiting_reply: 'bg-amber',
  idle: 'bg-ink-faint',
  completed: 'bg-brand/60',
  killed: 'bg-red/70',
};

/** Every session of the plan — the plan-run controller, each phase run, and the
 * subagents under them — resolved server-side from the stamped run branch / worktree
 * cwd (?planTask=, see internal/api/session_plan_group.go), the same rule the Sessions
 * page groups by. The ledger reads its runs and costs from this, so the one fetch
 * feeds both. Run edges mint and end sessions; `epic` is already refetched on the
 * plan_updated WS frame, so a fingerprint of its run state is the cheapest live
 * trigger there is — no second subscription. */
export function usePlanSessions(epic: Epic): { sessions: Session[] | null; error: string | null } {
  const [sessions, setSessions] = useState<Session[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const runFingerprint =
    epic.phases.map((p) => `${p.id}:${p.runState}:${p.runSessionUuid ?? ''}`).join('|') +
    `#${epic.planRun?.runState ?? ''}:${epic.planRun?.runSessionUuid ?? ''}`;
  useEffect(() => {
    let alive = true;
    setError(null);
    fetchSessions({ planTask: epic.taskId }, { limit: 200 })
      .then((page) => {
        if (alive) setSessions(page.sessions);
      })
      .catch((e: unknown) => {
        if (alive) setError(e instanceof Error ? e.message : String(e));
      });
    return () => {
      alive = false;
    };
  }, [epic.taskId, runFingerprint]);
  return { sessions, error };
}

/** The plan's sessions, under the ledger or under an open phase. The phase rows show
 * their own runs; this list is the rest of the story: the plan-run controller, the
 * subagents, and the operator's own sessions linked to the plan. It is the only place
 * those surface, and the only way to a finished plan's transcripts that a phase row
 * does not name. Folded on the ledger, open under a phase, until the operator picks. */
export function PlanSessions({
  epic,
  activePhaseId,
  sessions,
  err,
}: {
  epic: Epic;
  /** The phase whose details are open, so its sessions read as selected. */
  activePhaseId: number | null;
  sessions: Session[] | null;
  err: string | null;
}): JSX.Element {
  const { t } = useLingui();
  const sessionHref = useSessionHref();
  const [folded, setFolded] = useState<boolean | null>(null);
  const open = !(folded ?? activePhaseId === null);

  // The operator's hand-picked slice lives WITH the plan it belongs to:
  // [taskId, scope]. Keying on taskId (the same idiom as `runMsg`, above) means
  // the pick cannot leak onto another plan and needs no reset effect — so the
  // refetch on `runFingerprint`, which replaces the whole session array whenever
  // a run ends, cannot knock the operator back to the automatic slice.
  const [picked, setPicked] = useState<{ taskId: number; scope: PlanSessionScope } | null>(null);
  const pickedScope = picked !== null && picked.taskId === epic.taskId ? picked.scope : null;

  // Ordering, dedup of the two sources, the phase slice and the auto-fallback
  // are one pure decision — see lib/planSessionScope.ts. This component only
  // renders what comes back.
  const view = useMemo(
    () =>
      scopePlanSessions({
        sessions: sessions ?? [],
        linked: epic.linkedSessions,
        activePhaseId,
        picked: pickedScope,
      }),
    [sessions, epic.linkedSessions, activePhaseId, pickedScope],
  );

  // The human label for the open phase — `phaseId` is a database id, `seq` is
  // what the timeline next to this column calls it.
  const activePhaseSeq = epic.phases.find((p) => p.id === activePhaseId)?.seq ?? null;
  const seqLabel = String(activePhaseSeq ?? '?');
  const phaseCount = String(view.phaseCount);
  const totalCount = String(view.totalCount);

  return (
    <section className="mt-[22px]" aria-label={t`plan sessions`}>
      <div className="flex items-center gap-2 leading-[normal]">
        <button
          type="button"
          aria-expanded={open}
          onClick={() => setFolded(open)}
          className="flex cursor-pointer items-center gap-2 font-mono text-[10px] tracking-[0.1em] text-ink-faint uppercase transition-colors hover:text-ink"
        >
          <Trans>sessions</Trans>
          <span aria-hidden="true">{open ? '▾' : '▸'}</span>
        </button>
        <span aria-hidden="true" className="h-px flex-1 bg-line" />
        {sessions !== null &&
          // A plan with no sessions at all has nothing to switch between, so it
          // keeps the bare count rather than a toggle that cannot do anything.
          (view.phaseCount === null || view.totalCount === 0 ? (
            <span className="font-mono text-[10px] text-ink-faint">{view.totalCount}</span>
          ) : (
            // Both numbers stay on screen in either slice, so a narrowed column
            // never hides the FACT that it is hiding rows — and `#N (0)` is the
            // visible marker of the auto-fallback.
            //
            // The visible label is two counts, which say nothing on their own to
            // a screen reader, so the accessible name spells the state out and
            // `aria-pressed` carries the flip. Both keep the literal prefix
            // "session scope" — that is what selectors match on.
            <button
              type="button"
              aria-pressed={view.scope === 'phase'}
              aria-label={
                view.scope === 'phase'
                  ? plural(view.totalCount, {
                      one: `session scope: phase ${seqLabel} only, ${phaseCount} of # session`,
                      few: `session scope: phase ${seqLabel} only, ${phaseCount} of # sessions`,
                      many: `session scope: phase ${seqLabel} only, ${phaseCount} of # sessions`,
                      other: `session scope: phase ${seqLabel} only, ${phaseCount} of # sessions`,
                    })
                  : plural(view.totalCount, {
                      one: 'session scope: all # session of the plan',
                      few: 'session scope: all # sessions of the plan',
                      many: 'session scope: all # sessions of the plan',
                      other: 'session scope: all # sessions of the plan',
                    })
              }
              data-tip={
                view.scope === 'phase'
                  ? t`showing this phase only — click for every session of the plan`
                  : view.fellBack
                    ? t`this phase has no sessions of its own — showing the whole plan; click to narrow anyway`
                    : t`showing every session of the plan — click to narrow to this phase`
              }
              onClick={() =>
                setPicked({
                  taskId: epic.taskId,
                  scope: view.scope === 'phase' ? 'all' : 'phase',
                })
              }
              className="rounded font-mono text-[10px] text-ink-faint transition-colors hover:text-ink focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-brand"
            >
              {/* Colour alone must not carry the state — the active side is also
                  underlined, which survives a monochrome or high-contrast view. */}
              <span
                className={
                  view.scope === 'phase' ? 'text-ink underline decoration-dotted underline-offset-2' : undefined
                }
              >
                #{activePhaseSeq ?? '?'} ({view.phaseCount})
              </span>
              <span className="px-1">/</span>
              <span
                className={
                  view.scope === 'all' ? 'text-ink underline decoration-dotted underline-offset-2' : undefined
                }
              >
                <Trans>all ({totalCount})</Trans>
              </span>
            </button>
          ))}
      </div>
      {!open ? null : err !== null ? (
        <div className="mt-2">
          <ErrorBox message={err} />
        </div>
      ) : sessions === null ? (
        <div className="mt-2">
          <Loading />
        </div>
      ) : view.totalCount === 0 ? (
        <div className="mt-2">
          <Empty>
            <Trans>no sessions ran this plan</Trans>
          </Empty>
        </div>
      ) : view.sessions.length === 0 && view.linked.length === 0 ? (
        // Only reachable through a deliberate pick: the automatic rule hands back
        // 'all' when the phase slice would be empty.
        <div className="mt-2">
          <Empty>
            <Trans>no sessions for this phase</Trans>
          </Empty>
        </div>
      ) : (
        <ol className="mt-2 grid gap-1 desk:grid-cols-2">
          {view.sessions.map((s) => {
            const g = s.planGroup ?? null;
            const label = g?.role === 'phase' ? `#${String(g.phaseSeq ?? '?')}` : t`plan`;
            const selected = g?.role === 'phase' && g.phaseId === activePhaseId;
            return (
              <li key={s.id}>
                <Link
                  to={sessionHref(s.sessionUuid)}
                  data-tip={g?.phaseName ?? undefined}
                  className={`block rounded-lg border px-2 py-1.5 transition-colors ${
                    selected
                      ? 'border-line-strong bg-surface2'
                      : 'border-line bg-surface/40 hover:border-line-strong'
                  }`}
                >
                  <div className="flex items-center gap-1.5">
                    <span
                      className={`h-1.5 w-1.5 shrink-0 rounded-full ${SESSION_DOT[s.status]} ${
                        s.status === 'active' ? 'animate-pulse' : ''
                      }`}
                    />
                    <span
                      className={`shrink-0 rounded border px-1 py-px font-mono text-[9px] ${
                        g?.role === 'phase' ? 'border-line text-ink-dim' : 'border-brand/40 text-brand'
                      }`}
                    >
                      {label}
                    </span>
                    <span className="truncate text-[11.5px] text-ink">
                      {s.title ?? s.why ?? s.sessionUuid.slice(0, 8)}
                    </span>
                  </div>
                  <div className="mt-0.5 truncate font-mono text-[9.5px] text-ink-faint">
                    {fmtDateTime(s.startedAt)} · {s.status}
                    {s.costUsd != null ? ` · ${fmtCost(s.costUsd)}` : ''}
                  </div>
                </Link>
              </li>
            );
          })}
          {view.linked.map((l) => (
            <li key={l.sessionUuid}>
              <Link
                to={sessionHref(l.sessionUuid)}
                data-tip={
                  l.linkSource === 'heuristic'
                    ? t`inferred from files this session edited under plan/`
                    : t`linked to this plan by the daemon`
                }
                className="block rounded-lg border border-line border-dashed bg-surface/40 px-2 py-1.5 transition-colors hover:border-line-strong"
              >
                <div className="flex items-center gap-1.5">
                  <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-ink-faint" />
                  <span
                    className={`shrink-0 rounded border px-1 py-px font-mono text-[9px] ${
                      l.linkSource === 'heuristic'
                        ? 'border-line text-ink-faint'
                        : 'border-line text-ink-dim'
                    }`}
                  >
                    {l.linkSource === 'heuristic' ? t`inferred` : t`linked`}
                  </span>
                  <span className="truncate text-[11.5px] text-ink">{l.sessionUuid.slice(0, 8)}</span>
                </div>
                <div className="mt-0.5 truncate font-mono text-[9.5px] text-ink-faint">
                  {fmtDateTime(l.startedAt)}
                  {l.endedAt === null ? t` · live` : ''}
                  {l.costUsd != null ? ` · ${fmtCost(l.costUsd)}` : ''}
                </div>
              </Link>
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}
