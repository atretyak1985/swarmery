// reviewNextStep's seven required states (plan D23, phase 5 step 1): a pure
// function, so each case is read straight off its input — no rendering, no API.

import { describe, expect, it } from 'vitest';
import type { NeedsYouDecision, PhaseLanding, ProviderTerms } from '../../api/types';
import { reviewNextStep, type ReviewNextStepPhase } from './reviewNextStep';

const TERMS: ProviderTerms = { provider: 'GitHub', change: 'pull request', changeShort: 'PR' };

const landing = (over: Partial<PhaseLanding> = {}): PhaseLanding => ({
  state: 'none',
  prUrl: null,
  prNumber: null,
  prProvider: null,
  prStatus: null,
  landedAt: null,
  error: null,
  ...over,
});

const phase = (over: Partial<ReviewNextStepPhase> = {}): ReviewNextStepPhase => ({
  runState: 'done',
  runSessionUuid: 'sess-1',
  manualOpen: 0,
  landOpen: 0,
  checkboxesDone: 4,
  checkboxesTotal: 4,
  landing: landing(),
  guardDecision: null,
  ...over,
});

const decision = (over: Partial<NeedsYouDecision> = {}): NeedsYouDecision => ({
  taskId: 1,
  phaseId: 1,
  decisionId: 'D-4',
  question: 'Allow?',
  options: [
    { id: 'allow-add-to-scope', label: 'Allow · add to scope' },
    { id: 'reject', label: 'Reject' },
  ],
  guard: { protectedChanged: ['pkg/x_test.go'] },
  paths: ['pkg/x_test.go'],
  ...over,
});

function run(phaseOver: Partial<ReviewNextStepPhase>, opts: { gitErrored?: boolean; branchGone?: boolean } = {}) {
  return reviewNextStep({
    phase: phase(phaseOver),
    gitErrored: opts.gitErrored ?? false,
    branchGone: opts.branchGone ?? false,
    terms: TERMS,
  });
}

describe('reviewNextStep', () => {
  it('phase never ran: neutral verdict, nothing offered, every action unavailable', () => {
    const r = run({ runState: 'idle', runSessionUuid: null, landing: landing() });
    expect(r.verdict.tone).toBe('neutral');
    expect(r.primary).toBeNull();
    expect(r.secondary).toBeNull();
    expect(r.unavailable.map((u) => u.id).sort()).toEqual(['pr', 'push', 'return']);
  });

  it('guard stopped the run: the primary action answers the decision, everything else is unavailable', () => {
    const r = run({ guardDecision: decision() });
    expect(r.verdict.tone).toBe('danger');
    expect(r.primary?.id).toBe('answer-decision');
    expect(r.secondary).toBeNull();
    expect(r.unavailable).toHaveLength(3);
    expect(r.unavailable.every((u) => u.reason !== '')).toBe(true);
  });

  it('an empty guard decision (D26) offers no card action, and is never named as the stop reason', () => {
    const r = run({ guardDecision: decision({ paths: [] }) });
    expect(r.primary).toBeNull();
    expect(r.note).not.toBeNull();
    expect(r.verdict.tone).toBe('neutral');
    expect(r.verdict.lead + r.verdict.rest).not.toContain('stopped');
  });

  it('a stop decision is never treated as empty, even with no paths', () => {
    const r = run({ guardDecision: decision({ paths: [], trigger: 'protected-path', reason: 'touched a hook' }) });
    expect(r.primary?.id).toBe('answer-decision');
  });

  it('only manual criteria remain: nothing to land, no action offered', () => {
    const r = run({ manualOpen: 1, landOpen: 0, checkboxesDone: 3, checkboxesTotal: 4, landing: landing({ state: 'pushed' }) });
    expect(r.verdict.tone).toBe('neutral');
    expect(r.primary).toBeNull();
    expect(r.note).not.toBeNull();
  });

  it('merged cleanly: success verdict, nothing left to do', () => {
    const r = run({ landing: landing({ state: 'merged', landedAt: '2026-11-18T00:00:00Z' }) });
    expect(r.verdict.tone).toBe('success');
    expect(r.primary).toBeNull();
    expect(r.unavailable.map((u) => u.id).sort()).toEqual(['pr', 'push', 'return']);
  });

  it('merged, but CI on the merged change failed: warn verdict, still nothing actionable', () => {
    const r = run({
      landing: landing({
        state: 'merged',
        prStatus: { state: 'merged', draft: false, ci: 'failure', review: 'none', checkedAt: '2026-11-18T00:00:00Z' },
      }),
    });
    expect(r.verdict.tone).toBe('warn');
    expect(r.primary).toBeNull();
  });

  it('merged, but review on the merged change did not pass: warn verdict', () => {
    const r = run({
      landing: landing({
        state: 'merged',
        prStatus: { state: 'merged', draft: false, ci: 'success', review: 'changes_requested', checkedAt: '2026-11-18T00:00:00Z' },
      }),
    });
    expect(r.verdict.tone).toBe('warn');
    expect(r.primary).toBeNull();
  });

  it('the run branch is gone: neutral verdict, nothing left to push or return', () => {
    const r = run({ landing: landing({ state: 'pushed' }) }, { branchGone: true });
    expect(r.verdict.tone).toBe('neutral');
    expect(r.primary).toBeNull();
    expect(r.unavailable.map((u) => u.id).sort()).toEqual(['pr', 'push', 'return']);
  });

  it('a merged phase whose branch is since gone still reads as merged', () => {
    const r = run({ landing: landing({ state: 'merged' }) }, { branchGone: true });
    expect(r.verdict.tone).toBe('success');
    expect(r.verdict.lead).toContain('Merged');
  });

  it('the branch is pushed and its open-PR review failed: Return is the only action, push/pr are unavailable', () => {
    const r = run({
      landing: landing({
        state: 'pr_open',
        prUrl: 'https://host/pr/1',
        prNumber: 1,
        prStatus: { state: 'open', draft: false, ci: 'success', review: 'changes_requested', checkedAt: '2026-11-18T00:00:00Z' },
      }),
    });
    expect(r.verdict.tone).toBe('danger');
    expect(r.primary?.id).toBe('return');
    expect(r.secondary).toBeNull();
    expect(r.unavailable.map((u) => u.id).sort()).toEqual(['pr', 'push']);
  });

  it('ready to land: Push + open <change> is primary, Push alone is secondary', () => {
    const r = run({ landing: landing({ state: 'ready' }) });
    expect(r.verdict.tone).toBe('neutral');
    expect(r.primary?.id).toBe('pr');
    expect(r.primary?.label).toContain(TERMS.change);
    expect(r.secondary?.id).toBe('push');
    expect(r.unavailable).toHaveLength(0);
  });

  it('a git error on a landable phase names push as unavailable, pointing at the disclosure', () => {
    const r = run({ landing: landing({ state: 'ready' }) }, { gitErrored: true });
    expect(r.unavailable.some((u) => u.id === 'push')).toBe(true);
  });

  it('an unknown combination (e.g. still running, nothing landed) stays neutral with no action — never guesses', () => {
    const r = run({ runState: 'running', landing: landing({ state: 'none' }) });
    expect(r.verdict.tone).toBe('neutral');
    expect(r.primary).toBeNull();
    expect(r.secondary).toBeNull();
  });
});
