// Unit tests for the plan-sessions column's slice rules (plan-sessions phase
// filter, phase 1). Pure logic, no DOM.
//
// The web app ships no committed test runner (CI is `npm run build` only, and
// the Go coverage gate excludes web/), so this suite is dev-only: run it with
//   npx vitest run src/lib/planSessionScope.test.ts
// (vitest is fetched on demand; it is intentionally NOT a committed dependency.)
// web/tsconfig.json EXCLUDES *.test.ts, so `npm run build` does NOT type-check
// this file — the runner surfaces type errors as failures instead.
//
// The fence these tests hold is the NEGATIVE half of the contract: no row
// without a real phase coordinate may ever be counted into, or rendered in, the
// phase slice. The controller row and the task_sessions links are exactly the
// rows an operator would most easily mistake for "this phase's transcript", and
// they are exactly the rows the data cannot place on a phase.

import { describe, expect, it } from 'vitest';
import type { LinkedSession, Session, SessionPlanGroup, SessionStatus } from '../api/types';
import { planSessionOrder, scopePlanSessions } from './planSessionScope';

/** Every plan session carries the SAME boilerplate prompt as its title — which
 * is why the column labels rows by role instead. */
const BOILERPLATE = 'You are the controller for an ENTIRE approved implementation plan…';

function makeSession(over: Partial<Session> = {}): Session {
  return {
    id: 0,
    projectId: 1,
    projectSlug: 'swarmery',
    projectName: 'swarmery',
    sessionUuid: 'uuid-0',
    model: 'opus',
    gitBranch: null,
    cwd: null,
    status: 'completed' as SessionStatus,
    startedAt: '2026-09-20T10:00:00Z',
    endedAt: null,
    title: BOILERPLATE,
    source: 'jsonl' as Session['source'],
    ...over,
  };
}

function plan(over: Partial<SessionPlanGroup> = {}): SessionPlanGroup {
  return {
    taskId: 7,
    title: 'plan-sessions phase filter',
    role: 'phase',
    phaseId: 1,
    phaseSeq: 1,
    phaseName: 'scope model',
    ...over,
  };
}

function makeLinked(over: Partial<LinkedSession> = {}): LinkedSession {
  return {
    sessionUuid: 'uuid-linked',
    linkSource: 'heuristic',
    confidence: 0.6,
    costUsd: null,
    startedAt: '2026-09-20T09:00:00Z',
    endedAt: null,
    ...over,
  };
}

/** The shape of a real plan page: one controller, two sessions under phase #1
 * (the phase run and a subagent of it), one under phase #2… except phase #2 is
 * deliberately absent here, so `PHASE_2_ID` is a phase with zero own sessions. */
const PHASE_1_ID = 11;
const PHASE_2_ID = 22;

const controller = makeSession({
  id: 1,
  sessionUuid: 'uuid-controller',
  startedAt: '2026-09-20T10:00:00Z',
  planGroup: plan({ role: 'controller', phaseId: null, phaseSeq: null, phaseName: null }),
});
const phase1Run = makeSession({
  id: 2,
  sessionUuid: 'uuid-p1-run',
  startedAt: '2026-09-20T10:05:00Z',
  planGroup: plan({ phaseId: PHASE_1_ID, phaseSeq: 1 }),
});
const phase1Sub = makeSession({
  id: 3,
  sessionUuid: 'uuid-p1-sub',
  startedAt: '2026-09-20T10:09:00Z',
  planGroup: plan({ phaseId: PHASE_1_ID, phaseSeq: 1 }),
});
const linkedRow = makeLinked({ sessionUuid: 'uuid-operator' });

const SESSIONS = [phase1Sub, controller, phase1Run]; // deliberately unsorted
const LINKED = [linkedRow];

// The contract is the ORDER the key induces, not the sentinel values it picks,
// so these assert relations — swapping -1 for -Infinity must stay green.
describe('planSessionOrder', () => {
  const key = (over: Partial<SessionPlanGroup>): number =>
    planSessionOrder(makeSession({ planGroup: plan(over) }));

  it('sorts the controller above the phases, and phases by seq', () => {
    expect(planSessionOrder(controller)).toBeLessThan(key({ phaseSeq: 1 }));
    expect(key({ phaseSeq: 1 })).toBeLessThan(key({ phaseSeq: 4 }));
  });

  it('parks a phase row with no seq at the end rather than above the controller', () => {
    expect(key({ phaseSeq: null })).toBeGreaterThan(key({ phaseSeq: 4 }));
  });

  it('treats a session with no plan group as plan-level, like the controller', () => {
    expect(planSessionOrder(makeSession())).toBe(planSessionOrder(controller));
  });
});

describe('scopePlanSessions — no phase open (timeline)', () => {
  const view = scopePlanSessions({
    sessions: SESSIONS,
    linked: LINKED,
    activePhaseId: null,
    picked: null,
  });

  it('shows the whole plan and reports no phase count', () => {
    expect(view.scope).toBe('all');
    expect(view.phaseCount).toBeNull();
    expect(view.fellBack).toBe(false);
    expect(view.sessions).toHaveLength(3);
    expect(view.linked).toEqual([linkedRow]);
    expect(view.totalCount).toBe(4);
  });

  it('orders the controller first, then the phases, ties by startedAt ascending', () => {
    expect(view.sessions.map((s) => s.sessionUuid)).toEqual([
      'uuid-controller',
      'uuid-p1-run',
      'uuid-p1-sub',
    ]);
  });

  it('does not mutate either caller array', () => {
    expect(SESSIONS.map((s) => s.sessionUuid)).toEqual(['uuid-p1-sub', 'uuid-controller', 'uuid-p1-run']);
    // The `linked` fence is the easier one to lose: a future `linked.sort(...)`
    // that skips the `.filter()` copy would reorder the page's own props array.
    const older = makeLinked({ sessionUuid: 'uuid-older', startedAt: '2026-09-19T09:00:00Z' });
    const callerLinked = [older, linkedRow];
    scopePlanSessions({ sessions: SESSIONS, linked: callerLinked, activePhaseId: null, picked: null });
    expect(callerLinked.map((l) => l.sessionUuid)).toEqual(['uuid-older', 'uuid-operator']);
  });
});

describe('scopePlanSessions — a phase with sessions of its own (SC-1, SC-6)', () => {
  const view = scopePlanSessions({
    sessions: SESSIONS,
    linked: LINKED,
    activePhaseId: PHASE_1_ID,
    picked: null,
  });

  it('narrows to exactly that phase’s two sessions', () => {
    expect(view.scope).toBe('phase');
    expect(view.phaseCount).toBe(2);
    expect(view.fellBack).toBe(false);
    expect(view.sessions.map((s) => s.sessionUuid)).toEqual(['uuid-p1-run', 'uuid-p1-sub']);
  });

  it('keeps the controller and every linked row out of the phase slice', () => {
    expect(view.sessions.some((s) => s.planGroup?.role === 'controller')).toBe(false);
    expect(view.linked).toEqual([]);
  });

  it('still reports the plan’s full size, so the header can say what is hidden', () => {
    expect(view.totalCount).toBe(4);
  });
});

describe('scopePlanSessions — a phase with no sessions of its own (SC-2)', () => {
  const view = scopePlanSessions({
    sessions: SESSIONS,
    linked: LINKED,
    activePhaseId: PHASE_2_ID,
    picked: null,
  });

  it('falls back to the whole plan instead of an empty column', () => {
    expect(view.scope).toBe('all');
    expect(view.fellBack).toBe(true);
    expect(view.phaseCount).toBe(0);
    expect(view.sessions).toHaveLength(3);
    expect(view.linked).toEqual([linkedRow]);
    expect(view.totalCount).toBe(4);
  });
});

describe('scopePlanSessions — the operator’s own pick wins', () => {
  it('“all” on a phase that HAS sessions is not a fallback', () => {
    const view = scopePlanSessions({
      sessions: SESSIONS,
      linked: LINKED,
      activePhaseId: PHASE_1_ID,
      picked: 'all',
    });
    expect(view.scope).toBe('all');
    expect(view.fellBack).toBe(false);
    expect(view.phaseCount).toBe(2);
    expect(view.sessions).toHaveLength(3);
    expect(view.linked).toEqual([linkedRow]);
  });

  it('“phase” on a phase with zero sessions yields an honest empty slice', () => {
    const view = scopePlanSessions({
      sessions: SESSIONS,
      linked: LINKED,
      activePhaseId: PHASE_2_ID,
      picked: 'phase',
    });
    expect(view.scope).toBe('phase');
    expect(view.fellBack).toBe(false);
    expect(view.phaseCount).toBe(0);
    expect(view.sessions).toEqual([]);
    expect(view.linked).toEqual([]);
    expect(view.totalCount).toBe(4);
  });

  it('a pick cannot narrow the timeline: with no phase open the slice stays “all”', () => {
    const view = scopePlanSessions({
      sessions: SESSIONS,
      linked: LINKED,
      activePhaseId: null,
      picked: 'phase',
    });
    expect(view.scope).toBe('all');
    expect(view.sessions).toHaveLength(3);
  });
});

describe('scopePlanSessions — dedup of the two sources (R2)', () => {
  it('a linked row the ?planTask= grouping already found appears once, in plan order', () => {
    const dupe = makeLinked({ sessionUuid: 'uuid-p1-run', linkSource: 'explicit', confidence: 1 });
    const view = scopePlanSessions({
      sessions: SESSIONS,
      linked: [dupe, linkedRow],
      activePhaseId: null,
      picked: null,
    });
    expect(view.linked.map((l) => l.sessionUuid)).toEqual(['uuid-operator']);
    expect(view.totalCount).toBe(4);
    expect(view.sessions.filter((s) => s.sessionUuid === 'uuid-p1-run')).toHaveLength(1);
  });

  it('orders linked-only rows newest first', () => {
    const older = makeLinked({ sessionUuid: 'uuid-older', startedAt: '2026-09-19T09:00:00Z' });
    const view = scopePlanSessions({
      sessions: SESSIONS,
      linked: [older, linkedRow],
      activePhaseId: null,
      picked: null,
    });
    expect(view.linked.map((l) => l.sessionUuid)).toEqual(['uuid-operator', 'uuid-older']);
  });
});

describe('scopePlanSessions — degenerate input', () => {
  it('an empty plan reports zero in both counts', () => {
    const view = scopePlanSessions({ sessions: [], linked: [], activePhaseId: null, picked: null });
    expect(view.totalCount).toBe(0);
    expect(view.phaseCount).toBeNull();
    expect(view.scope).toBe('all');
    expect(view.fellBack).toBe(false);
  });

  it('a plan whose only rows are linked shows them and falls back on any phase', () => {
    const view = scopePlanSessions({
      sessions: [],
      linked: LINKED,
      activePhaseId: PHASE_1_ID,
      picked: null,
    });
    expect(view.scope).toBe('all');
    expect(view.fellBack).toBe(true);
    expect(view.totalCount).toBe(1);
    expect(view.linked).toEqual([linkedRow]);
  });
});
