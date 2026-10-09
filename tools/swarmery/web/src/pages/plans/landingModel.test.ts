import { describe, expect, it } from 'vitest';
import type { PhaseLandingState, ProviderTerms } from '../../api/types';
import {
  branchToPhaseId,
  canLand,
  canReturn,
  hasReviewTab,
  type LandingPhase,
  landingChip,
  landingLabel,
  prLinkText,
} from './landingModel';

// Two vocabularies the model must treat identically — it may only ever read them.
const PR: ProviderTerms = { provider: 'Host A', change: 'Pull Request', changeShort: 'PR' };
const MR: ProviderTerms = { provider: 'Host B', change: 'Merge Request', changeShort: 'MR' };

const phase = (over: Partial<LandingPhase> = {}): LandingPhase => ({
  runState: 'done',
  runSessionUuid: 'sess-1',
  landing: { state: 'ready' },
  ...over,
});

describe('landingLabel', () => {
  it('names every state, taking the change term from terms', () => {
    const states: PhaseLandingState[] = ['none', 'ready', 'pushed', 'pr_open', 'merged', 'returned'];
    expect(states.map((s) => landingLabel(s, PR))).toEqual([
      'not landed',
      'ready to land',
      'pushed',
      'PR open',
      'merged',
      'returned to agent',
    ]);
    expect(landingLabel('pr_open', MR)).toBe('MR open');
  });
});

describe('canLand', () => {
  it('is true for a finished run that is ready, pushed or returned', () => {
    for (const state of ['ready', 'pushed', 'returned', 'none'] as const) {
      expect(canLand(phase({ landing: { state } }))).toBe(true);
    }
  });

  it('is false while running, before any run, and past the push', () => {
    expect(canLand(phase({ runState: 'running' }))).toBe(false);
    expect(canLand(phase({ runState: 'idle', landing: { state: 'none' } }))).toBe(false);
    expect(canLand(phase({ landing: { state: 'pr_open' } }))).toBe(false);
    expect(canLand(phase({ landing: { state: 'merged' } }))).toBe(false);
  });

  it('reads a missing landing as none', () => {
    expect(canLand(phase({ landing: undefined }))).toBe(true);
    expect(canLand(phase({ runState: 'idle', landing: undefined }))).toBe(false);
  });
});

describe('canReturn', () => {
  it('is offered on any finished, unmerged phase', () => {
    expect(canReturn(phase({ landing: { state: 'pr_open' } }))).toBe(true);
    expect(canReturn(phase({ landing: { state: 'merged' } }))).toBe(false);
    expect(canReturn(phase({ runState: 'running' }))).toBe(false);
  });
});

describe('hasReviewTab', () => {
  it('hides the tab only for a phase that never ran', () => {
    expect(hasReviewTab(phase({ runState: 'idle', runSessionUuid: null, landing: { state: 'none' } }))).toBe(false);
    expect(hasReviewTab(phase({ runState: 'idle', runSessionUuid: null, landing: undefined }))).toBe(false);
    expect(hasReviewTab(phase({ runState: 'idle', runSessionUuid: null, landing: { state: 'pushed' } }))).toBe(true);
    expect(hasReviewTab(phase({ runState: 'idle', runSessionUuid: 's', landing: { state: 'none' } }))).toBe(true);
    expect(hasReviewTab(phase({ runState: 'failed', runSessionUuid: null }))).toBe(true);
  });
});

describe('prLinkText', () => {
  it('builds "<short> #<n>" from terms', () => {
    expect(prLinkText({ prUrl: 'https://h/acme/w/pull/77', prNumber: 77 }, PR)).toBe('PR #77');
    expect(prLinkText({ prUrl: 'https://h/acme/w/-/merge_requests/12', prNumber: 12 }, MR)).toBe('MR #12');
  });

  it('falls back to the bare short term without a number, and null without a URL', () => {
    expect(prLinkText({ prUrl: 'https://h/x', prNumber: null }, PR)).toBe('PR');
    expect(prLinkText({ prUrl: null, prNumber: 5 }, PR)).toBeNull();
    expect(prLinkText({ prUrl: '', prNumber: null }, PR)).toBeNull();
  });
});

describe('branchToPhaseId', () => {
  it('reads swarm/phase-<id>', () => {
    expect(branchToPhaseId('swarm/phase-123')).toBe(123);
    expect(branchToPhaseId(' swarm/phase-7 ')).toBe(7);
  });

  it('is null for any other branch', () => {
    for (const b of ['main', 'swarm/phase-', 'swarm/phase-12a', 'swarm/task-12', 'feat/swarm/phase-1', 'swarm/phase-0']) {
      expect(branchToPhaseId(b)).toBeNull();
    }
  });
});

describe('landingChip', () => {
  const base = { prUrl: null, prNumber: null };

  it('says nothing before the landing flow', () => {
    expect(landingChip({ ...base, state: 'none' }, PR)).toBeNull();
  });

  it('labels every other state, with a check mark on a merge', () => {
    expect(landingChip({ ...base, state: 'ready' }, PR)?.text).toBe('ready to land');
    expect(landingChip({ ...base, state: 'pushed' }, PR)?.text).toBe('pushed');
    expect(landingChip({ ...base, state: 'merged' }, PR)?.text).toBe('merged ✓');
    expect(landingChip({ ...base, state: 'returned' }, PR)).toEqual({
      state: 'returned',
      text: 'returned to agent',
      href: null,
    });
  });

  it('links an open change request, labelled from terms', () => {
    const open = { state: 'pr_open' as const, prUrl: 'https://host.example/acme/w/7', prNumber: 7 };
    expect(landingChip(open, PR)).toEqual({ state: 'pr_open', text: 'PR #7', href: open.prUrl });
    expect(landingChip(open, MR)?.text).toBe('MR #7');
  });

  it('falls back to the state label when the change request has no URL', () => {
    expect(landingChip({ ...base, state: 'pr_open' }, MR)).toEqual({ state: 'pr_open', text: 'MR open', href: null });
  });
});
