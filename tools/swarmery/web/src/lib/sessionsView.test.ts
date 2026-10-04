// Unit tests for the Sessions page's arrangement rules (day-first timeline
// redesign). Pure logic, no DOM.
//
// Runs with the rest of the web suite: `npm test` (vitest, also a swarmery-ci
// step). On its own: `npx vitest run src/lib/sessionsView.test.ts`.
// web/tsconfig.json EXCLUDES *.test.ts, and vitest transpiles without type
// checking, so NOTHING type-checks this file — treat its types as documentation.

import { describe, expect, it } from 'vitest';
import type { Session, SessionPlanGroup, SessionStatus } from '../api/types';
import {
  buildDays,
  countRuns,
  countSessions,
  matchesQuery,
  planRowNotes,
  planRowTag,
  planRowTitle,
  runsOnly,
} from './sessionsView';

/** Every plan session carries the SAME boilerplate prompt as its title — that
 * is the whole reason the fan-out rows are labelled by role instead. */
const BOILERPLATE =
  'You are the controller for an ENTIRE approved implementation plan. Read the plan…';

let nextId = 1;

function makeSession(over: Partial<Session> = {}): Session {
  return {
    id: nextId++,
    projectId: 1,
    projectSlug: 'swarmery',
    projectName: 'swarmery',
    sessionUuid: `uuid-${String(nextId)}`,
    model: 'opus',
    gitBranch: null,
    cwd: null,
    status: 'completed' as SessionStatus,
    startedAt: '2026-08-02T10:00:00Z',
    endedAt: null,
    title: BOILERPLATE,
    source: 'jsonl' as Session['source'],
    ...over,
  };
}

function plan(over: Partial<SessionPlanGroup> = {}): SessionPlanGroup {
  return {
    taskId: 7,
    title: 'Sessions page redesign',
    role: 'phase',
    phaseId: 1,
    phaseSeq: 1,
    phaseName: 'day-first timeline',
    ...over,
  };
}

describe('matchesQuery', () => {
  it('matches the plan run title, which no other field carries', () => {
    const s = makeSession({ planGroup: plan() });
    expect(matchesQuery(s, 'redesign')).toBe(true);
    // Sanity: the match comes from planGroup.title, not the boilerplate title.
    expect(matchesQuery(makeSession({ planGroup: null }), 'redesign')).toBe(false);
  });

  it('matches the phase name', () => {
    const s = makeSession({ planGroup: plan({ phaseName: 'scope chip migration' }) });
    expect(matchesQuery(s, 'scope chip')).toBe(true);
  });

  it('still matches title / project / branch, and is case-insensitive', () => {
    const s = makeSession({ title: 'Fix the ingest', gitBranch: 'wip/Ingest' });
    expect(matchesQuery(s, 'ingest')).toBe(true);
    expect(matchesQuery(s, 'swarmery')).toBe(true);
    expect(matchesQuery(s, '')).toBe(true);
    expect(matchesQuery(s, 'nothing-here')).toBe(false);
  });
});

describe('buildDays — plan runs are one item inside their day', () => {
  it('emits one run item for a fan-out and positions it by its newest row', () => {
    const rows = [
      makeSession({ startedAt: '2026-08-02T12:00:00Z', planGroup: plan({ phaseSeq: 2 }) }),
      makeSession({ startedAt: '2026-08-02T11:00:00Z', planGroup: plan({ role: 'controller', phaseSeq: null, phaseName: null }) }),
      makeSession({ startedAt: '2026-08-02T09:00:00Z', planGroup: plan({ phaseSeq: 1 }) }),
    ];
    const loose = makeSession({ startedAt: '2026-08-02T10:00:00Z', planGroup: null });
    const days = buildDays([rows[0]!, rows[1]!, loose, rows[2]!], null);

    expect(days).toHaveLength(1);
    const items = days[0]!.items;
    expect(items.map((i) => i.kind)).toEqual(['run', 'session']);
    const run = items[0]!;
    if (run.kind !== 'run') throw new Error('expected a run item');
    expect(run.group.rows).toHaveLength(3);
    // Positioned by the NEWEST row (12:00), so it precedes the 10:00 session.
    expect(run.group.newestAt).toBe('2026-08-02T12:00:00Z');
    // In-run order: controller first, then phases by seq.
    expect(run.group.rows.map(planRowTag)).toEqual(['ctl', '#1', '#2']);
  });

  it('splits items across day buckets and drops empty days', () => {
    const days = buildDays(
      [
        makeSession({ startedAt: '2026-08-02T12:00:00Z' }),
        makeSession({ startedAt: '2026-07-31T12:00:00Z' }),
      ],
      null,
    );
    expect(days).toHaveLength(2);
    expect(days[0]!.items).toHaveLength(1);
  });
});

describe('buildDays — status filtering keeps a run card whole (any-row rule)', () => {
  const controller = makeSession({
    startedAt: '2026-08-02T12:00:00Z',
    status: 'completed',
    planGroup: plan({ role: 'controller', phaseSeq: null, phaseName: null }),
  });
  const okPhase = makeSession({
    startedAt: '2026-08-02T11:00:00Z',
    status: 'completed',
    planGroup: plan({ phaseSeq: 1 }),
  });
  const deadPhase = makeSession({
    startedAt: '2026-08-02T10:30:00Z',
    status: 'killed',
    planGroup: plan({ phaseSeq: 2 }),
  });
  const looseDone = makeSession({ startedAt: '2026-08-02T10:00:00Z', status: 'completed' });
  const all = [controller, okPhase, deadPhase, looseDone];

  it('includes the run when ANY row matches, and never splits the fan-out', () => {
    const days = buildDays(all, 'killed');
    expect(countRuns(days)).toBe(1);
    const run = days[0]!.items[0]!;
    if (run.kind !== 'run') throw new Error('expected a run item');
    // All three rows survive — the card is the unit, not the row.
    expect(run.group.rows).toHaveLength(3);
    // The non-matching loose session IS dropped.
    expect(countSessions(days)).toBe(3);
  });

  it('drops the run when no row matches', () => {
    const days = buildDays(all, 'active');
    expect(days).toHaveLength(0);
  });

  it('keeps everything when no status filter is set', () => {
    const days = buildDays(all, null);
    expect(countRuns(days)).toBe(1);
    expect(countSessions(days)).toBe(4);
  });
});

describe('buildDays — infinite scroll appends', () => {
  it('re-buckets a fan-out split across a page boundary into ONE run', () => {
    // Page 1 ends mid-run; page 2 carries the rest of the same taskId.
    const page1 = [
      makeSession({ startedAt: '2026-08-02T12:00:00Z', planGroup: plan({ phaseSeq: 3 }) }),
      makeSession({ startedAt: '2026-08-02T11:00:00Z', planGroup: plan({ phaseSeq: 2 }) }),
    ];
    const page2 = [
      makeSession({ startedAt: '2026-08-02T10:00:00Z', planGroup: plan({ phaseSeq: 1 }) }),
      makeSession({
        startedAt: '2026-08-02T09:00:00Z',
        planGroup: plan({ role: 'controller', phaseSeq: null, phaseName: null }),
      }),
    ];

    const firstPass = buildDays(page1, null);
    expect(countRuns(firstPass)).toBe(1);
    expect(countSessions(firstPass)).toBe(2);

    const appended = buildDays([...page1, ...page2], null);
    expect(countRuns(appended)).toBe(1); // merged by taskId, NOT duplicated
    const run = appended[0]!.items[0]!;
    if (run.kind !== 'run') throw new Error('expected a run item');
    expect(run.group.rows).toHaveLength(4);
    expect(run.group.rows.map(planRowTag)).toEqual(['ctl', '#1', '#2', '#3']);
    // The run keeps its timeline position from the newest row of page 1.
    expect(run.group.newestAt).toBe('2026-08-02T12:00:00Z');
  });

  it('keeps two DIFFERENT plan runs apart', () => {
    const days = buildDays(
      [
        makeSession({ startedAt: '2026-08-02T12:00:00Z', planGroup: plan({ taskId: 7 }) }),
        makeSession({ startedAt: '2026-08-02T11:00:00Z', planGroup: plan({ taskId: 9 }) }),
      ],
      null,
    );
    expect(countRuns(days)).toBe(2);
  });
});

describe('runsOnly (the `plan runs` audit view)', () => {
  it('keeps only run cards and drops days left empty', () => {
    const days = buildDays(
      [
        makeSession({ startedAt: '2026-08-02T12:00:00Z', planGroup: plan() }),
        makeSession({ startedAt: '2026-07-31T12:00:00Z', planGroup: null }),
      ],
      null,
    );
    expect(days).toHaveLength(2);
    const audit = runsOnly(days);
    expect(audit).toHaveLength(1);
    expect(countRuns(audit)).toBe(1);
  });
});

describe('fan-out row identity — never the boilerplate title', () => {
  it('labels the controller and phases by role, not by session.title', () => {
    const ctl = makeSession({ planGroup: plan({ role: 'controller', phaseSeq: null, phaseName: null }) });
    const ph = makeSession({ planGroup: plan({ phaseSeq: 5, phaseName: 'scope chip' }) });
    expect(planRowTag(ctl)).toBe('ctl');
    expect(planRowTitle(ctl)).toBe('plan controller');
    expect(planRowTag(ph)).toBe('#5');
    expect(planRowTitle(ph)).toBe('scope chip');
    // Neither label leaks the boilerplate prompt.
    expect(planRowTitle(ctl)).not.toContain('You are the controller');
    expect(planRowTitle(ph)).not.toContain('You are the controller');
  });

  it('falls back to #? / phase when the server sent no seq or name', () => {
    const bare = makeSession({ planGroup: plan({ phaseSeq: null, phaseName: null }) });
    expect(planRowTag(bare)).toBe('#?');
    expect(planRowTitle(bare)).toBe('phase');
  });
});

describe('planRowNotes', () => {
  it('explains the controller row and marks a retried phase', () => {
    const ctl = makeSession({ planGroup: plan({ role: 'controller', phaseSeq: null, phaseName: null }) });
    const first = makeSession({
      startedAt: '2026-08-02T09:00:00Z',
      status: 'killed',
      planGroup: plan({ phaseSeq: 5 }),
    });
    const retry = makeSession({
      startedAt: '2026-08-02T11:00:00Z',
      status: 'completed',
      planGroup: plan({ phaseSeq: 5 }),
    });
    const notes = planRowNotes([ctl, first, retry]);
    expect(notes.get(ctl.id)).toContain('dispatches phases in dependency order');
    expect(notes.get(first.id)).toBeUndefined(); // the first attempt needs no note
    expect(notes.get(retry.id)).toBe('second attempt — the previous run exited with an error');
    // The retry keeps its #seq.
    expect(planRowTag(retry)).toBe('#5');
  });

  it('leaves a single-attempt phase without a note', () => {
    const ph = makeSession({ planGroup: plan({ phaseSeq: 2 }) });
    expect(planRowNotes([ph]).size).toBe(0);
  });
});
