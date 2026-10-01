// One plan run as ONE row in the day timeline (Sessions redesign).
//
// A plan run fans out into a controller session plus one session per phase, and
// every one of them opens with the same boilerplate prompt ("You are the
// controller for an ENTIRE approved implementation plan…"). Flat, those rows are
// indistinguishable AND they bury the day list. So the run collapses to a single
// bordered card: caret · PLAN RUN pill · project · plan title · status · tallies
// · "open plan →". Expanding fans the rows out INDENTED and identified by role
// (`ctl` / `#5`), never by their boilerplate title.
//
// The card is the filter unit: Sessions keeps it whole when any row matches the
// status chip (lib/sessionsView), so a filter never shows half a run.

import { Link } from 'react-router-dom';
import { projectLabel } from '../lib/format';
import { plansTaskHref } from '../pages/plans/plansUrl';
import { useProjectColor } from '../lib/projectColors';
import {
  planRowNotes,
  planRowTag,
  planRowTitle,
  runIsRunning,
  statusSummary,
  type PlanRun,
} from '../lib/sessionsView';
import { SessionCard } from './SessionCard';

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
  const colorFor = useProjectColor();
  // A run knows its plan's task id, not its externalId: hand off through the
  // permanent ?task= resolver, which replaces to the canonical plan path.
  const plansHref = plansTaskHref(slug ?? run.projectSlug, run.taskId);
  const running = runIsRunning(run.rows);
  const notes = planRowNotes(run.rows);
  const count = run.rows.length;
  const meta = `${String(count)} session${count === 1 ? '' : 's'} · ${statusSummary(run.rows)}`;

  return (
    <div className="mb-2.5 overflow-hidden rounded-xl border border-line bg-surface">
      {/* role=button + a nested stopPropagation link mirrors SessionCard: an <a>
          intercepts clicks before React's synthetic system can stop them, so the
          "open plan →" link must live OUTSIDE any anchor and cancel bubbling
          itself — otherwise opening the plan would also toggle the card. */}
      <div
        role="button"
        tabIndex={0}
        aria-expanded={expanded}
        aria-label={`plan run ${run.title}`}
        onClick={onToggle}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault();
            onToggle();
          }
        }}
        className="flex min-h-[44px] cursor-pointer flex-wrap items-center gap-x-2.5 gap-y-1 px-3 py-2.5 transition-colors hover:bg-surface2 focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-brand"
      >
        <span aria-hidden="true" className="w-2 shrink-0 font-mono text-[10.5px] text-ink-faint">
          {expanded ? '▾' : '▸'}
        </span>
        {/* Purple, not a status hue: the pill says WHAT the row is, and must not
            read as another state beside the running/done chip. --color-purple is
            re-tuned per palette, so it clears AA in light mode too. */}
        <span className="shrink-0 rounded-full border border-purple/40 bg-purple/15 px-[7px] py-0.5 font-mono text-[9.5px] tracking-[0.1em] whitespace-nowrap text-purple uppercase">
          plan run
        </span>
        {!hideProject && (
          <span className="flex shrink-0 items-center gap-1.5">
            <span
              aria-hidden="true"
              className="h-[7px] w-[7px] shrink-0 rounded-full"
              style={{ backgroundColor: colorFor(run.projectSlug) }}
            />
            <span
              className="max-w-[130px] truncate font-mono text-[11px]"
              style={{ color: colorFor(run.projectSlug) }}
            >
              {projectLabel(run.projectName, run.projectSlug)}
            </span>
          </span>
        )}
        {/* The title is the row's identity, so it keeps a floor width and the
            meta line is the one that gives way (min-w-0) at narrow widths. */}
        <span className="flex min-w-0 flex-1 basis-[13rem] items-baseline gap-2.5">
          <span className="min-w-[7rem] shrink truncate text-[13.5px] font-semibold">
            {run.title}
          </span>
          <span className="min-w-0 shrink truncate font-mono text-[10.5px] text-ink-dim">
            {meta}
          </span>
        </span>
        <span
          className={`shrink-0 rounded-full border px-[9px] py-0.5 font-mono text-[10.5px] whitespace-nowrap ${
            running ? 'border-green/40 text-green' : 'border-line-strong text-ink-dim'
          }`}
        >
          {running ? 'running' : 'done'}
        </span>
        <Link
          to={plansHref}
          onClick={(e) => e.stopPropagation()}
          className="shrink-0 rounded px-1 py-1.5 font-mono text-[10.5px] whitespace-nowrap text-ink-dim transition-colors hover:text-brand focus-visible:outline-2 focus-visible:outline-brand"
        >
          open plan →
        </Link>
      </div>
      {expanded && (
        <div className="divide-y divide-line-soft border-t border-line-soft pl-2 desk:pl-5">
          {run.rows.map((s) => (
            <SessionCard
              key={s.id}
              session={s}
              now={nowById[s.id] ?? null}
              flat
              dense
              hideProject
              roleTag={planRowTag(s)}
              label={planRowTitle(s)}
              subline={notes.get(s.id) ?? null}
            />
          ))}
        </div>
      )}
    </div>
  );
}
