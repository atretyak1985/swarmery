// @vitest-environment jsdom
//
// The probe table on a proposal card (harness² phase 4). The claims:
//
//   1. A probeable proposal (proposed/approved) shows the Probe button and the
//      single-sample warning, before any run.
//   2. The latest job's rows render one per case: ✓/✗ per side, the blind pick,
//      USD, and the total; earlier jobs are counted, not shown.
//   3. Probe POSTs and re-reads; a refusal shows inline.
//   4. A decided proposal with no rows renders nothing; with rows it shows them
//      without a button.
//   5. The labels and the warning are translated in uk.
//
// `npx vitest run src/pages/agent-hub/ProposalProbes.test.tsx`.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { AgentChangeProposal, ProposalProbe, ProposalProbesResp } from '../../api/types';
import { activate } from '../../i18n';
import { act, cleanup, fireEvent, render, screen, waitFor } from '../../test/render';

const api = vi.hoisted(() => ({ fetchProposalProbes: vi.fn(), startProposalProbe: vi.fn() }));

vi.mock('../../api', async (importOriginal) => {
  const real = await importOriginal<typeof import('../../api')>();
  return { ...real, fetchProposalProbes: api.fetchProposalProbes, startProposalProbe: api.startProposalProbe };
});

const { canProbe, latestRun, ProposalProbes } = await import('./ProposalProbes');

afterEach(async () => {
  cleanup();
  await act(() => activate('en'));
});
beforeEach(() => {
  api.fetchProposalProbes.mockReset();
  api.startProposalProbe.mockReset();
});

function proposal(over: Partial<AgentChangeProposal> = {}): AgentChangeProposal {
  return {
    id: 7,
    recommendation_id: null,
    agent: 'tech-lead',
    agent_path: 'plugins/core/agents/tech-lead.md',
    target_kind: 'agent',
    target_path: '',
    base_sha256: 'x',
    diff: '',
    rationale: '',
    status: 'proposed',
    error: null,
    pr_url: null,
    created_at: '2026-11-03T00:00:00.000Z',
    decided_at: null,
    ...over,
  };
}

function row(over: Partial<ProposalProbe> = {}): ProposalProbe {
  return {
    id: 1,
    proposal_id: 7,
    job_id: 'probe-7-new',
    source: 'promptfoo',
    task_id: 'routes schema work to architect',
    base_pass: true,
    variant_pass: true,
    blind_choice: 'tie',
    reason: '[A=base · B=variant] both route correctly',
    usd: 0.42,
    created_at: '2026-11-03T00:00:00Z',
    ...over,
  };
}

function resp(probes: ProposalProbe[], over: Partial<ProposalProbesResp> = {}): ProposalProbesResp {
  return { proposal_id: 7, max_usd: 2, jobs: [], probes, ...over };
}

describe('ProposalProbes', () => {
  it('offers a probe with the single-sample warning before any run', async () => {
    api.fetchProposalProbes.mockResolvedValue(resp([]));
    render(<ProposalProbes p={proposal()} />);
    await waitFor(() => expect(api.fetchProposalProbes).toHaveBeenCalledWith(7));
    expect(screen.getByRole('button', { name: 'Probe' })).toBeTruthy();
    expect(screen.getByTestId('probe-sample-warning').textContent).toBe(
      'One sample per case: treat a single flip as noise and a difference that repeats across runs as signal.',
    );
    expect(screen.queryAllByTestId('probe-row')).toHaveLength(0);
  });

  it('renders the latest run, one row per case, with the total', async () => {
    api.fetchProposalProbes.mockResolvedValue(
      resp([
        row({ id: 3 }),
        row({ id: 4, task_id: 'reviewer before commit', variant_pass: false, blind_choice: 'skipped', usd: 0.3 }),
        row({ id: 5, task_id: 'ledger row', base_pass: null, variant_pass: null, blind_choice: 'capped', usd: 0 }),
        row({ id: 1, job_id: 'probe-7-old', blind_choice: 'variant' }),
      ]),
    );
    render(<ProposalProbes p={proposal({ status: 'approved' })} />);
    await waitFor(() => expect(screen.getAllByTestId('probe-row')).toHaveLength(3));
    const choices = screen.getAllByTestId('probe-choice').map((c) => c.textContent);
    expect(choices).toEqual(['tie', 'not judged', 'capped']);
    expect(screen.getByTestId('probe-total').textContent).toBe('$0.72');
    expect(screen.getByText('total · earlier runs not shown: 1')).toBeTruthy();
    expect(screen.getAllByLabelText('fail')).toHaveLength(1);
    expect(screen.getAllByLabelText('pass')).toHaveLength(3);
    expect(screen.getByText('routes schema work to architect').getAttribute('data-tip')).toContain('[A=base');
    expect(screen.getByRole('button', { name: 'Probe again' })).toBeTruthy();
  });

  it('starts a probe, re-reads, and shows a refusal inline', async () => {
    api.fetchProposalProbes.mockResolvedValue(resp([]));
    api.startProposalProbe.mockResolvedValueOnce({ id: 'probe-7-x', status: 'running' });
    render(<ProposalProbes p={proposal()} />);
    const button = await screen.findByRole('button', { name: 'Probe' });
    fireEvent.click(button);
    await waitFor(() => expect(api.startProposalProbe).toHaveBeenCalledWith(7));
    await waitFor(() => expect(api.fetchProposalProbes).toHaveBeenCalledTimes(2));

    api.startProposalProbe.mockRejectedValueOnce(new Error('a probe is already running for this proposal'));
    fireEvent.click(screen.getByRole('button', { name: 'Probe' }));
    expect(await screen.findByText('a probe is already running for this proposal')).toBeTruthy();
  });

  it('shows "probing…" and a disabled button while a job runs, and the job error after', async () => {
    api.fetchProposalProbes.mockResolvedValue(
      resp([], { jobs: [{ id: 'j', proposal_id: 7, status: 'running', usd: 0, max_usd: 2, started_at: 'x' }] }),
    );
    render(<ProposalProbes p={proposal()} />);
    const busy = await screen.findByRole('button', { name: 'probing…' });
    expect((busy as HTMLButtonElement).disabled).toBe(true);
    cleanup();

    api.fetchProposalProbes.mockResolvedValue(
      resp([], {
        jobs: [{ id: 'j', proposal_id: 7, status: 'failed', error: 'sha256 mismatch', usd: 0, max_usd: 2, started_at: 'x' }],
      }),
    );
    render(<ProposalProbes p={proposal()} />);
    expect((await screen.findByTestId('probe-job-error')).textContent).toBe('sha256 mismatch');
  });

  it('hides itself on a decided proposal with no rows, and shows rows without a button', async () => {
    api.fetchProposalProbes.mockResolvedValue(resp([]));
    const { container } = render(<ProposalProbes p={proposal({ status: 'applied' })} />);
    await waitFor(() => expect(api.fetchProposalProbes).toHaveBeenCalled());
    expect(container.textContent).toBe('');
    cleanup();

    api.fetchProposalProbes.mockResolvedValue(resp([row()]));
    render(<ProposalProbes p={proposal({ status: 'rejected' })} />);
    await waitFor(() => expect(screen.getAllByTestId('probe-row')).toHaveLength(1));
    expect(screen.queryByRole('button')).toBeNull();
  });

  it('is translated in uk', async () => {
    await act(() => activate('uk'));
    api.fetchProposalProbes.mockResolvedValue(resp([row({ blind_choice: 'no_suite', task_id: '' })]));
    render(<ProposalProbes p={proposal()} />);
    await waitFor(() => expect(screen.getAllByTestId('probe-row')).toHaveLength(1));
    expect(screen.getByTestId('probe-sample-warning').textContent).toContain('Одна вибірка на кейс');
    expect(screen.getByTestId('probe-choice').textContent).toBe('немає набору');
    expect(screen.getByRole('button', { name: 'Проба ще раз' })).toBeTruthy();
  });
});

describe('helpers', () => {
  it('canProbe only while the decision is open', () => {
    expect(canProbe({ status: 'proposed' })).toBe(true);
    expect(canProbe({ status: 'approved' })).toBe(true);
    for (const s of ['applied', 'rejected', 'failed', 'needs_target'] as const) {
      expect(canProbe({ status: s })).toBe(false);
    }
  });

  it('latestRun keeps the first job only', () => {
    expect(latestRun([])).toEqual([]);
    expect(latestRun([row({ id: 2 }), row({ id: 1, job_id: 'old' })]).map((r) => r.id)).toEqual([2]);
  });
});
