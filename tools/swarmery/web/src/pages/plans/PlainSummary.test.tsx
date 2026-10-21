// @vitest-environment jsdom
//
// The "In plain words" block: a cached retelling renders straight away, a
// missing one is generated on open, an empty source says so, and a failed run
// offers a retry.

import { cleanup, fireEvent, render, screen, waitFor } from '../../test/render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { PlainSummary } from './PlainSummary';

const api = vi.hoisted(() => ({
  fetchPlainSummary: vi.fn(),
  generatePlainSummary: vi.fn(),
}));

vi.mock('../../api', async (importOriginal) => {
  const real = await importOriginal<typeof import('../../api')>();
  return {
    ...real,
    fetchPlainSummary: api.fetchPlainSummary,
    generatePlainSummary: api.generatePlainSummary,
  };
});

afterEach(cleanup);
beforeEach(() => {
  api.fetchPlainSummary.mockReset();
  api.generatePlainSummary.mockReset();
});

describe('PlainSummary', () => {
  it('renders a cached retelling without generating', async () => {
    api.fetchPlainSummary.mockResolvedValue({ status: 'ready', text: 'This phase adds **login**.' });
    render(<PlainSummary taskId={7} kind="phase" path="phase-1-x.md" version="v1" />);
    expect(await screen.findByText('login')).toBeTruthy();
    expect(api.fetchPlainSummary).toHaveBeenCalledWith(7, 'phase', 'phase-1-x.md', 'en');
    expect(api.generatePlainSummary).not.toHaveBeenCalled();
  });

  it('generates the retelling when none is cached', async () => {
    api.fetchPlainSummary.mockResolvedValue({ status: 'missing' });
    api.generatePlainSummary.mockResolvedValue({ status: 'ready', text: 'The plan makes exports faster.' });
    render(<PlainSummary taskId={7} kind="plan" />);
    expect(await screen.findByText('The plan makes exports faster.')).toBeTruthy();
    expect(api.generatePlainSummary).toHaveBeenCalledWith(7, 'plan', null, 'en');
  });

  it('says so when there is nothing to explain', async () => {
    api.fetchPlainSummary.mockResolvedValue({ status: 'empty' });
    render(<PlainSummary taskId={7} kind="phase-report" path="phase-1-x.md" />);
    expect(await screen.findByText(/nothing to explain yet/)).toBeTruthy();
    expect(api.generatePlainSummary).not.toHaveBeenCalled();
  });

  it('shows the error and retries', async () => {
    api.fetchPlainSummary.mockResolvedValue({ status: 'missing' });
    api.generatePlainSummary
      .mockRejectedValueOnce(new Error('retelling failed: boom'))
      .mockResolvedValueOnce({ status: 'ready', text: 'second try' });
    render(<PlainSummary taskId={7} kind="plan-report" />);
    expect(await screen.findByText(/retelling failed: boom/)).toBeTruthy();
    fireEvent.click(screen.getByRole('button'));
    await waitFor(() => expect(screen.getByText('second try')).toBeTruthy());
  });
});
