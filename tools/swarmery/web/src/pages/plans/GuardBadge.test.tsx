// @vitest-environment jsdom
//
// The guard badge (verify-not-trust phase 4): red pills for what the daemon's
// guard check found in a finished run, nothing at all for a clean check, a
// pre-0124 review (`{}`) or an older daemon (no field). Also: the phase card
// and the Review tab show it from the phase's open decision or latest review.

import { cleanup, render, screen } from '../../test/render';
import { afterEach, describe, expect, it } from 'vitest';
import { guardFindings, phaseGuard } from '../../api/guard';
import type { EpicPhase, GuardSummary } from '../../api/types';
import { GuardBadge } from './GuardBadge';

afterEach(cleanup);

const FLAGGED: GuardSummary = {
  hasGuard: true,
  protectedPaths: ['**/*_test.go'],
  protectedChanged: ['trap_test.go', 'pkg/a_test.go'],
  docCopyMismatch: true,
  guardBlocks: [{ at: 't', tool: 'Edit', path: 'trap_test.go', reason: 'r' }],
  guardBlocksTotal: 3,
  parseError: null,
};

describe('GuardBadge', () => {
  it('renders one red pill per finding', () => {
    render(<GuardBadge guard={FLAGGED} />);
    const status = screen.getByRole('status', { name: 'guard check' });
    expect(status.textContent).toContain('2 protected files changed');
    expect(status.textContent).toContain('doc copy mismatch');
    expect(status.textContent).toContain('3 blocked attempts');
    expect(screen.getByText('2 protected files changed').getAttribute('data-tip')).toBe(
      'the run changed protected paths: trap_test.go, pkg/a_test.go',
    );
    expect(screen.getByText('2 protected files changed').className).toContain('text-red');
  });

  it('singular counts and an unreadable guard block', () => {
    render(
      <GuardBadge
        guard={{
          protectedChanged: ['a_test.go'],
          guardBlocksTotal: 1,
          parseError: { kind: 'unparsable', line: 10, reason: 'not valid yaml' },
        }}
      />,
    );
    expect(screen.getByText('1 protected file changed')).toBeTruthy();
    expect(screen.getByText('1 blocked attempt')).toBeTruthy();
    expect(screen.getByText('guard block unreadable').getAttribute('data-tip')).toBe('not valid yaml');
  });

  it('renders nothing for a clean check, an old review and an old daemon', () => {
    const { container } = render(
      <>
        <GuardBadge guard={{ hasGuard: true, protectedChanged: [], guardBlocks: [], guardBlocksTotal: 0 }} />
        <GuardBadge guard={{}} />
        <GuardBadge guard={undefined} />
        <GuardBadge guard={null} />
      </>,
    );
    expect(container.textContent).toBe('');
    expect(guardFindings({})).toBeNull();
  });

  it('a phase shows its open decision first, else its latest review', () => {
    const decision = { taskId: 1, phaseId: 2, decisionId: 'D-1', question: 'q', options: [], guard: FLAGGED };
    const review = { protectedChanged: ['old_test.go'] };
    const phase = (p: Partial<EpicPhase>) => p as Pick<EpicPhase, 'guardDecision' | 'reviews'>;
    expect(phaseGuard(phase({ guardDecision: decision, reviews: [] }))).toBe(FLAGGED);
    expect(
      phaseGuard(
        phase({
          guardDecision: null,
          reviews: [
            {
              id: 1,
              verdict: 'pass',
              detail: '',
              fixRound: 0,
              runSessionUuid: 'u',
              startedAt: '',
              finishedAt: null,
              guard: review,
            },
          ],
        }),
      ),
    ).toBe(review);
    expect(phaseGuard(phase({}))).toBeNull();
  });
});
