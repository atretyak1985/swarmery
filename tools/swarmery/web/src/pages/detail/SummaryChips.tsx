// At-a-glance strip for the session header: which subagents ran (blue, the
// subagent accent) and which skills were used (amber), derived client-side
// from the already-loaded events — no extra API calls. Renders nothing when
// the session has neither.

import { useMemo } from 'react';
import type { Event } from '../../api/types';
import { pickString, subagentDescription, skillName } from '../../lib/payload';

interface AgentChip {
  /** Chip label: agent type (aggregated) or task description (small sessions). */
  name: string;
  count: number;
  /** Native tooltip: the task descriptions hidden behind an aggregated chip. */
  title: string | null;
}

/** With this many agents or fewer, label chips by description (WHO did WHAT). */
const DESCRIBE_ALL_THRESHOLD = 4;

function deriveAgents(events: Event[]): AgentChip[] {
  const starts: { type: string; description: string | null }[] = [];
  for (const event of events) {
    if (event.type !== 'subagent_start') continue;
    // Real daemon payloads carry `subagent_type` (+ `description`); stop rows /
    // older fixtures use `agentType`. Skip rows where no name can be recovered.
    const type = pickString(event.payload, ['subagent_type', 'agentType', 'agent_type', 'name']);
    if (type === null) continue;
    starts.push({ type, description: subagentDescription(event) });
  }

  // Small sessions: one chip per agent, labeled by its task description.
  if (starts.length <= DESCRIBE_ALL_THRESHOLD) {
    return starts.map(({ type, description }) => ({
      name: description ?? type,
      count: 1,
      title: description !== null ? type : null,
    }));
  }

  // Larger sessions: keep chips compact by aggregating per type, but expose
  // the individual task descriptions via a native tooltip.
  const byType = new Map<string, { count: number; descriptions: string[] }>();
  for (const { type, description } of starts) {
    const entry = byType.get(type) ?? { count: 0, descriptions: [] };
    entry.count += 1;
    if (description !== null) entry.descriptions.push(description);
    byType.set(type, entry);
  }
  return [...byType.entries()].map(([name, { count, descriptions }]) => ({
    name,
    count,
    title: descriptions.length > 0 ? descriptions.join('\n') : null,
  }));
}

function deriveSkills(events: Event[]): string[] {
  const names = new Set<string>();
  for (const event of events) {
    if (event.type !== 'skill_use') continue;
    const name = skillName(event);
    if (name !== null) names.add(name);
  }
  return [...names];
}

function ChipGroup({
  label,
  tone,
  children,
}: {
  label: string;
  tone: string;
  children: JSX.Element[];
}): JSX.Element {
  return (
    <div className="flex flex-wrap items-baseline gap-1.5">
      <span className={`font-mono text-[10.5px] tracking-[0.1em] uppercase ${tone}`}>{label}</span>
      {children}
    </div>
  );
}

export function SummaryChips({ events }: { events: Event[] }): JSX.Element | null {
  const agents = useMemo(() => deriveAgents(events), [events]);
  const skills = useMemo(() => deriveSkills(events), [events]);

  if (agents.length === 0 && skills.length === 0) return null;

  return (
    <div className="mt-2.5 flex flex-col gap-1.5">
      {agents.length > 0 && (
        <ChipGroup label="agents" tone="text-blue/80">
          {agents.map(({ name, count, title }, i) => (
            <span
              key={`${name}-${String(i)}`}
              title={title ?? undefined}
              className="max-w-[360px] truncate rounded-md border border-blue/25 bg-blue-soft/60 px-2 py-0.5 font-mono text-[11px] text-blue"
            >
              <span aria-hidden="true">⬡ </span>
              {name}
              {count > 1 ? ` ×${count}` : ''}
            </span>
          ))}
        </ChipGroup>
      )}
      {skills.length > 0 && (
        <ChipGroup label="skills" tone="text-amber/90">
          {skills.map((name) => (
            <span
              key={name}
              className="rounded-md border border-amber/25 bg-amber-soft/60 px-2 py-0.5 font-mono text-[11px] text-amber"
            >
              <span aria-hidden="true">◈ </span>
              {name}
            </span>
          ))}
        </ChipGroup>
      )}
    </div>
  );
}
