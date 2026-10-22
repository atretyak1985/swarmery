// @vitest-environment jsdom
//
// CostBadge: shows the question's cost-to-change and flips it through
// PATCH /api/planning/sessions/{id}/turns/{seq}/cost.

import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '../../test/render';
import { CostBadge } from './CostBadge';

const setCost = vi.fn();
vi.mock('../../api', () => ({
  setPlanningTurnCost: (...a: unknown[]) => setCost(...a),
}));

afterEach(() => {
  cleanup();
  setCost.mockReset();
});

describe('CostBadge', () => {
  it('reads a missing cost as cheap and shows the reason', () => {
    render(<CostBadge planningSessionId={4} seq={3} cost={undefined} reason="a default value" />);
    expect(screen.getByTestId('cost-badge-label').textContent).toBe('cheap to change');
    expect(screen.getByText('D-3')).toBeTruthy();
    expect(screen.getByText('a default value')).toBeTruthy();
  });

  it('toggles cheap → expensive through the PATCH and reports the new question', async () => {
    setCost.mockResolvedValue({ id: 'q', type: 'single_select', question: 'Q?', options: [], costToChange: 'expensive' });
    const onChanged = vi.fn();
    render(<CostBadge planningSessionId={4} seq={3} cost="cheap" onChanged={onChanged} />);
    fireEvent.click(screen.getByRole('button', { name: 'Mark D-3 expensive to change' }));
    await waitFor(() => expect(screen.getByTestId('cost-badge-label').textContent).toBe('expensive to change'));
    expect(setCost).toHaveBeenCalledWith(4, 3, 'expensive');
    expect(onChanged).toHaveBeenCalledTimes(1);
    expect(screen.getByRole('button', { name: 'Mark D-3 cheap to change' })).toBeTruthy();
  });

  it('keeps the old cost and shows the error when the PATCH fails', async () => {
    setCost.mockRejectedValue(new Error('cost must be cheap or expensive'));
    render(<CostBadge planningSessionId={4} seq={1} cost="expensive" />);
    fireEvent.click(screen.getByRole('button', { name: 'Mark D-1 cheap to change' }));
    await waitFor(() => expect(screen.getByRole('alert').textContent).toBe('cost must be cheap or expensive'));
    expect(screen.getByTestId('cost-badge-label').textContent).toBe('expensive to change');
  });
});
