// One plan run as ONE card in the list's Plan runs group, the audit view of the
// fan-out (the groups above list each of its sessions on its own row).
//
// A plan run fans out into a controller session plus one session per phase, and
// every one of them opens with the same boilerplate prompt ("You are the
// controller for an ENTIRE approved implementation plan…"). So the run collapses
// to a single card: caret · plan title · plan run · project · tallies · "open
// plan →". Expanding fans the rows out identified by role (`ctl` / `#5`), never
// by their boilerplate title. A category gets no colour (handoff §4): the tag is
// grey text.

import { plural } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { Link } from 'react-router-dom';
import { projectLabel } from '../lib/format';
import { plansTaskHref } from '../pages/plans/plansUrl';
import { planRowNotes, planRowTag, planRowTitle, statusSummary, type PlanRun } from '../lib/sessionsView';
import { SessionRow } from './SessionRow';

export function PlanRunCard({
  run,
  expanded,
  onToggle,
  slug,
  hideProject = false,
  nowById = {},
}: {
  run: PlanRun;
  expanded: boolean;
  onToggle: () => void;
  /** Project slug of the surrounding route, when the page is project-scoped. */
  slug: string | undefined;
  /** Drop the project cell — redundant when the list is already scoped. */
  hideProject?: boolean;
  /** Live "now: <last action>" lines by session id (event_appended WS). */
  nowById?: Record<number, string>;
}): JSX.Element {
  const { t } = useLingui();
  // A run knows its plan's task id, not its externalId: hand off through the
  // permanent ?task= resolver, which replaces to the canonical plan path.
  const plansHref = plansTaskHref(slug ?? run.projectSlug, run.taskId);
  const notes = planRowNotes(run.rows);
  const count = run.rows.length;
  const sessions = plural(count, { one: '# session', few: '# sessions', many: '# sessions', other: '# sessions' });
  const summary = statusSummary(run.rows);
  const meta = `${sessions} · ${summary}`;
  const runTitle = run.title;

  return (
    <div className="mb-2 overflow-hidden rounded-[8px] border border-line bg-surface">
      {/* role=button + a nested stopPropagation link: an <a> intercepts clicks
          before React's synthetic system can stop them, so "open plan →" lives
          outside any anchor and cancels bubbling itself, or opening the plan
          would also toggle the card. */}
      <div
        role="button"
        tabIndex={0}
        aria-expanded={expanded}
        aria-label={t`plan run ${runTitle}`}
        onClick={onToggle}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault();
            onToggle();
          }
        }}
        className="cursor-pointer px-3 py-2 transition-colors hover:bg-surface2 focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-brand"
      >
        <div className="flex min-w-0 items-baseline gap-1.5">
          <span aria-hidden="true" className="w-2 shrink-0 font-mono text-[10.5px] text-ink-faint">
            {expanded ? '▾' : '▸'}
          </span>
          <span className="min-w-0 truncate text-[13px] font-medium text-ink">{run.title}</span>
          <span className="shrink-0 font-mono text-[10px] text-ink-faint">
            <Trans>plan run</Trans>
          </span>
        </div>
        <div className="mt-0.5 flex min-w-0 items-baseline justify-between gap-2 pl-[14px] font-mono text-[10.5px] text-ink-dim">
          <span className="min-w-0 truncate">
            {hideProject ? meta : `${projectLabel(run.projectName, run.projectSlug)} · ${meta}`}
          </span>
          <Link
            to={plansHref}
            onClick={(e) => e.stopPropagation()}
            className="shrink-0 whitespace-nowrap transition-colors hover:text-brand focus-visible:outline-2 focus-visible:outline-brand"
          >
            <Trans>open plan →</Trans>
          </Link>
        </div>
      </div>
      {expanded && (
        <div className="border-t border-line-soft">
          {run.rows.map((s) => (
            <SessionRow
              key={s.id}
              session={s}
              now={nowById[s.id] ?? null}
              hideProject
              tag={planRowTag(s)}
              label={planRowTitle(s)}
              note={notes.get(s.id) ?? null}
            />
          ))}
        </div>
      )}
    </div>
  );
}
