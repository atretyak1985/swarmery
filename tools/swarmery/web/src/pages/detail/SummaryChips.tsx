// At-a-glance strip for the session header: which subagents ran (blue, the
// subagent accent) and which skills were used (amber), derived client-side
// from the already-loaded events — no extra API calls. Renders nothing when
// the session has neither.

import { useMemo } from 'react';
import type { Event } from '../../api/types';
import { pickString, skillName } from '../../lib/payload';

interface AgentChip {
  name: string;
  count: number;
}

function deriveAgents(events: Event[]): AgentChip[] {
  const counts = new Map<string, number>();
  for (const event of events) {
    if (event.type !== 'subagent_start') continue;
    // Real daemon payloads carry `subagent_type`; stop rows / older fixtures
    // use `agentType`. Skip rows where no name can be recovered.
    const name = pickString(event.payload, ['subagent_type', 'agentType', 'agent_type', 'name']);
    if (name === null) continue;
    counts.set(name, (counts.get(name) ?? 0) + 1);
  }
  return [...counts.entries()].map(([name, count]) => ({ name, count }));
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
        <ChipGroup label="агенти" tone="text-blue/70">
          {agents.map(({ name, count }) => (
            <span
              key={name}
              className="rounded-full border border-blue/30 bg-blue/10 px-2 py-0.5 font-mono text-[11px] text-blue"
            >
              <span aria-hidden="true">⬡ </span>
              {name}
              {count > 1 ? ` ×${count}` : ''}
            </span>
          ))}
        </ChipGroup>
      )}
      {skills.length > 0 && (
        <ChipGroup label="скіли" tone="text-amber/70">
          {skills.map((name) => (
            <span
              key={name}
              className="rounded-full border border-amber/30 bg-amber/10 px-2 py-0.5 font-mono text-[11px] text-amber"
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
