// runsModel's verdict, card naming and grouping (plan D-27…D-31): pure
// functions, so each case is read straight off its input — no rendering.

import { describe, expect, it } from 'vitest';
import type { Epic, EpicPhase, PhaseReopen, Session, SessionPlanGroup } from '../../api/types';
import {
  phaseAttemptGroups,
  planPhaseGroups,
  runCardTitle,
  runRole,
  runsVerdict,
} from './runsModel';

function group(over: Partial<SessionPlanGroup> = {}): SessionPlanGroup {
  return {
    taskId: 1,
    title: 'plan',
    role: 'phase',
    phaseId: 11,
    phaseSeq: 1,
    phaseName: 'Scope model',
    ...over,
  };
}

function session(over: Partial<Session> = {}): Session {
  return {
    id: 1,
    projectId: 3,
    projectSlug: 'swarmery',
    projectName: 'swarmery',
    sessionUuid: 'uuid-1',
    model: 'opus',
    gitBranch: null,
    cwd: null,
    status: 'completed',
    startedAt: '2026-11-01T09:00:00Z',
    endedAt: null,
    title: 'You are the executor for phase 1 of an approved implementation plan…',
    source: 'jsonl',
    planGroup: group(),
    ...over,
  };
}

function reopen(over: Partial<PhaseReopen> = {}): PhaseReopen {
  return {
    id: 1,
    reason: 'a regression slipped through',
    fixUrl: '',
    caughtBy: 'verifier',
    criteria: ['a criterion'],
    createdAt: '2026-11-02T09:00:00Z',
    ...over,
  };
}

const PHASE: Pick<EpicPhase, 'runOutcome' | 'checkboxesDone' | 'checkboxesTotal'> = {
  runOutcome: 'idle',
  checkboxesDone: 0,
  checkboxesTotal: 3,
};

describe('runsVerdict', () => {
  it('no runs: neutral, says why', () => {
    const v = runsVerdict(PHASE, 'pending', false);
    expect(v.tone).toBe('neutral');
    expect(v.lead).toBe('No runs yet');
  });

  it('a live run: neutral, running', () => {
    const v = runsVerdict(PHASE, 'in_progress', true);
    expect(v.tone).toBe('neutral');
    expect(v.lead).toBe('Running');
  });

  it('done: success, even with an idle runOutcome (every criterion ticked by hand)', () => {
    const v = runsVerdict(PHASE, 'done', false);
    expect(v.tone).toBe('success');
    expect(v.lead).toBe('Done');
  });

  it('a failed run: danger', () => {
    const v = runsVerdict({ ...PHASE, runOutcome: 'failed' }, 'blocked', false);
    expect(v.tone).toBe('danger');
  });

  it('a noop run: warn, not danger', () => {
    const v = runsVerdict({ ...PHASE, runOutcome: 'noop' }, 'unverified', false);
    expect(v.tone).toBe('warn');
  });

  it('in progress, no unresolved outcome: neutral, criteria count', () => {
    const v = runsVerdict({ ...PHASE, runOutcome: 'completed', checkboxesDone: 1, checkboxesTotal: 3 }, 'in_progress', false);
    expect(v.tone).toBe('neutral');
    expect(v.rest).toContain('1/3');
  });
});

describe('runRole / runCardTitle', () => {
  it('a phase-scoped session reads as the executor', () => {
    expect(runRole(session())).toBe('executor');
  });

  it('a controller-scoped session reads as the plan run', () => {
    expect(runRole(session({ planGroup: group({ role: 'controller', phaseId: null, phaseSeq: null }) }))).toBe(
      'controller',
    );
  });

  it('no planGroup at all reads as a generic session, never the title', () => {
    expect(runRole(session({ planGroup: null }))).toBe('session');
  });

  it('a phase-scoped session that is NOT the attempt’s identified executor reads as a generic session', () => {
    expect(runRole(session(), 'uuid-someone-else')).toBe('session');
  });

  it('the card title is the role, never the service-prompt title', () => {
    const title = runCardTitle(session());
    expect(title).not.toContain('You are the executor');
    expect(title).toBe('executor');
  });

  it('a short one-line `why` is appended to the role', () => {
    const title = runCardTitle(session({ why: 'wire the new settings toggle' }));
    expect(title).toBe('executor — wire the new settings toggle');
  });
});

describe('phaseAttemptGroups', () => {
  it('a single run is a single attempt group', () => {
    const groups = phaseAttemptGroups([session()], []);
    expect(groups).toHaveLength(1);
    expect(groups[0]?.label).toBe('Attempt 1');
    expect(groups[0]?.reason).toBeNull();
  });

  it('a reopened phase groups each run as its own attempt, oldest first', () => {
    const first = session({ id: 1, sessionUuid: 'uuid-1', startedAt: '2026-11-01T09:00:00Z', costUsd: 1.5 });
    const second = session({ id: 2, sessionUuid: 'uuid-2', startedAt: '2026-11-02T09:00:00Z', costUsd: 2.5 });
    const groups = phaseAttemptGroups([second, first], [reopen()]);
    expect(groups.map((g) => g.label)).toEqual(['Attempt 1', 'Attempt 2']);
    expect(groups[0]?.sessions[0]?.sessionUuid).toBe('uuid-1');
    expect(groups[1]?.reason).toBe('a regression slipped through');
    expect(groups[0]?.costUsd).toBe(1.5);
    expect(groups[1]?.costUsd).toBe(2.5);
  });

  it('no reopens: every session of the phase collapses into attempt 1, whatever the count', () => {
    const groups = phaseAttemptGroups(
      [session({ id: 1, sessionUuid: 'uuid-1' }), session({ id: 2, sessionUuid: 'uuid-2', startedAt: '2026-11-02T09:00:00Z' })],
      [],
    );
    expect(groups).toHaveLength(1);
    expect(groups[0]?.sessions).toHaveLength(2);
    expect(groups[0]?.reason).toBeNull();
  });

  it('a reopen only starts a new attempt from its own createdAt — sessions started earlier stay in the prior attempt', () => {
    const a = session({ id: 1, sessionUuid: 'uuid-a', startedAt: '2026-11-01T09:00:00Z' });
    const b = session({ id: 2, sessionUuid: 'uuid-b', startedAt: '2026-11-01T09:05:00Z' });
    const c = session({ id: 3, sessionUuid: 'uuid-c', startedAt: '2026-11-02T10:00:00Z' });
    const groups = phaseAttemptGroups([a, b, c], [reopen({ createdAt: '2026-11-02T09:00:00Z' })]);
    expect(groups).toHaveLength(2);
    expect(groups[0]?.sessions.map((s) => s.sessionUuid)).toEqual(['uuid-a', 'uuid-b']);
    expect(groups[1]?.sessions.map((s) => s.sessionUuid)).toEqual(['uuid-c']);
  });

  it('a bucket of several sessions names the executor from the phase’s own runSessionUuid, the rest read as generic sessions', () => {
    const exec = session({ id: 1, sessionUuid: 'uuid-exec', startedAt: '2026-11-01T09:00:00Z', costUsd: 1 });
    const verifier = session({ id: 2, sessionUuid: 'uuid-verifier', startedAt: '2026-11-01T09:05:00Z', costUsd: 0.5 });
    const groups = phaseAttemptGroups([exec, verifier], [], 'uuid-exec');
    expect(groups).toHaveLength(1);
    expect(groups[0]?.sessions).toHaveLength(2);
    expect(groups[0]?.costUsd).toBe(1.5);
    expect(groups[0]?.executorUuid).toBe('uuid-exec');
    expect(runRole(exec, groups[0]?.executorUuid)).toBe('executor');
    expect(runRole(verifier, groups[0]?.executorUuid)).toBe('session');
  });

  it('a bucket of several sessions with no matching runSessionUuid names no executor — every card reads as a generic session', () => {
    const a = session({ id: 1, sessionUuid: 'uuid-a', startedAt: '2026-11-01T09:00:00Z' });
    const b = session({ id: 2, sessionUuid: 'uuid-b', startedAt: '2026-11-01T09:05:00Z' });
    const groups = phaseAttemptGroups([a, b], [], null);
    expect(groups[0]?.executorUuid).toBeNull();
    expect(runRole(a, groups[0]?.executorUuid)).toBe('session');
    expect(runRole(b, groups[0]?.executorUuid)).toBe('session');
  });

  it('a lone session in a bucket is always the executor, even with no runSessionUuid to match', () => {
    const groups = phaseAttemptGroups([session()], [], null);
    expect(groups[0]?.executorUuid).toBe('uuid-1');
    expect(runRole(session(), groups[0]?.executorUuid)).toBe('executor');
  });
});

describe('planPhaseGroups', () => {
  const epic: Pick<Epic, 'phases'> = {
    phases: [
      { id: 11, seq: 1, name: 'Scope model' } as EpicPhase,
      { id: 22, seq: 2, name: 'UI wiring' } as EpicPhase,
    ],
  };

  it('the controller group comes first, ahead of every phase group', () => {
    const controllerSession = session({
      id: 9,
      sessionUuid: 'uuid-ctl',
      planGroup: group({ role: 'controller', phaseId: null, phaseSeq: null }),
    });
    const groups = planPhaseGroups(epic, [session(), controllerSession]);
    expect(groups[0]?.key).toBe('controller');
    expect(groups[0]?.label).toBe('Plan run');
    expect(groups[1]?.key).toBe('phase:11');
  });

  it('a phase with no sessions of its own has no group', () => {
    const groups = planPhaseGroups(epic, [session()]);
    expect(groups.map((g) => g.key)).toEqual(['phase:11']);
  });

  it('sums the cost of every session in a group', () => {
    const a = session({ id: 1, sessionUuid: 'uuid-a', costUsd: 1 });
    const b = session({ id: 2, sessionUuid: 'uuid-b', costUsd: 2.25 });
    const groups = planPhaseGroups(epic, [a, b]);
    expect(groups[0]?.costUsd).toBe(3.25);
  });

  it('a phase group with several sessions names the executor from that phase’s own runSessionUuid', () => {
    const withRunUuid: Pick<Epic, 'phases'> = {
      phases: [{ id: 11, seq: 1, name: 'Scope model', runSessionUuid: 'uuid-a' } as EpicPhase],
    };
    const a = session({ id: 1, sessionUuid: 'uuid-a' });
    const b = session({ id: 2, sessionUuid: 'uuid-b' });
    const groups = planPhaseGroups(withRunUuid, [a, b]);
    expect(groups[0]?.executorUuid).toBe('uuid-a');
  });
});
