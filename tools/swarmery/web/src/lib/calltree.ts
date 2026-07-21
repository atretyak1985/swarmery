// Builds the compact call tree for the session detail rail: who called what —
// main → skills → tools/subagents, recursively via parent_event_id. Tool calls
// are AGGREGATED per name inside each container (real sessions reach thousands
// of events; the per-call story lives in the Timeline tab). Skill containment
// is attribution, not strict causality: attributionSkill has no end marker in
// the transcript, so trailing tool calls may over-attach to a finished skill.

import type { Event } from '../api/types';
import {
  attributedSkill,
  pickNumber,
  pickString,
  skillName,
  subagentDescription,
  subagentName,
  toolArg,
} from './payload';

export interface ToolNode {
  kind: 'tool';
  name: string;
  count: number;
  errors: number;
  totalMs: number;
  /** Up to SAMPLE_LIMIT distinct one-line args (tooltip detail). */
  samples: string[];
}

export interface SkillNode {
  kind: 'skill';
  name: string;
  /** skill_use invocations folded into this node (same name, same container). */
  count: number;
  /** args of the first invocation that carried any (tooltip detail). */
  args: string | null;
  children: CallNode[];
}

export interface AgentNode {
  kind: 'agent';
  /** subagent_start event id — stable React key. */
  id: number;
  type: string;
  description: string | null;
  startedAt: string;
  running: boolean;
  failed: boolean;
  durationMs: number | null;
  /** totalTokens from the subagent_stop payload; null while running/unknown. */
  tokens: number | null;
  children: CallNode[];
}

/** More than GROUP_THRESHOLD same-type runs in one container fold into this. */
export interface AgentGroupNode {
  kind: 'agent-group';
  type: string;
  count: number;
  running: number;
  totalMs: number;
  tokens: number;
  runs: AgentNode[];
}

export type CallNode = ToolNode | SkillNode | AgentNode | AgentGroupNode;

/** Same UX threshold as SummaryChips' DESCRIBE_ALL_THRESHOLD. */
const GROUP_THRESHOLD = 4;

/** Distinct per-tool arg samples kept for the hover tooltip. */
const SAMPLE_LIMIT = 3;

function byTime(a: Event, b: Event): number {
  const t = a.ts.localeCompare(b.ts);
  return t !== 0 ? t : a.id - b.id;
}

function isFailure(status: Event['status']): boolean {
  return status === 'error' || status === 'denied' || status === 'timeout';
}

/** skill_use args (tooltip detail) — nested under input once closed, flat while open. */
function skillArgs(event: Event): string | null {
  const p = event.payload;
  const input =
    typeof p === 'object' && p !== null && !Array.isArray(p)
      ? (p as Record<string, unknown>)['input']
      : null;
  return pickString(input, ['args']) ?? pickString(p, ['args']);
}

/** Total tool invocations under `nodes`, recursively (tooltip stat). */
export function countToolCalls(nodes: CallNode[]): number {
  let n = 0;
  for (const node of nodes) {
    if (node.kind === 'tool') n += node.count;
    else if (node.kind === 'skill' || node.kind === 'agent') n += countToolCalls(node.children);
    else n += countToolCalls(node.runs);
  }
  return n;
}

/** Total subagent runs under `nodes`, recursively (tooltip stat). */
export function countAgentRuns(nodes: CallNode[]): number {
  let n = 0;
  for (const node of nodes) {
    if (node.kind === 'agent') n += 1 + countAgentRuns(node.children);
    else if (node.kind === 'skill') n += countAgentRuns(node.children);
    else if (node.kind === 'agent-group') n += node.count + countAgentRuns(node.runs.flatMap((r) => r.children));
  }
  return n;
}

/** One insertion point: an ordered node list + per-name tool aggregation. */
interface Container {
  list: CallNode[];
  tools: Map<string, ToolNode>;
}

function buildScope(scope: Event[], childrenOf: Map<number, Event[]>): CallNode[] {
  const root: Container = { list: [], tools: new Map() };
  // Skill containers by full name AND base name — attributionSkill may or may
  // not carry the plugin namespace the skill_use input used.
  const skillContainers = new Map<string, Container>();
  const skillNodes = new Map<string, SkillNode>();

  const findSkill = (name: string): Container | null =>
    skillContainers.get(name) ?? skillContainers.get(name.split(':').pop() ?? name) ?? null;

  const ensureSkill = (
    name: string,
    parent: Container,
  ): { container: Container; node: SkillNode } => {
    const existing = findSkill(name);
    const existingNode = skillNodes.get(name) ?? skillNodes.get(name.split(':').pop() ?? name);
    if (existing !== null && existingNode !== undefined) {
      return { container: existing, node: existingNode };
    }
    const node: SkillNode = { kind: 'skill', name, count: 0, args: null, children: [] };
    const container: Container = { list: node.children, tools: new Map() };
    parent.list.push(node);
    for (const key of new Set([name, name.split(':').pop() ?? name])) {
      if (!skillContainers.has(key)) {
        skillContainers.set(key, container);
        skillNodes.set(key, node);
      }
    }
    return { container, node };
  };

  // Where a node belongs: under the attributed skill when one is active (and
  // is not the skill itself), else at this scope's top level.
  const containerFor = (event: Event, selfSkill: string | null): Container => {
    const attr = attributedSkill(event);
    if (attr === null || attr === selfSkill) return root;
    return findSkill(attr) ?? ensureSkill(attr, root).container;
  };

  for (const event of scope) {
    if (event.type === 'skill_use') {
      const name = skillName(event) ?? 'skill';
      const parent = containerFor(event, name);
      const { node } = ensureSkill(name, parent);
      node.count += 1;
      node.args ??= skillArgs(event);
    } else if (event.type === 'subagent_start') {
      const kids = (childrenOf.get(event.id) ?? []).slice().sort(byTime);
      const stop = kids.find((e) => e.type === 'subagent_stop') ?? null;
      const node: AgentNode = {
        kind: 'agent',
        id: event.id,
        type: subagentName(event),
        description: subagentDescription(event),
        startedAt: event.ts,
        running: stop === null,
        failed: isFailure(event.status) || (stop !== null && isFailure(stop.status)),
        durationMs: stop?.durationMs ?? event.durationMs,
        tokens: stop !== null ? pickNumber(stop.payload, ['totalTokens']) : null,
        children: buildScope(
          kids.filter((e) => e.type !== 'subagent_stop'),
          childrenOf,
        ),
      };
      containerFor(event, null).list.push(node);
    } else if (event.type === 'tool_call' && event.toolName !== null) {
      const container = containerFor(event, null);
      let node = container.tools.get(event.toolName);
      if (node === undefined) {
        node = { kind: 'tool', name: event.toolName, count: 0, errors: 0, totalMs: 0, samples: [] };
        container.tools.set(event.toolName, node);
        container.list.push(node);
      }
      node.count += 1;
      if (isFailure(event.status)) node.errors += 1;
      node.totalMs += event.durationMs ?? 0;
      const arg = toolArg(event);
      if (arg !== null && node.samples.length < SAMPLE_LIMIT && !node.samples.includes(arg)) {
        node.samples.push(arg);
      }
    }
    // Other event types (prompts, permissions, test runs, …) are not calls.
  }

  for (const node of new Set(skillNodes.values())) {
    node.children = groupRuns(node.children);
  }
  return groupRuns(root.list);
}

/** Fold >GROUP_THRESHOLD same-type agent runs of one container into a group. */
function groupRuns(list: CallNode[]): CallNode[] {
  const perType = new Map<string, number>();
  for (const node of list) {
    if (node.kind === 'agent') perType.set(node.type, (perType.get(node.type) ?? 0) + 1);
  }
  const groups = new Map<string, AgentGroupNode>();
  const out: CallNode[] = [];
  for (const node of list) {
    if (node.kind !== 'agent' || (perType.get(node.type) ?? 0) <= GROUP_THRESHOLD) {
      out.push(node);
      continue;
    }
    let group = groups.get(node.type);
    if (group === undefined) {
      group = { kind: 'agent-group', type: node.type, count: 0, running: 0, totalMs: 0, tokens: 0, runs: [] };
      groups.set(node.type, group);
      out.push(group);
    }
    group.count += 1;
    if (node.running) group.running += 1;
    group.totalMs += node.durationMs ?? 0;
    group.tokens += node.tokens ?? 0;
    group.runs.push(node);
  }
  return out;
}

export function buildCallTree(events: Event[]): CallNode[] {
  const sorted = events.slice().sort(byTime);
  const ids = new Set(sorted.map((e) => e.id));
  const childrenOf = new Map<number, Event[]>();
  const roots: Event[] = [];
  for (const event of sorted) {
    if (event.parentEventId !== null && ids.has(event.parentEventId)) {
      const list = childrenOf.get(event.parentEventId);
      if (list) list.push(event);
      else childrenOf.set(event.parentEventId, [event]);
    } else {
      roots.push(event);
    }
  }
  return buildScope(roots, childrenOf);
}
