// One plan run as ONE card in the list's Plan runs group, the audit view of the
// fan-out (the groups above list each of its sessions on its own row).
//
// A plan run fans out into a controller session plus one session per phase, and
// every one of them opens with the same boilerplate prompt ("You are the
// controller for an ENTIRE approved implementation plan…"). So the run collapses
// to a single card. Its header stands in the list like a session row (artboard
// 2a on v4, plan D-7): the same grid and padding (listRowFrame) inside the card's
// frame, a chevron where a row has its glyph, the plan title with `plan run` in
// brand text (never a pill), one grey sentence `phase N · controller + K
// sessions · project`, and `open plan →` where a row has its age. The status
// tallies moved into the header's tooltip. Expanding fans the rows out
// identified by role (`ctl` / `#5`), never by their boilerplate title.

import { Trans, useLingui } from '@lingui/react/macro';
import { Link } from 'react-router-dom';
import { projectLabel } from '../lib/format';
import { useTheme } from '../lib/theme';
import { plansTaskHref } from '../pages/plans/plansUrl';
import { planRowNotes, planRowTag, planRowTitle, planRunLine, statusSummary, type PlanRun } from '../lib/sessionsView';
import { listRowFrame } from './place/ListRow';
import { SessionRow } from './SessionRow';
import { Chevron } from './Chevron';

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
  const { compact } = useTheme();
  const line = planRunLine(run.rows);
  const sentence = hideProject ? line : `${line} · ${projectLabel(run.projectName, run.projectSlug)}`;
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
        data-tip={statusSummary(run.rows)}
        onClick={onToggle}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault();
            onToggle();
          }
        }}
        className={`${listRowFrame(compact)} border-transparent hover:bg-surface2 focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-brand`}
      >
        <span aria-hidden="true" className="mt-px font-mono text-[10.5px] text-ink-faint">
          <Chevron open={expanded} />
        </span>
        <span className="block min-w-0">
          <span className="flex min-w-0 items-baseline gap-1.5">
            <span className="min-w-0 truncate text-[13px] font-medium text-ink">{run.title}</span>
            <span data-testid="plan-run-label" className="shrink-0 font-mono text-[10px] text-brand">
              <Trans>plan run</Trans>
            </span>
          </span>
          <span className="mt-0.5 block truncate text-[12px] text-ink-dim">{sentence}</span>
        </span>
        <Link
          to={plansHref}
          onClick={(e) => e.stopPropagation()}
          className="font-mono text-[10.5px] whitespace-nowrap text-ink-faint transition-colors hover:text-brand focus-visible:outline-2 focus-visible:outline-brand"
        >
          <Trans>open plan →</Trans>
        </Link>
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
