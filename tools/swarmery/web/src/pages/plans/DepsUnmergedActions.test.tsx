// @vitest-environment jsdom
//
// The deps-unmerged refusal's "Open <change>" actions (Phase 5, SC-13), in
// isolation: which branches get a button, what a click sends, and what the
// refusal shows afterwards — the link, or the land failure with its hint.
// The wire path through the Plans page is Plans.depsUnmerged.test.tsx.

import { cleanup, fireEvent, render, screen } from '../../test/render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { PhaseLanding, PhaseLandResponse, ProviderTerms } from '../../api/types';
import { DepsUnmergedActions } from './DepsUnmergedActions';

const api = vi.hoisted(() => ({ landPhase: vi.fn() }));

vi.mock('../../api', async (importOriginal) => {
  const real = await importOriginal<typeof import('../../api')>();
  return { ...real, landPhase: api.landPhase };
});

const { LandError } = await import('../../api');

afterEach(cleanup);

const PR_TERMS: ProviderTerms = { provider: 'Host A', change: 'Pull Request', changeShort: 'PR' };
const MR_TERMS: ProviderTerms = { provider: 'Host B', change: 'Merge Request', changeShort: 'MR' };

const PHASES = [
  { id: 11, seq: 1 },
  { id: 12, seq: 2 },
  { id: 13, seq: 3 },
];

const opened = (over: Partial<PhaseLanding> = {}): PhaseLandResponse => ({
  branch: 'swarm/phase-11',
  base: 'main',
  action: 'pr',
  landing: {
    state: 'pr_open',
    prUrl: 'https://host.example/acme/w/5',
    prNumber: 5,
    prProvider: null,
    prStatus: null,
    landedAt: null,
    error: null,
    ...over,
  },
});

beforeEach(() => {
  api.landPhase.mockReset();
});

describe('DepsUnmergedActions', () => {
  it('offers Open <changeShort> only for branches that name a phase of this plan', () => {
    render(
      <DepsUnmergedActions
        taskId={77}
        phases={PHASES}
        branches={['swarm/phase-11', 'swarm/phase-999', 'feature/x', 'swarm/phase-12']}
        terms={MR_TERMS}
      />,
    );
    const buttons = screen.getAllByRole('button', { name: /^Open MR for / });
    expect(buttons.map((b) => b.getAttribute('aria-label'))).toEqual([
      'Open MR for swarm/phase-11',
      'Open MR for swarm/phase-12',
    ]);
    expect(buttons[0]?.textContent).toBe('Open MR');
  });

  it('renders nothing when no branch maps to a phase', () => {
    render(<DepsUnmergedActions taskId={77} phases={PHASES} branches={['swarm/phase-999']} terms={PR_TERMS} />);
    expect(screen.queryByRole('list')).toBeNull();
  });

  it('opens a change request for that phase and shows the link', async () => {
    api.landPhase.mockResolvedValue(opened());
    const onLanded = vi.fn();
    render(
      <DepsUnmergedActions
        taskId={77}
        phases={PHASES}
        branches={['swarm/phase-11']}
        terms={PR_TERMS}
        onLanded={onLanded}
      />,
    );
    fireEvent.click(screen.getByRole('button', { name: 'Open PR for swarm/phase-11' }));
    expect(api.landPhase).toHaveBeenCalledWith(77, 11, { action: 'pr' });

    const link = await screen.findByRole('link', { name: 'PR #5' });
    expect(link.getAttribute('href')).toBe('https://host.example/acme/w/5');
    expect(link.getAttribute('target')).toBe('_blank');
    expect(screen.queryByRole('button', { name: /Open PR/ })).toBeNull();
    expect(onLanded).toHaveBeenCalledTimes(1);
  });

  it('shows a 422 failure with its manual-command hint, and keeps the button', async () => {
    api.landPhase.mockRejectedValue(
      new LandError(
        422,
        { error: 'not signed in to the host', code: 'not-authenticated', hint: 'hostcli auth login' },
        'land failed',
      ),
    );
    render(<DepsUnmergedActions taskId={77} phases={PHASES} branches={['swarm/phase-12']} terms={PR_TERMS} />);
    fireEvent.click(screen.getByRole('button', { name: 'Open PR for swarm/phase-12' }));

    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('not signed in to the host');
    expect(alert.textContent).toContain('hostcli auth login');
    expect(screen.getByRole('button', { name: 'Open PR for swarm/phase-12' })).toBeTruthy();
  });
});
