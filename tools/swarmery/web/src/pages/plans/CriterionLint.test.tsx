// @vitest-environment jsdom
//
// CriterionLint (phase-run outcomes plan, phase 3): the Criteria tab's hint on
// an UNMARKED, UNTICKED criterion that reads like landing work or a hand check,
// with "Mark [LAND]" / "Mark [MANUAL]" calling PATCH …/docs {line, class}.
// web/tsconfig.json EXCLUDES *.test.tsx — treat the casts as documentation.

import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../../api';
import { CriterionLint, criterionClassOf, looksLikeLandOrManual } from './CriterionLint';

vi.mock('../../api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../api')>()),
  markPlanCriterion: vi.fn(),
}));

beforeEach(() => vi.clearAllMocks());
afterEach(cleanup);

describe('looksLikeLandOrManual', () => {
  it.each([
    'Push the branch and open a pull request',
    'open the PR against main',
    'PR merged',
    'merge the change',
    'run gh pr create',
    'verified on production',
    'check the console for errors',
    'Checked manually in the browser',
    'перевірено на прод',
    'Перевірити вручну',
    'протестовано по руках',
  ])('flags %j', (label) => {
    expect(looksLikeLandOrManual(label)).toBe(true);
  });

  it.each([
    'the endpoint returns 404 for an unknown id',
    'pushes are debounced', // `push` only as a whole word
    'a product page renders', // `pr`/`прод` only as whole words
    'продукт має ціну', // Cyrillic stem inside a longer word
    'merged-state helper is pure', // `merge` only as a whole word
    'consoles are not involved',
  ])('leaves %j alone', (label) => {
    expect(looksLikeLandOrManual(label)).toBe(false);
  });

  it('never flags an already marked criterion', () => {
    expect(looksLikeLandOrManual('[LAND] push the branch')).toBe(false);
    expect(looksLikeLandOrManual('[MANUAL] check production')).toBe(false);
    expect(criterionClassOf('[LAND] push')).toBe('LAND');
    expect(criterionClassOf('[MANUAL] check')).toBe('MANUAL');
    expect(criterionClassOf('push [LAND]')).toBeNull();
  });
});

describe('CriterionLint', () => {
  const props = {
    taskId: 7,
    path: 'phase-2-rollout.md',
    line: 12,
    text: 'push the branch and open the PR',
    done: false,
  };

  it('renders nothing for a ticked, a marked or an ordinary criterion', () => {
    const onMarked = vi.fn();
    const { container, rerender } = render(<CriterionLint {...props} done onMarked={onMarked} />);
    expect(container.innerHTML).toBe('');
    rerender(<CriterionLint {...props} text="[LAND] push the branch" onMarked={onMarked} />);
    expect(container.innerHTML).toBe('');
    rerender(<CriterionLint {...props} text="the handler validates input" onMarked={onMarked} />);
    expect(container.innerHTML).toBe('');
  });

  it('marks [LAND] through the PATCH and hands back the fresh doc', async () => {
    const fresh = { path: props.path, content: '- [ ] [LAND] push the branch and open the PR\n' };
    vi.mocked(api.markPlanCriterion).mockResolvedValue(fresh);
    const onMarked = vi.fn();
    render(<CriterionLint {...props} onMarked={onMarked} />);
    expect(screen.getByText(/looks like a landing\/manual criterion/)).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: 'Mark [LAND]' }));
    await waitFor(() => expect(onMarked).toHaveBeenCalledWith(fresh));
    expect(api.markPlanCriterion).toHaveBeenCalledWith(7, 'phase-2-rollout.md', 12, 'LAND');
  });

  it('marks [MANUAL], and shows a refusal without calling back', async () => {
    vi.mocked(api.markPlanCriterion).mockRejectedValue(new Error('line is not an acceptance criterion'));
    const onMarked = vi.fn();
    render(<CriterionLint {...props} text="check the production console" onMarked={onMarked} />);
    fireEvent.click(screen.getByRole('button', { name: 'Mark [MANUAL]' }));
    expect((await screen.findByRole('alert')).textContent).toBe('line is not an acceptance criterion');
    expect(api.markPlanCriterion).toHaveBeenCalledWith(7, 'phase-2-rollout.md', 12, 'MANUAL');
    expect(onMarked).not.toHaveBeenCalled();
  });
});
