// The numbers behind the session, a fold under the Steps ledger (plan D-7): it goes
// deeper than the rail beside the conversation. Context hogs, then usage blocks —
// MODELS, AGENTS, SKILLS — each in its own card with a per-row bar showing how much it was
// used relative to the group's top entry. Then the CALL TREE (who called what:
// skills → tools → subagents, recursive) and FILES CHANGED aggregated per path
// (one row per file, +/− summed across all its edits, sorted by churn). Category
// gets no colour (handoff §4), so the bars are neutral. Everything is derived
// client-side from the already-loaded detail — no extra API calls.

import { Trans, useLingui } from '@lingui/react/macro';
import { useMemo } from 'react';
import type { Event, FileChange, Turn } from '../../api/types';
import { fmtDurationMs, fmtTokens } from '../../lib/format';
import { subagentDescription, subagentName, skillName } from '../../lib/payload';
import { buildCallTree } from '../../lib/calltree';
import { CallTreeCard } from './CallTree';
import { ContextHogsCard } from './ContextHogsCard';

interface UsageRow {
  name: string;
  /** Right-side label: "×3 · 5 m 12 s", "×12 · 295K tok", … */
  detail: string;
  /** 0..1 — usage relative to the group's top entry (drives the bar width). */
  fraction: number;
  /** Native tooltip (full model id, per-run task descriptions, …). */
  title?: string | undefined;
}

/** Per-model usage from assistant turns: turn count + token sum; bar by tokens
 *  when any model reported them, else by turn count. */
function deriveModels(turns: Turn[]): UsageRow[] {
  const byModel = new Map<string, { count: number; tokens: number }>();
  for (const turn of turns) {
    if (turn.model === null) continue;
    const entry = byModel.get(turn.model) ?? { count: 0, tokens: 0 };
    entry.count += 1;
    entry.tokens += (turn.tokensIn ?? 0) + (turn.tokensOut ?? 0);
    byModel.set(turn.model, entry);
  }
  const entries = [...byModel.entries()];
  const byTokens = entries.some(([, e]) => e.tokens > 0);
  const max = Math.max(1, ...entries.map(([, e]) => (byTokens ? e.tokens : e.count)));
  return entries
    .map(([model, e]) => ({
      name: model.replace(/^claude-/, ''),
      detail: `×${String(e.count)}${e.tokens > 0 ? ` · ${fmtTokens(e.tokens)} tok` : ''}`,
      fraction: (byTokens ? e.tokens : e.count) / max,
      title: model,
    }))
    .sort((a, b) => b.fraction - a.fraction);
}

/** Per-agent-type usage from subagent runs: ×N + total duration; bar by runs. */
function deriveAgents(events: Event[]): UsageRow[] {
  const byName = new Map<
    string,
    { count: number; running: number; totalMs: number; titles: string[] }
  >();
  for (const event of events) {
    if (event.type !== 'subagent_start') continue;
    const stop = events.find((e) => e.type === 'subagent_stop' && e.parentEventId === event.id);
    const name = subagentName(event);
    const group = byName.get(name) ?? { count: 0, running: 0, totalMs: 0, titles: [] };
    group.count += 1;
    if (stop === undefined) group.running += 1;
    const ms = stop?.durationMs ?? null;
    if (ms !== null) group.totalMs += ms;
    const desc = subagentDescription(event);
    if (desc !== null) {
      group.titles.push(ms !== null ? `${desc} — ${fmtDurationMs(ms)}` : `${desc} — running`);
    }
    byName.set(name, group);
  }
  const max = Math.max(1, ...[...byName.values()].map((g) => g.count));
  return [...byName.entries()]
    .map(([name, g]) => ({
      name,
      detail: [
        `×${String(g.count)}`,
        g.totalMs > 0 ? fmtDurationMs(g.totalMs) : null,
        g.running > 0 ? 'running' : null,
      ]
        .filter((part) => part !== null)
        .join(' · '),
      fraction: g.count / max,
      title: g.titles.length > 0 ? g.titles.join('\n') : undefined,
    }))
    .sort((a, b) => b.fraction - a.fraction);
}

/** Per-skill usage: ×N invocations; bar by count. */
function deriveSkillUsage(events: Event[]): UsageRow[] {
  const byName = new Map<string, number>();
  for (const event of events) {
    if (event.type !== 'skill_use') continue;
    const name = skillName(event);
    if (name === null) continue;
    byName.set(name, (byName.get(name) ?? 0) + 1);
  }
  const max = Math.max(1, ...byName.values());
  return [...byName.entries()]
    .map(([name, count]) => ({
      name,
      detail: `×${String(count)}`,
      fraction: count / max,
    }))
    .sort((a, b) => b.fraction - a.fraction);
}

interface FileRow {
  path: string;
  additions: number;
  deletions: number;
}

/** One row per file path: +/− summed over all its file_change rows, sorted by total churn desc. */
function aggregateFileChanges(changes: FileChange[]): FileRow[] {
  const byPath = new Map<string, FileRow>();
  for (const change of changes) {
    const row = byPath.get(change.filePath) ?? {
      path: change.filePath,
      additions: 0,
      deletions: 0,
    };
    row.additions += change.additions ?? 0;
    row.deletions += change.deletions ?? 0;
    byPath.set(change.filePath, row);
  }
  return [...byPath.values()].sort(
    (a, b) => b.additions + b.deletions - (a.additions + a.deletions),
  );
}

function UsageBlock({
  label,
  labelTone,
  barTone,
  rows,
  className,
}: {
  label: string;
  labelTone: string;
  barTone: string;
  rows: UsageRow[];
  className?: string;
}): JSX.Element {
  return (
    <div className={`rounded-xl border border-line bg-surface px-4 py-3.5 ${className ?? ''}`}>
      <div className="mb-1 flex items-baseline justify-between">
        <span className={`font-mono text-[10.5px] tracking-[0.08em] uppercase ${labelTone}`}>
          {label}
        </span>
        <span className="font-mono text-[12px] font-bold text-ink">{rows.length}</span>
      </div>
      {rows.map((row) => (
        <div
          key={row.name}
          data-tip={row.title}
          className="border-b border-line-soft py-1.5 last:border-b-0"
        >
          <div className="flex items-baseline gap-2 font-mono text-[11px]">
            <span className="min-w-0 flex-1 truncate text-ink-2">{row.name}</span>
            <span className="shrink-0 text-ink-dim">{row.detail}</span>
          </div>
          <div className="mt-1 h-[3px] overflow-hidden rounded-full bg-surface2">
            <div
              className={`h-full rounded-full ${barTone}`}
              style={{ width: `${String(Math.max(4, Math.round(row.fraction * 100)))}%` }}
            />
          </div>
        </div>
      ))}
    </div>
  );
}

export function DetailRail({
  sessionId,
  turns,
  events,
  fileChanges,
  onShowDiffs,
}: {
  sessionId: number;
  turns: Turn[];
  events: Event[];
  fileChanges: FileChange[];
  onShowDiffs: (path?: string) => void;
}): JSX.Element | null {
  const { t } = useLingui();
  const models = useMemo(() => deriveModels(turns), [turns]);
  const agents = useMemo(() => deriveAgents(events), [events]);
  const skills = useMemo(() => deriveSkillUsage(events), [events]);
  const tree = useMemo(() => buildCallTree(events), [events]);
  const files = useMemo(() => aggregateFileChanges(fileChanges), [fileChanges]);

  if (
    models.length === 0 &&
    agents.length === 0 &&
    skills.length === 0 &&
    tree.length === 0 &&
    files.length === 0
  ) {
    return null;
  }

  return (
    <div className="grid min-w-0 grid-cols-[repeat(auto-fit,minmax(280px,1fr))] items-start gap-2.5">
      <ContextHogsCard sessionId={sessionId} />
      {models.length > 0 && (
        <UsageBlock label={t`models`} labelTone="text-ink-dim" barTone="bg-ink-dim/60" rows={models} />
      )}
      {agents.length > 0 && (
        <UsageBlock label={t`agents`} labelTone="text-ink-dim" barTone="bg-ink-dim/60" rows={agents} />
      )}
      {skills.length > 0 && (
        <UsageBlock label={t`skills`} labelTone="text-ink-dim" barTone="bg-ink-dim/60" rows={skills} />
      )}

      {tree.length > 0 && <CallTreeCard nodes={tree} />}

      {files.length > 0 && (
        <div className="rounded-xl border border-line bg-surface px-4 py-3.5">
          <div className="mb-1 flex items-baseline justify-between">
            <span className="font-mono text-[10.5px] tracking-[0.08em] text-ink-dim uppercase">
              <Trans>files changed</Trans>
            </span>
            <span className="font-mono text-[12px] font-bold text-ink">{files.length}</span>
          </div>
          {files.map((file) => (
            <button
              key={file.path}
              type="button"
              onClick={() => onShowDiffs(file.path)}
              className="flex w-full items-center gap-2 border-b border-line-soft py-1.5 text-left font-mono text-[11px] transition-colors last:border-b-0 hover:bg-surface2/50"
            >
              <span className="min-w-0 flex-1 truncate text-left text-ink-3 [direction:rtl]">
                {file.path}
              </span>
              <span className="text-green">+{file.additions}</span>
              <span className="text-red">−{file.deletions}</span>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
