// Unit tests for the board's pure presentation model: the card-label helpers
// (0049 UI: badges + filter), the card readout selectors (board redesign v2
// phase 1: source line, attention signal, stale), and the inbox amnesty.
// Pure logic, no DOM.
//
// The web app ships no committed test runner (CI is `npm run build` only, and
// the Go coverage gate excludes web/), so this suite is dev-only: run it with
//   npx vitest run src/workspace/boardModel.test.ts
// (vitest is fetched on demand; it is intentionally NOT a committed dependency).
// web/tsconfig.json EXCLUDES *.test.ts, so `npm run build` does NOT type-check
// this file — check it explicitly with
//   npx tsc --noEmit --project tsconfig.json src/workspace/boardModel.test.ts
// or trust the runner, which type-errors as runtime failures.

import { describe, expect, it } from 'vitest';
import type { BoardColumn, BoardTask, BoardTaskSource } from '../api/types';
import {
  ageLabel,
  amnestyBefore,
  amnestyCandidates,
  attentionSignal,
  BOARD_LANES,
  compareDispatchOrder,
  DEP_BLOCK_PREFIX,
  idleSince,
  inboxTtlMs,
  isStale,
  labelColor,
  labelFilterOptions,
  LANE_TITLES,
  laneOf,
  matchesLabelFilter,
  sourceLine,
  splitLanes,
  staleLabel,
  STALE_WARN_DAYS,
  uniqueLabels,
  visibleLabels,
} from './boardModel';

let nextId = 1;

function makeTask(over: Partial<BoardTask> = {}): BoardTask {
  return {
    id: nextId++,
    externalId: `T-${String(nextId)}`,
    projectId: 1,
    projectSlug: 'swarmery',
    title: 'a task',
    prompt: 'a task',
    priority: 'normal',
    status: 'queued',
    boardColumn: 'triage',
    paused: false,
    userPaused: false,
    dependencies: [],
    model: null,
    playbook: null,
    fileScope: [],
    labels: [],
    branch: null,
    worktreePath: null,
    startPoint: null,
    dispatchError: null,
    retryCount: 0,
    verifyRetryCount: 0,
    verifyVerdict: null,
    verifyDetail: null,
    agent: null,
    origin: 'manual',
    originSessionId: null,
    source: null,
    staleAfter: null,
    dispatchedPrompt: null,
    planExternalId: null,
    resultNote: null,
    columnMovedAt: null,
    createdAt: '2026-08-01T00:00:00Z',
    ...over,
  };
}

/** A capture source with the fields a test does not care about defaulted. */
function makeSource(over: Partial<BoardTaskSource> = {}): BoardTaskSource {
  return { sessionId: 1867, turnUuid: 'turn-uuid', quote: null, files: [], ...over };
}

describe('visibleLabels', () => {
  it('shows everything and no overflow at or under the cap', () => {
    expect(visibleLabels([])).toEqual({ shown: [], overflow: 0 });
    expect(visibleLabels(['a', 'b', 'c'])).toEqual({ shown: ['a', 'b', 'c'], overflow: 0 });
  });

  it('caps at 3 and rolls the rest into overflow, preserving order', () => {
    const { shown, overflow } = visibleLabels(['a', 'b', 'c', 'd', 'e']);
    expect(shown).toEqual(['a', 'b', 'c']);
    expect(overflow).toBe(2);
  });
});

describe('labelColor', () => {
  it('is a pure function of the label text: same input, same output, every call', () => {
    const calls = Array.from({ length: 5 }, () => labelColor('jira-ticket'));
    expect(new Set(calls).size).toBe(1);
  });

  it('returns "H S% L%" components with H inside the reserved-safe bands', () => {
    const samples = ['jira-ticket', 'bug', 'bud', 'ui', 'needs-design', 'flaky', 'p1', 'p2', ''];
    for (const label of samples) {
      const [h, s, l] = labelColor(label).split(' ');
      const hue = Number(h);
      expect(Number.isInteger(hue)).toBe(true);
      // Never in the reserved red band (VerdictBadge fail) or green band (pass).
      const inRedBand = hue < 20 || hue >= 345;
      const inGreenBand = hue >= 90 && hue < 175;
      expect(inRedBand).toBe(false);
      expect(inGreenBand).toBe(false);
      expect(s).toBe('58%');
      expect(l).toBe('62%');
    }
  });

  it('different labels are not forced onto the same hue (spot check, not a full collision proof)', () => {
    const hues = new Set(['jira-ticket', 'bug', 'ui', 'flaky', 'p1'].map((l) => labelColor(l).split(' ')[0]));
    expect(hues.size).toBeGreaterThan(1);
  });
});

describe('uniqueLabels', () => {
  it('dedupes across tasks and sorts the result', () => {
    const tasks = [
      makeTask({ labels: ['jira-ticket', 'ui'] }),
      makeTask({ labels: ['ui', 'flaky'] }),
      makeTask({ labels: [] }),
    ];
    expect(uniqueLabels(tasks)).toEqual(['flaky', 'jira-ticket', 'ui']);
  });

  it('returns an empty list when no task carries a label', () => {
    expect(uniqueLabels([makeTask(), makeTask()])).toEqual([]);
  });
});

describe('matchesLabelFilter', () => {
  const withLabel = makeTask({ labels: ['jira-ticket'] });
  const withoutLabel = makeTask({ labels: [] });

  it('matches everything when the filter is null or empty', () => {
    expect(matchesLabelFilter(withLabel, null)).toBe(true);
    expect(matchesLabelFilter(withoutLabel, null)).toBe(true);
    expect(matchesLabelFilter(withoutLabel, '')).toBe(true);
  });

  it('matches only tasks carrying the exact label', () => {
    expect(matchesLabelFilter(withLabel, 'jira-ticket')).toBe(true);
    expect(matchesLabelFilter(withoutLabel, 'jira-ticket')).toBe(false);
    expect(matchesLabelFilter(withLabel, 'other')).toBe(false);
  });
});

describe('labelFilterOptions', () => {
  it('lists each label with how many tasks carry it, sorted by label', () => {
    const tasks = [
      makeTask({ labels: ['jira-ticket', 'ui'] }),
      makeTask({ labels: ['ui', 'flaky'] }),
      makeTask({ labels: [] }),
    ];
    expect(labelFilterOptions(tasks, null)).toEqual([
      { label: 'flaky', count: 1 },
      { label: 'jira-ticket', count: 1 },
      { label: 'ui', count: 2 },
    ]);
  });

  it('keeps a stale filter in the list with count 0 instead of dropping it', () => {
    // The filtered label no longer sits on any loaded task -- a bookmarked
    // URL, or the last card carrying it lost the label. The dropdown must
    // still offer this exact value so <select value={filter}> always has a
    // matching <option> and never silently disagrees with the applied filter.
    const tasks = [makeTask({ labels: ['ui'] })];
    expect(labelFilterOptions(tasks, 'gone')).toEqual([
      { label: 'gone', count: 0 },
      { label: 'ui', count: 1 },
    ]);
  });

  it('does not duplicate the filter when it is still a live label', () => {
    const tasks = [makeTask({ labels: ['ui'] })];
    expect(labelFilterOptions(tasks, 'ui')).toEqual([{ label: 'ui', count: 1 }]);
  });

  it('ignores a null or empty filter -- same as no filter applied', () => {
    const tasks = [makeTask({ labels: ['ui'] })];
    expect(labelFilterOptions(tasks, null)).toEqual([{ label: 'ui', count: 1 }]);
    expect(labelFilterOptions(tasks, '')).toEqual([{ label: 'ui', count: 1 }]);
  });
});

// --- card readout: source line ------------------------------------------------

describe('sourceLine', () => {
  it('names the session, links to it, and hovers the captured quote', () => {
    const line = sourceLine(
      makeTask({
        origin: 'session',
        originSessionId: 1867,
        source: makeSource({ sessionId: 1867, quote: 'add waypoint editing to the board' }),
      }),
    );
    expect(line.text).toBe('from session #1867');
    expect(line.target).toEqual({ kind: 'session', sessionId: 1867 });
    expect(line.tip).toBe('add waypoint editing to the board');
  });

  it('says an llm card was suggested, not captured', () => {
    const line = sourceLine(
      makeTask({ origin: 'llm', originSessionId: 42, source: makeSource({ sessionId: 42 }) }),
    );
    expect(line.text).toBe('suggested from session #42');
    expect(line.target).toEqual({ kind: 'session', sessionId: 42 });
  });

  it('falls back to originSessionId when the 0066 source object is absent', () => {
    // Rows captured before the provenance columns: origin + origin_session_id
    // only. The line must still link, and say where the card came from.
    const line = sourceLine(makeTask({ origin: 'session', originSessionId: 7, source: null }));
    expect(line.text).toBe('from session #7');
    expect(line.target).toEqual({ kind: 'session', sessionId: 7 });
    expect(line.tip).toBe('captured from session #7');
  });

  it('reads a fix card as the repair of the card it names', () => {
    // A fix card's own external_id IS the id of the card it repairs
    // (verify/service.go createFixTask), which is what makes this derivable.
    const line = sourceLine(makeTask({ origin: 'verify-fix', externalId: 'T-12' }));
    expect(line.text).toBe('fix for T-12');
    expect(line.target).toBeNull();
    expect(line.tip).toBe('spawned by verification to repair T-12');
  });

  it('links a plan card to its project plan list', () => {
    const line = sourceLine(
      makeTask({ origin: 'manual', planExternalId: '2026-07-18-plan-doc-lifecycle', projectSlug: 'swarmery' }),
    );
    expect(line.text).toBe('plan 2026-07-18-plan-doc-lifecycle');
    expect(line.target).toEqual({ kind: 'plans', slug: 'swarmery' });
  });

  it('drops the plan link, not the prose, when the card has no project slug', () => {
    const line = sourceLine(makeTask({ planExternalId: 'plan-x', projectSlug: null }));
    expect(line.text).toBe('plan plan-x');
    expect(line.target).toBeNull();
  });

  it('prefers the session over the plan a dispatched card materialized', () => {
    // Every dispatched card gets a micro-plan, so most running cards carry both.
    // Provenance is where the card CAME FROM; the plan is where its outcome was
    // recorded, and lives in the modal.
    const line = sourceLine(
      makeTask({ origin: 'session', originSessionId: 3, source: makeSource({ sessionId: 3 }), planExternalId: 'p-1' }),
    );
    expect(line.target).toEqual({ kind: 'session', sessionId: 3 });
  });

  it('says "added by hand" for a plain manual card', () => {
    const line = sourceLine(makeTask({ origin: 'manual' }));
    expect(line.text).toBe('added by hand');
    expect(line.target).toBeNull();
  });

  it('still names a source for a captured card whose session is gone', () => {
    expect(sourceLine(makeTask({ origin: 'session', originSessionId: null })).text).toBe('from a session');
    expect(sourceLine(makeTask({ origin: 'llm', originSessionId: null })).text).toBe('suggested');
  });

  // Totality fence, inherited from the ORIGIN_BADGE Record this line replaced:
  // that map crashed the whole board render on the first 'verify-fix' card
  // because a missing key was destructured. Every origin must produce a line.
  it('produces a non-empty line for every origin in the union', () => {
    for (const origin of ['manual', 'session', 'llm', 'verify-fix'] as const) {
      const line = sourceLine(makeTask({ origin, originSessionId: origin === 'manual' ? null : 99 }));
      expect(line.text.length).toBeGreaterThan(0);
    }
  });
});

describe('ageLabel', () => {
  const now = Date.UTC(2026, 8, 4, 12, 0, 0);

  it('counts whole days since the card appeared', () => {
    expect(ageLabel(makeTask({ createdAt: '2026-08-23T12:00:00.000Z' }), now)).toBe('12d');
  });

  it('says "today" under a day rather than "0d"', () => {
    expect(ageLabel(makeTask({ createdAt: '2026-09-04T01:00:00.000Z' }), now)).toBe('today');
  });

  it('returns null on an unparseable stamp instead of "NaNd"', () => {
    expect(ageLabel(makeTask({ createdAt: 'not a date' }), now)).toBeNull();
  });
});

// --- card readout: attention signal -------------------------------------------

describe('attentionSignal', () => {
  it('reports a failed verdict with its detail', () => {
    const s = attentionSignal(makeTask({ verifyVerdict: 'fail', verifyDetail: 'typecheck: 3 errors' }));
    expect(s).toEqual({ text: 'verdict FAIL: typecheck: 3 errors', tone: 'bad', tip: 'typecheck: 3 errors' });
  });

  it('reports a failed verdict with no detail as just the verdict', () => {
    const s = attentionSignal(makeTask({ verifyVerdict: 'fail', verifyDetail: null }));
    expect(s).toEqual({ text: 'verdict FAIL', tone: 'bad', tip: null });
  });

  it('clips a long detail into the line and keeps the whole of it in the tip', () => {
    const detail = 'x'.repeat(200);
    const s = attentionSignal(makeTask({ verifyVerdict: 'fail', verifyDetail: detail }));
    expect(s?.text.endsWith('…')).toBe(true);
    expect(s?.text.length).toBeLessThan(120);
    expect(s?.tip).toBe(detail);
  });

  // The criterion: exactly one signal, in this order. Peeled one condition at a
  // time off a card that satisfies all four.
  it('returns exactly one signal, FAIL > dispatchError > blocked > in_review', () => {
    const all = {
      boardColumn: 'in_review' as BoardColumn,
      dispatchError: 'runner exited 1',
      verifyVerdict: 'fail',
      verifyDetail: 'build broke',
    };
    expect(attentionSignal(makeTask(all))?.text).toBe('verdict FAIL: build broke');

    const noVerdict = makeTask({ ...all, verifyVerdict: null, verifyDetail: null });
    expect(attentionSignal(noVerdict)).toEqual({
      text: 'dispatch error: runner exited 1',
      tone: 'bad',
      tip: 'runner exited 1',
    });

    const depBlocked = makeTask({
      ...all,
      verifyVerdict: null,
      verifyDetail: null,
      dispatchError: `${DEP_BLOCK_PREFIX}T-14: still in_progress`,
    });
    expect(attentionSignal(depBlocked)).toEqual({
      text: 'blocked by T-14: still in_progress',
      tone: 'warn',
      tip: `${DEP_BLOCK_PREFIX}T-14: still in_progress`,
    });

    const reviewOnly = makeTask({ boardColumn: 'in_review' });
    expect(attentionSignal(reviewOnly)).toEqual({ text: 'waiting for review', tone: 'info', tip: null });
  });

  it('separates a dependency block from a real failure — same column, different meaning', () => {
    const blocked = attentionSignal(makeTask({ dispatchError: `${DEP_BLOCK_PREFIX}T-9: unknown id` }));
    const broken = attentionSignal(makeTask({ dispatchError: 'worktree missing' }));
    expect(blocked?.tone).toBe('warn');
    expect(broken?.tone).toBe('bad');
    expect(blocked?.text.startsWith('blocked by')).toBe(true);
    expect(broken?.text.startsWith('dispatch error')).toBe(true);
  });

  it('reads a passing verdict as no signal at all', () => {
    expect(attentionSignal(makeTask({ verifyVerdict: 'pass', verifyDetail: 'all green' }))).toBeNull();
    expect(attentionSignal(makeTask({ verifyVerdict: 'inconclusive' }))).toBeNull();
  });

  it('folds verdict case — the wire is lowercase, a mixed-case row must not lose the signal', () => {
    expect(attentionSignal(makeTask({ verifyVerdict: 'FAIL' }))?.tone).toBe('bad');
  });

  it('is silent on a card that wants nothing', () => {
    expect(attentionSignal(makeTask({ boardColumn: 'todo' }))).toBeNull();
    expect(attentionSignal(makeTask({ boardColumn: 'triage', dispatchError: '' }))).toBeNull();
  });
});

// --- card readout: stale ------------------------------------------------------

describe('isStale', () => {
  const now = Date.UTC(2026, 8, 4, 12, 0, 0);
  const inDays = (n: number): string => new Date(now + n * 86_400_000).toISOString();

  // The one the plan review caught: the sweeper only touches non-manual cards in
  // triage with no worktree, so null is the COMMON case — manual, running and
  // review cards all carry it. Read naively (`new Date(null) < now`) it means
  // 1970, and half the board renders as about to be archived.
  it('is false when staleAfter is null — "never expires", not "expired in 1970"', () => {
    expect(isStale(makeTask({ staleAfter: null }), now)).toBe(false);
  });

  it('is false on an empty string and on an unparseable stamp', () => {
    expect(isStale(makeTask({ staleAfter: '' }), now)).toBe(false);
    expect(isStale(makeTask({ staleAfter: 'soon' }), now)).toBe(false);
  });

  it('is true once the date has passed', () => {
    expect(isStale(makeTask({ staleAfter: inDays(-1) }), now)).toBe(true);
  });

  it('is true inside the warning window and false outside it', () => {
    expect(isStale(makeTask({ staleAfter: inDays(2) }), now)).toBe(true);
    expect(isStale(makeTask({ staleAfter: inDays(STALE_WARN_DAYS) }), now)).toBe(false);
    expect(isStale(makeTask({ staleAfter: inDays(11) }), now)).toBe(false);
  });
});

describe('staleLabel', () => {
  const now = Date.UTC(2026, 8, 4, 12, 0, 0);
  const inDays = (n: number): string => new Date(now + n * 86_400_000).toISOString();

  it('counts the days left', () => {
    expect(staleLabel(makeTask({ staleAfter: inDays(2) }), now)).toBe('archived in 2d');
    expect(staleLabel(makeTask({ staleAfter: inDays(0.5) }), now)).toBe('archived in 1d');
  });

  it('does not render a negative countdown for a date already passed', () => {
    expect(staleLabel(makeTask({ staleAfter: inDays(-4) }), now)).toBe('archived at the next sweep');
  });

  it('is null exactly when the card is not stale, so caption and dimming agree', () => {
    for (const staleAfter of [null, '', inDays(11)]) {
      const task = makeTask({ staleAfter });
      expect(staleLabel(task, now)).toBeNull();
      expect(isStale(task, now)).toBe(false);
    }
  });
});

// --- inbox amnesty ------------------------------------------------------------

describe('idleSince', () => {
  it('falls back to createdAt — capture never writes columnMovedAt', () => {
    expect(idleSince(makeTask({ columnMovedAt: null, createdAt: '2026-01-01T00:00:00.000Z' }))).toBe(
      '2026-01-01T00:00:00.000Z',
    );
  });

  it('prefers columnMovedAt when the card was actually moved', () => {
    expect(
      idleSince(
        makeTask({ columnMovedAt: '2026-08-01T00:00:00.000Z', createdAt: '2026-01-01T00:00:00.000Z' }),
      ),
    ).toBe('2026-08-01T00:00:00.000Z');
  });
});

const TTL_MS = 14 * 86_400_000;

describe('inboxTtlMs', () => {
  // The sweeper's TTL is recoverable because staleAfter IS idleSince + TTL. That
  // is what let AMNESTY_AGE_DAYS = 7 go: the client no longer carries a number
  // that can disagree with the daemon's SWARMERY_INBOX_TTL (which was 14).
  it('recovers the TTL from a dated card', () => {
    const created = '2026-08-01T00:00:00.000Z';
    const task = makeTask({
      origin: 'session',
      createdAt: created,
      staleAfter: new Date(Date.parse(created) + TTL_MS).toISOString(),
    });
    expect(inboxTtlMs([task])).toBe(TTL_MS);
  });

  it('measures from columnMovedAt when the card was moved — the server does', () => {
    const moved = '2026-08-20T00:00:00.000Z';
    const task = makeTask({
      origin: 'session',
      createdAt: '2026-01-01T00:00:00.000Z',
      columnMovedAt: moved,
      staleAfter: new Date(Date.parse(moved) + TTL_MS).toISOString(),
    });
    expect(inboxTtlMs([task])).toBe(TTL_MS);
  });

  it('is null when no card is dated — the sweep is off, or nothing is eligible', () => {
    expect(inboxTtlMs([])).toBeNull();
    expect(inboxTtlMs([makeTask({ staleAfter: null }), makeTask({ staleAfter: '' })])).toBeNull();
  });

  it('skips undated cards to find a dated one', () => {
    const created = '2026-08-01T00:00:00.000Z';
    const dated = makeTask({
      origin: 'session',
      createdAt: created,
      staleAfter: new Date(Date.parse(created) + TTL_MS).toISOString(),
    });
    expect(inboxTtlMs([makeTask(), makeTask({ staleAfter: null }), dated])).toBe(TTL_MS);
  });
});

describe('amnestyBefore', () => {
  it('renders now-minus-TTL in the millisecond-Z shape the server stores', () => {
    // The server re-renders the cutoff in this exact format before comparing
    // lexically, so an un-normalized instant would shift the matched set.
    const now = Date.UTC(2026, 8, 4, 12, 0, 0);
    const created = '2026-08-01T00:00:00.000Z';
    const task = makeTask({
      origin: 'session',
      createdAt: created,
      staleAfter: new Date(Date.parse(created) + TTL_MS).toISOString(),
    });
    expect(amnestyBefore([task], now)).toBe('2026-08-21T12:00:00.000Z');
  });

  it('is null when nothing on the board is dated', () => {
    expect(amnestyBefore([makeTask()], Date.now())).toBeNull();
  });
});

describe('amnestyCandidates', () => {
  const now = Date.UTC(2026, 8, 4, 12, 0, 0);
  const at = (days: number): string => new Date(now + days * 86_400_000).toISOString();

  it('counts the cards whose server-published archive date has passed', () => {
    const tasks = [
      makeTask({ origin: 'session', staleAfter: at(-3) }),
      makeTask({ origin: 'llm', staleAfter: at(-1) }),
    ];
    expect(amnestyCandidates(tasks, now)).toBe(2);
  });

  it('ignores cards that have not reached their date yet', () => {
    expect(amnestyCandidates([makeTask({ origin: 'session', staleAfter: at(2) })], now)).toBe(0);
  });

  // The server's conjuncts (source='queue', triage, non-manual origin, no
  // worktree) are already baked into whether staleAfter is set at all — so a
  // null date is the whole exclusion list, in one field, with no second copy
  // here to drift from taskcap.StaleInboxWhere.
  it('excludes every card the sweeper cannot touch, by their null date alone', () => {
    const excluded = [
      makeTask({ origin: 'manual', boardColumn: 'triage', staleAfter: null }),
      makeTask({ origin: 'session', boardColumn: 'todo', staleAfter: null }),
      makeTask({ origin: 'session', boardColumn: 'triage', worktreePath: '/tmp/wt', staleAfter: null }),
    ];
    expect(amnestyCandidates(excluded, now)).toBe(0);
    expect(amnestyCandidates([...excluded, makeTask({ origin: 'session', staleAfter: at(-1) })], now)).toBe(1);
  });

  it('is empty on an empty board', () => {
    expect(amnestyCandidates([], now)).toBe(0);
  });
});

// --- lanes (board redesign phase 4) -------------------------------------------

describe('laneOf', () => {
  it('collapses every live column into one of the three lanes', () => {
    expect(laneOf('triage')).toBe('inbox');
    expect(laneOf('todo')).toBe('working');
    expect(laneOf('in_progress')).toBe('working');
    expect(laneOf('in_review')).toBe('review');
  });

  it('gives the two history columns no lane at all', () => {
    // Not "some lane you should ignore" — null, so a caller that forgets to
    // handle history cannot silently render them into Inbox.
    expect(laneOf('done')).toBeNull();
    expect(laneOf('archived')).toBeNull();
  });

  it('covers the whole BoardColumn enum — a new column cannot be forgotten', () => {
    const all: BoardColumn[] = ['triage', 'todo', 'in_progress', 'in_review', 'done', 'archived'];
    for (const c of all) {
      const lane = laneOf(c);
      expect(lane === null || BOARD_LANES.includes(lane)).toBe(true);
    }
  });
});

describe('BOARD_LANES / LANE_TITLES', () => {
  it('renders exactly three lanes, left to right', () => {
    expect(BOARD_LANES).toEqual(['inbox', 'working', 'review']);
  });

  it('titles every lane it renders', () => {
    for (const lane of BOARD_LANES) expect(LANE_TITLES[lane]).toBeTruthy();
  });
});

describe('compareDispatchOrder', () => {
  // The contract under test is dispatch/service.go candidates(): priority asc →
  // created_at asc → id asc. If these ever disagree, the Queued group is lying
  // about which card runs next, which is the only reason the group exists.
  const early = '2026-01-01T00:00:00.000Z';
  const late = '2026-08-01T00:00:00.000Z';

  it('ranks by priority first, even against an older card', () => {
    const urgentNew = makeTask({ priority: 'urgent', createdAt: late });
    const lowOld = makeTask({ priority: 'low', createdAt: early });
    expect(compareDispatchOrder(urgentNew, lowOld)).toBeLessThan(0);
    expect(compareDispatchOrder(lowOld, urgentNew)).toBeGreaterThan(0);
  });

  it('breaks a priority tie with the older createdAt', () => {
    const older = makeTask({ priority: 'normal', createdAt: early });
    const newer = makeTask({ priority: 'normal', createdAt: late });
    expect(compareDispatchOrder(older, newer)).toBeLessThan(0);
  });

  it('breaks a full tie with the lower id, so the order is total and stable', () => {
    const first = makeTask({ id: 7, priority: 'high', createdAt: early });
    const second = makeTask({ id: 9, priority: 'high', createdAt: early });
    expect(compareDispatchOrder(first, second)).toBeLessThan(0);
    expect(compareDispatchOrder(second, first)).toBeGreaterThan(0);
    expect(compareDispatchOrder(first, first)).toBe(0);
  });

  it('sorts a mixed queue into the exact order the dispatcher would pick from', () => {
    const queue = [
      makeTask({ id: 1, priority: 'normal', createdAt: late }),
      makeTask({ id: 2, priority: 'urgent', createdAt: late }),
      makeTask({ id: 3, priority: 'normal', createdAt: early }),
      makeTask({ id: 4, priority: 'low', createdAt: early }),
      makeTask({ id: 5, priority: 'high', createdAt: late }),
    ];
    expect([...queue].sort(compareDispatchOrder).map((t) => t.id)).toEqual([2, 5, 3, 1, 4]);
  });
});

describe('splitLanes', () => {
  it('routes each column to the group that renders it', () => {
    const lanes = splitLanes([
      makeTask({ id: 1, boardColumn: 'triage' }),
      makeTask({ id: 2, boardColumn: 'todo' }),
      makeTask({ id: 3, boardColumn: 'in_progress' }),
      makeTask({ id: 4, boardColumn: 'in_review' }),
      makeTask({ id: 5, boardColumn: 'done' }),
    ]);
    expect(lanes.inbox.map((t) => t.id)).toEqual([1]);
    expect(lanes.queued.map((t) => t.id)).toEqual([2]);
    expect(lanes.running.map((t) => t.id)).toEqual([3]);
    expect(lanes.review.map((t) => t.id)).toEqual([4]);
    expect(lanes.done.map((t) => t.id)).toEqual([5]);
  });

  it('drops archived cards on the floor — they arrive by their own lazy fetch', () => {
    const lanes = splitLanes([makeTask({ boardColumn: 'archived' })]);
    expect([...lanes.inbox, ...lanes.queued, ...lanes.running, ...lanes.review, ...lanes.done]).toEqual(
      [],
    );
  });

  it('orders the Queued group the way the dispatcher orders candidates', () => {
    const lanes = splitLanes([
      makeTask({ id: 10, boardColumn: 'todo', priority: 'low', createdAt: '2026-01-01T00:00:00.000Z' }),
      makeTask({ id: 11, boardColumn: 'todo', priority: 'urgent', createdAt: '2026-08-01T00:00:00.000Z' }),
      makeTask({ id: 12, boardColumn: 'todo', priority: 'normal', createdAt: '2026-02-01T00:00:00.000Z' }),
    ]);
    expect(lanes.queued.map((t) => t.id)).toEqual([11, 12, 10]);
  });

  it('keeps a paused card in Queued rather than hiding it', () => {
    // The dispatcher's candidates() filters paused cards out; the board must
    // not, or a card someone parked would look deleted. It just sorts with the
    // rest and wears its `paused` badge.
    const lanes = splitLanes([
      makeTask({ id: 20, boardColumn: 'todo', priority: 'normal' }),
      makeTask({ id: 21, boardColumn: 'todo', priority: 'urgent', userPaused: true }),
    ]);
    expect(lanes.queued.map((t) => t.id)).toEqual([21, 20]);
  });

  it('orders Done most-recently-moved first', () => {
    const lanes = splitLanes([
      makeTask({ id: 30, boardColumn: 'done', columnMovedAt: '2026-03-01T00:00:00.000Z' }),
      makeTask({ id: 31, boardColumn: 'done', columnMovedAt: '2026-08-01T00:00:00.000Z' }),
      makeTask({ id: 32, boardColumn: 'done', columnMovedAt: null }),
    ]);
    expect(lanes.done.map((t) => t.id)).toEqual([31, 30, 32]);
  });

  it('returns five empty groups for an empty board', () => {
    const lanes = splitLanes([]);
    expect(lanes).toEqual({ inbox: [], queued: [], running: [], review: [], done: [] });
  });

  it('does not mutate the list it was handed', () => {
    const tasks = [
      makeTask({ id: 40, boardColumn: 'todo', priority: 'low' }),
      makeTask({ id: 41, boardColumn: 'todo', priority: 'urgent' }),
    ];
    const before = tasks.map((t) => t.id);
    splitLanes(tasks);
    expect(tasks.map((t) => t.id)).toEqual(before);
  });
});
