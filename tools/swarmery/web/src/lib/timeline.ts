// Builds the timeline tree for a session: turns in order, events grouped per
// turn, subagent_start..subagent_stop spans folded into nested blocks via
// parent_event_id.

import type { Event, SessionDetail, Turn } from '../api/types';

export type TimelineNode =
  | { kind: 'event'; event: Event }
  | { kind: 'subagent'; start: Event; stop: Event | null; children: TimelineNode[] };

export interface TurnGroup {
  /** null = events whose turnId did not resolve to a turn in this session. */
  turn: Turn | null;
  nodes: TimelineNode[];
}

function byTime(a: Event, b: Event): number {
  const t = a.ts.localeCompare(b.ts);
  return t !== 0 ? t : a.id - b.id;
}

function buildNodes(events: Event[], childrenOf: Map<number, Event[]>): TimelineNode[] {
  const nodes: TimelineNode[] = [];
  for (const event of events) {
    if (event.type === 'subagent_stop') continue; // folded into its start block
    if (event.type === 'subagent_start') {
      const children = (childrenOf.get(event.id) ?? []).slice().sort(byTime);
      const stop = children.find((e) => e.type === 'subagent_stop') ?? null;
      nodes.push({
        kind: 'subagent',
        start: event,
        stop,
        children: buildNodes(children, childrenOf),
      });
    } else {
      nodes.push({ kind: 'event', event });
    }
  }
  return nodes;
}

/** Count of leaf events inside a subagent block (excluding the stop marker). */
export function countEvents(nodes: TimelineNode[]): number {
  let n = 0;
  for (const node of nodes) {
    if (node.kind === 'event') n += 1;
    else n += 1 + countEvents(node.children);
  }
  return n;
}

/** Build a timeline tree from an arbitrary subset of events (e.g. one chat
 * group's), folding subagent spans exactly like the full session build. */
export function buildSubtree(events: readonly Event[]): TimelineNode[] {
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
  return buildNodes(roots, childrenOf);
}

export function buildTimeline(detail: SessionDetail): TurnGroup[] {
  const events = detail.events.slice().sort(byTime);
  const ids = new Set(events.map((e) => e.id));

  // parent → children (only for parents that exist in this session).
  const childrenOf = new Map<number, Event[]>();
  const roots: Event[] = [];
  for (const event of events) {
    if (event.parentEventId !== null && ids.has(event.parentEventId)) {
      const list = childrenOf.get(event.parentEventId);
      if (list) list.push(event);
      else childrenOf.set(event.parentEventId, [event]);
    } else {
      roots.push(event);
    }
  }

  // Group root events by turn, keeping turn order (seq).
  const turns = detail.turns.slice().sort((a, b) => a.seq - b.seq);
  const turnById = new Map<number, Turn>(turns.map((t) => [t.id, t]));
  const rootsByTurn = new Map<number | null, Event[]>();
  for (const event of roots) {
    const key = event.turnId !== null && turnById.has(event.turnId) ? event.turnId : null;
    const list = rootsByTurn.get(key);
    if (list) list.push(event);
    else rootsByTurn.set(key, [event]);
  }

  const groups: TurnGroup[] = [];
  for (const turn of turns) {
    const turnRoots = rootsByTurn.get(turn.id);
    if (!turnRoots || turnRoots.length === 0) continue;
    groups.push({ turn, nodes: buildNodes(turnRoots, childrenOf) });
  }
  const orphans = rootsByTurn.get(null);
  if (orphans && orphans.length > 0) {
    groups.push({ turn: null, nodes: buildNodes(orphans, childrenOf) });
  }
  return groups;
}
