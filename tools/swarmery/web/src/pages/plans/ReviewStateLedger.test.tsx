// @vitest-environment jsdom
//
// The Review tab's state block (plan D20, phase 5 step 4): one row per fact,
// each with a glyph + word that reads without color (CI/review/state chips
// carried over unchanged from the old inline strip — LandingStatus.ts), a
// Criteria row, a Guard row, and D26's one-button close for an empty guard
// decision.

import { cleanup, fireEvent, render, screen } from '../../test/render';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { NeedsYouDecision, PhaseChangeStatus, PhaseLanding, ProviderTerms } from '../../api/types';
import { ReviewStateLedger } from './ReviewStateLedger';

afterEach(cleanup);

const TERMS: ProviderTerms = { provider: 'Host A', change: 'Pull Request', changeShort: 'PR' };

const status = (over: Partial<PhaseChangeStatus> = {}): PhaseChangeStatus => ({
  state: 'open',
  draft: false,
  ci: 'none',
  review: 'none',
  checkedAt: new Date().toISOString(),
  ...over,
});

const landing = (over: Partial<PhaseLanding> = {}): PhaseLanding => ({
  state: 'pr_open',
  prUrl: 'https://host.example/acme/w/pull/77',
  prNumber: 77,
  prProvider: 'github',
  prStatus: status(),
  landedAt: null,
  error: null,
  ...over,
});

function renderLedger(over: Partial<Parameters<typeof ReviewStateLedger>[0]> = {}) {
  const onRefreshed = vi.fn();
  render(
    <ReviewStateLedger
      taskId={1}
      phaseId={2}
      runBranch="swarm/phase-2"
      landing={landing()}
      terms={TERMS}
      checkboxesDone={3}
      checkboxesTotal={4}
      manualOpen={0}
      guard={{}}
      onRefreshed={onRefreshed}
      {...over}
    />,
  );
  return { onRefreshed };
}

describe('ReviewStateLedger', () => {
  it.each([
    ['success', '✓ CI passed'],
    ['failure', '✗ CI failed'],
    ['pending', '◌ CI running'],
  ] as const)('CI %s reads "%s"', (ci, want) => {
    renderLedger({ landing: landing({ prStatus: status({ ci }) }) });
    expect(screen.getByTestId('review-ledger-ci').textContent).toContain(want);
  });

  it.each([
    ['approved', 'approved'],
    ['changes_requested', 'changes requested'],
    ['review_required', 'review required'],
  ] as const)('review %s reads "%s"', (review, want) => {
    renderLedger({ landing: landing({ prStatus: status({ review }) }) });
    expect(screen.getByTestId('review-ledger-review').textContent).toContain(want);
  });

  it('names the pull request link from terms', () => {
    renderLedger();
    const link = screen.getByRole('link', { name: 'open ↗' }) as HTMLAnchorElement;
    expect(link.href).toBe('https://host.example/acme/w/pull/77');
    expect(screen.getByTestId('review-ledger-pr').textContent).toContain('PR #77');
  });

  it('names the branch row "Branch" with no run branch, "Landing" once there is one', () => {
    renderLedger({ runBranch: '', landing: landing({ state: 'none', prStatus: null, prUrl: null, prNumber: null }) });
    expect(screen.getByTestId('review-ledger-branch').textContent).toContain('no run branch yet');
    cleanup();
    renderLedger();
    expect(screen.getByTestId('review-ledger-branch').textContent).toContain('PR open');
  });

  it('shows merged N ago once merged', () => {
    renderLedger({
      landing: landing({ state: 'merged', landedAt: new Date(Date.now() - 60_000).toISOString(), prStatus: status({ state: 'merged' }) }),
    });
    expect(screen.getByTestId('review-ledger-branch').textContent).toContain('merged');
  });

  it('shows the criteria count and flags open manual checks', () => {
    renderLedger({ checkboxesDone: 2, checkboxesTotal: 5, manualOpen: 1 });
    const row = screen.getByTestId('review-ledger-criteria');
    expect(row.textContent).toContain('2/5');
    expect(row.textContent).toContain('manual check open');
  });

  it('reads the guard row clean when the guard found nothing', () => {
    renderLedger({ guard: {} });
    expect(screen.getByTestId('review-ledger-guard').textContent).toContain('clean');
  });

  it('names what the guard found, in words', () => {
    renderLedger({ guard: { protectedChanged: ['pkg/x.go'], guardBlocksTotal: 2 } });
    const row = screen.getByTestId('review-ledger-guard');
    expect(row.textContent).toContain('protected file changed');
    expect(row.textContent).toContain('blocked attempt');
  });

  it("D26: an empty guard decision's Guard row offers one close button", () => {
    const decision: NeedsYouDecision = {
      taskId: 1,
      phaseId: 2,
      decisionId: 'D-9',
      question: 'Allow?',
      options: [
        { id: 'allow-add-to-scope', label: 'Allow · add to scope' },
        { id: 'reject', label: 'Reject' },
      ],
      guard: {},
      paths: [],
    };
    const onCloseEmptyGuard = vi.fn();
    renderLedger({ guardDecision: decision, onCloseEmptyGuard });
    fireEvent.click(screen.getByRole('button', { name: 'Close · allow nothing' }));
    expect(onCloseEmptyGuard).toHaveBeenCalledTimes(1);
  });
});
