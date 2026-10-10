// Agent Hub profile tab panels: Overview / Runs / Activity / Tasks / Insights.
// Pure presentational — they take a slice of the AgentProfile bundle and render
// it with the existing design language (stat tiles, mono rows, retro-style
// insight cards). The Definition tab is NOT here: it embeds the existing System
// editor (SystemItemPanel) directly from AgentHub.tsx.

import { Trans, useLingui } from '@lingui/react/macro';
import { Link } from 'react-router-dom';
import type {
  AgentActivity,
  AgentInsights,
  AgentRun,
  AgentTask,
  Recommendation,
  RetroLesson,
} from '../../api/types';
import { fmtAgo, fmtDurationMs, fmtDayShort } from '../../lib/format';
import { displaySlug, findProject } from '../../lib/projectSlug';
import { useScope } from '../../lib/scope';
import { Empty } from '../../components/ui';

/* ----- Overview ----- */

// Overview now leads with the History + Versions block (the agent's usage
// profile, rendered by SystemItemPanel variant="meta" from AgentHub). This tab
// itself renders only the open-insights preview beneath it.
export function OverviewTab({ topInsights }: { topInsights: Recommendation[] }): JSX.Element {
  if (topInsights.length === 0)
    return (
      <Empty>
        <Trans>no open insights</Trans>
      </Empty>
    );
  return (
    <div>
      <div className="mb-1.5 font-mono text-[10px] tracking-[0.1em] text-ink-faint uppercase">
        <Trans>open insights</Trans>
      </div>
      <div className="space-y-1.5">
        {topInsights.slice(0, 3).map((rec) => (
          <div
            key={rec.id}
            className="rounded-lg border border-line bg-bg px-3 py-2 text-[12px] text-ink-dim"
          >
            <span className="mr-1.5 font-mono text-[10px] text-ink-faint">{rec.rule}</span>
            {rec.title}
          </div>
        ))}
      </div>
    </div>
  );
}

/* ----- Runs ----- */

const STATUS_TONE: Record<string, string> = {
  ok: 'text-green',
  error: 'text-red',
  timeout: 'text-amber',
  denied: 'text-amber',
};

function statusTone(status: string): string {
  return STATUS_TONE[status] ?? 'text-ink-dim';
}

export function RunsTab({ runs }: { runs: AgentRun[] }): JSX.Element {
  const { projects } = useScope();
  if (runs.length === 0)
    return (
      <Empty>
        <Trans>no runs in the last 30 days</Trans>
      </Empty>
    );
  return (
    <div className="overflow-hidden rounded-xl border border-line">
      {runs.map((r, i) => (
        <Link
          key={`${r.sessionUuid}-${r.ts}-${String(i)}`}
          to={`/sessions/${encodeURIComponent(r.sessionUuid)}`}
          className="flex items-center gap-3 border-b border-line-soft px-3.5 py-2.5 transition-colors last:border-b-0 hover:bg-surface"
        >
          <span className={`font-mono text-[11px] ${statusTone(r.status)}`}>
            {r.status === 'ok' ? '●' : '▲'}
          </span>
          <div className="min-w-0 flex-1">
            <div className="truncate text-[12.5px] text-ink">
              {r.description !== '' ? r.description : r.sessionTitle || r.sessionUuid}
            </div>
            <div className="font-mono text-[10px] text-ink-faint">
              {r.projectSlug !== ''
                ? `${findProject(projects, r.projectSlug)?.name ?? r.projectSlug} · `
                : ''}
              {fmtAgo(r.ts)}
            </div>
          </div>
          <span className="shrink-0 font-mono text-[10.5px] text-ink-dim">
            {r.durationMs > 0 ? fmtDurationMs(r.durationMs) : ''}
          </span>
        </Link>
      ))}
    </div>
  );
}

/* ----- Activity ----- */

export function ActivityTab({ activity }: { activity: AgentActivity[] }): JSX.Element {
  if (activity.length === 0)
    return (
      <Empty>
        <Trans>no recent events</Trans>
      </Empty>
    );
  return (
    <div className="overflow-hidden rounded-xl border border-line">
      {activity.map((a, i) => (
        <div
          key={`${a.ts}-${String(i)}`}
          className="flex items-center gap-3 border-b border-line-soft px-3.5 py-2 last:border-b-0"
        >
          <span
            className={`font-mono text-[10px] ${a.status === 'error' ? 'text-red' : 'text-ink-faint'}`}
          >
            {a.status === 'error' ? '▲' : '·'}
          </span>
          <span className="font-mono text-[11.5px] text-ink">{a.type}</span>
          {a.toolName !== null && (
            <span className="font-mono text-[10.5px] text-ink-dim">{a.toolName}</span>
          )}
          <span className="ml-auto font-mono text-[10px] text-ink-faint">{fmtAgo(a.ts)}</span>
        </div>
      ))}
    </div>
  );
}

/* ----- Tasks ----- */

function verdictTone(verdict: string | null): string {
  if (verdict === null) return 'text-ink-dim';
  return /re-?dispatch|redo|fail|reject|повтор|відхил|провал|фейл/i.test(verdict)
    ? 'text-red'
    : 'text-green';
}

export function TasksTab({ tasks, projectSlug }: { tasks: AgentTask[]; projectSlug?: string | null }): JSX.Element {
  const { projects } = useScope();
  const { t } = useLingui();
  if (tasks.length === 0)
    return (
      <Empty>
        <Trans>no tasks this agent executed</Trans>
      </Empty>
    );
  // The prop may carry the DB path slug — link with the pretty slug when the
  // project resolves.
  const boardProject = findProject(projects, projectSlug ?? null);
  const boardSlug = boardProject !== null ? displaySlug(boardProject, projects) : projectSlug;
  return (
    <div className="overflow-hidden rounded-xl border border-line">
      {tasks.map((task, i) => {
        const phase = task.phase;
        const inner = (
          <>
            <div className="min-w-0 flex-1">
              <div className="truncate text-[12.5px] text-ink">{task.title}</div>
              <div className="font-mono text-[10px] text-ink-faint">
                {task.externalId}
                {phase !== null ? ` · ${t`phase ${phase}`}` : ''}
                {task.startedAt !== null ? ` · ${fmtDayShort(task.startedAt.slice(0, 10))}` : ''}
              </div>
            </div>
            {task.verdict !== null && (
              <span className={`shrink-0 font-mono text-[10.5px] ${verdictTone(task.verdict)}`}>
                {task.verdict}
              </span>
            )}
            <span className="shrink-0 rounded-[6px] border border-line-strong px-1.5 py-[1px] font-mono text-[9.5px] text-ink-dim">
              {task.status}
            </span>
          </>
        );
        const cls =
          'flex items-center gap-3 border-b border-line-soft px-3.5 py-2.5 last:border-b-0';
        // Link to the project board when we know the project; else a plain row.
        return boardSlug !== undefined && boardSlug !== null ? (
          <Link
            key={`${task.externalId}-${String(i)}`}
            to={`/p/${encodeURIComponent(boardSlug)}/board`}
            className={`${cls} transition-colors hover:bg-surface`}
          >
            {inner}
          </Link>
        ) : (
          <div key={`${task.externalId}-${String(i)}`} className={cls}>
            {inner}
          </div>
        );
      })}
    </div>
  );
}

/* ----- Insights ----- */

const RULE_HUES: Record<string, string> = {
  R1: 'border-blue/40 text-blue',
  R2: 'border-red/40 text-red',
  R3: 'border-amber/40 text-amber',
  R4: 'border-purple/40 text-purple',
  R5: 'border-green/40 text-green',
  R6: 'border-line-strong text-ink-2',
};

function ruleHue(rule: string): string {
  return RULE_HUES[rule] ?? 'border-line-strong text-ink-dim';
}

function LessonRow({ lesson }: { lesson: RetroLesson }): JSX.Element {
  return (
    <div className="rounded-lg border border-line bg-bg px-3 py-2">
      <div className="text-[12.5px] text-ink">{lesson.title}</div>
      {lesson.action !== null && (
        <div className="mt-0.5 text-[11.5px] text-ink-dim">→ {lesson.action}</div>
      )}
      <div className="mt-1 font-mono text-[9.5px] text-ink-faint">
        {lesson.task_external_id} · {lesson.date}
      </div>
    </div>
  );
}

export function InsightsTab({ insights }: { insights: AgentInsights }): JSX.Element {
  const empty =
    insights.recommendations.length === 0 &&
    insights.proposals.length === 0 &&
    insights.lessons.length === 0;
  if (empty)
    return (
      <Empty>
        <Trans>no lessons, recommendations, or proposals for this agent</Trans>
      </Empty>
    );
  return (
    <div className="space-y-4">
      {insights.recommendations.length > 0 && (
        <section>
          <div className="mb-1.5 font-mono text-[10px] tracking-[0.1em] text-ink-faint uppercase">
            <Trans>recommendations</Trans>
          </div>
          <div className="space-y-1.5">
            {insights.recommendations.map((rec) => (
              <div key={rec.id} className="rounded-lg border border-line bg-bg px-3 py-2">
                <div className="flex items-center gap-2">
                  <span
                    className={`rounded-[6px] border px-1.5 py-[1px] font-mono text-[10px] font-medium ${ruleHue(rec.rule)}`}
                  >
                    {rec.rule}
                  </span>
                  <span className="text-[12.5px] text-ink">{rec.title}</span>
                  <span className="ml-auto font-mono text-[9.5px] text-ink-faint">{rec.status}</span>
                </div>
                <div className="mt-1 text-[11.5px] text-ink-dim">{rec.detail}</div>
              </div>
            ))}
          </div>
        </section>
      )}

      {insights.proposals.length > 0 && (
        <section>
          <div className="mb-1.5 font-mono text-[10px] tracking-[0.1em] text-ink-faint uppercase">
            <Trans>change proposals</Trans>
          </div>
          <div className="space-y-1.5">
            {insights.proposals.map((p) => (
              <div key={p.id} className="rounded-lg border border-line bg-bg px-3 py-2">
                <div className="flex items-center gap-2">
                  <span className="text-[12.5px] text-ink">
                    <Trans>Rewrite proposal</Trans>
                  </span>
                  <span className="ml-auto font-mono text-[9.5px] text-ink-faint">{p.status}</span>
                </div>
                <div className="mt-1 line-clamp-2 text-[11.5px] text-ink-dim">{p.rationale}</div>
                {p.pr_url !== null && (
                  <a
                    href={p.pr_url}
                    target="_blank"
                    rel="noreferrer"
                    className="mt-1 inline-block font-mono text-[10.5px] text-brand hover:underline"
                  >
                    <Trans>view PR →</Trans>
                  </a>
                )}
              </div>
            ))}
          </div>
        </section>
      )}

      {insights.lessons.length > 0 && (
        <section>
          <div className="mb-1.5 font-mono text-[10px] tracking-[0.1em] text-ink-faint uppercase">
            <Trans>lessons</Trans>
          </div>
          <div className="space-y-1.5">
            {insights.lessons.map((l) => (
              <LessonRow key={`${l.task_external_id}-${String(l.seq)}`} lesson={l} />
            ))}
          </div>
        </section>
      )}
    </div>
  );
}
