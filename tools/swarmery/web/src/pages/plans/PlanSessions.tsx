// The sessions of a plan, as the Runs tab's one card list (plan D-29): grouped
// by attempt under an open phase, by phase (and the plan-run controller) over
// the whole plan. A card is named by its role, never by `s.title` (the start
// of its boilerplate service prompt) — see runsModel.ts.

import { plural } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { fetchSessions } from '../../api';
import type { Epic, Session } from '../../api/types';
import { Empty, ErrorBox, Loading } from '../../components/ui';
import { StateGlyph } from '../../components/place/StateGlyph';
import { fmtCost, fmtDateTime } from '../../lib/format';
import { modelShortName } from '../../lib/sessionModelChip';
import { scopePlanSessions, type PlanSessionScope } from '../../lib/planSessionScope';
import { useSessionHref } from '../../lib/sessionHref';
import { Chevron } from '../../components/Chevron';
import { runSpan } from './PhaseLedger';
import { sessionGlyph } from './phaseLedgerModel';
import { phaseAttemptGroups, planPhaseGroups, runCardTitle, type RunGroup } from './runsModel';
import { useNow } from './phaseChips';

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

/** One session as one card: a state glyph, its role-based name, a mono
 *  `agent · model · status` line, and `date · cost · duration` at the right.
 *  The whole card is the session's link (opens in a new tab on Cmd-click). */
function SessionCard({ s, now, executorUuid }: { s: Session; now: number; executorUuid: string | null }): JSX.Element {
  const sessionHref = useSessionHref();
  const { t } = useLingui();
  const title = runCardTitle(s, executorUuid);
  const span = runSpan(s, now);
  return (
    <li>
      <Link
        to={sessionHref(s.sessionUuid)}
        data-tip={s.planGroup?.phaseName ?? undefined}
        className="block h-full rounded-lg border border-line bg-surface/40 px-2.5 py-2 transition-colors hover:border-line-strong focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-brand"
      >
        <div className="flex items-center gap-1.5">
          <StateGlyph state={sessionGlyph(s.status)} size={12} />
          <span className="min-w-0 truncate text-[12.5px] font-medium text-ink">{title}</span>
        </div>
        <div className="mt-1 truncate font-mono text-[10px] text-ink-faint">
          {s.model !== null ? modelShortName(s.model) : t`—`} · {s.status}
        </div>
        <div className="mt-0.5 flex items-center justify-between font-mono text-[9.5px] text-ink-faint">
          <span className="truncate">{fmtDateTime(s.startedAt)}</span>
          <span className="shrink-0">
            {s.costUsd != null ? `${fmtCost(s.costUsd)} · ` : ''}
            {span}
          </span>
        </div>
      </Link>
    </li>
  );
}

/** One linked-only row (no `planGroup`, attached by the daemon or a cwd
 *  heuristic) as the same card shape, named generically — never its title. */
function LinkedCard({ l }: { l: Epic['linkedSessions'][number] }): JSX.Element {
  const sessionHref = useSessionHref();
  const { t } = useLingui();
  return (
    <li>
      <Link
        to={sessionHref(l.sessionUuid)}
        data-tip={l.linkSource === 'heuristic' ? t`inferred from files this session edited under plan/` : t`linked to this plan by the daemon`}
        className="block h-full rounded-lg border border-line border-dashed bg-surface/40 px-2.5 py-2 transition-colors hover:border-line-strong focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-brand"
      >
        <div className="flex items-center gap-1.5">
          <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-ink-faint" />
          <span className="min-w-0 truncate text-[12.5px] font-medium text-ink">
            {l.linkSource === 'heuristic' ? t`inferred session` : t`linked session`}
          </span>
        </div>
        <div className="mt-1 truncate font-mono text-[9.5px] text-ink-faint">
          {fmtDateTime(l.startedAt)}
          {l.endedAt === null ? t` · live` : ''}
          {l.costUsd != null ? ` · ${fmtCost(l.costUsd)}` : ''}
        </div>
      </Link>
    </li>
  );
}

// i18n-ignore — a Tailwind arbitrary value; its comma trips the class-list scanner
const CARD_GRID = 'mt-1.5 grid gap-1.5 grid-cols-[repeat(auto-fill,minmax(320px,1fr))]';

function GroupBlock({ group, now }: { group: RunGroup; now: number }): JSX.Element {
  const { t } = useLingui();
  return (
    <section className="mt-3">
      <div className="flex flex-wrap items-baseline gap-1.5 font-mono text-[10px] tracking-[0.08em] text-ink-faint uppercase">
        <span>{group.label}</span>
        {group.costUsd != null && <span className="text-ink-dim normal-case">{fmtCost(group.costUsd)}</span>}
      </div>
      {group.reason !== null && group.reason !== '' && (
        <p className="mt-0.5 text-[11px] text-ink-dim normal-case">{t`started because: ${group.reason}`}</p>
      )}
      <ol className={`${CARD_GRID} list-none p-0`}>
        {group.sessions.map((s) => (
          <SessionCard key={s.id} s={s} now={now} executorUuid={group.executorUuid} />
        ))}
      </ol>
    </section>
  );
}

/** The plan's sessions, under the ledger or under an open phase's Runs tab.
 *  Under a phase, cards group by attempt; over the whole plan, by phase (with
 *  the plan-run controller's own sessions in a group of their own, first). */
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
  const now = useNow(30_000);
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
  // groups and renders what comes back.
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

  const activePhase = epic.phases.find((p) => p.id === activePhaseId) ?? null;
  const groups = useMemo(() => {
    if (view.scope === 'phase' && activePhase !== null) {
      return phaseAttemptGroups(view.sessions, activePhase.reopens ?? [], activePhase.runSessionUuid);
    }
    return planPhaseGroups(epic, view.sessions);
  }, [view.scope, view.sessions, activePhase, epic]);

  const seqLabel = String(activePhase?.seq ?? '?');
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
          <span aria-hidden="true"><Chevron open={open} /></span>
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
                #{activePhase?.seq ?? '?'} ({view.phaseCount})
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
        <>
          {groups.map((g) => (
            <GroupBlock key={g.key} group={g} now={now} />
          ))}
          {view.linked.length > 0 && (
            <section className="mt-3">
              <div className="font-mono text-[10px] tracking-[0.08em] text-ink-faint uppercase">
                <Trans>other sessions</Trans>
              </div>
              <ol className={`${CARD_GRID} list-none p-0`}>
                {view.linked.map((l) => (
                  <LinkedCard key={l.sessionUuid} l={l} />
                ))}
              </ol>
            </section>
          )}
        </>
      )}
    </section>
  );
}
