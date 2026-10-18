// @vitest-environment jsdom
//
// RunOutcomeModal offers "Verify again" on the `unverified` blocker
// (phase-reverify plan, phase 2, D8). The modal does not own the request: the
// page hands it the shared action (label, busy state, tip) and the modal fires
// it and closes, like Retry run — so busy state and any refusal show on the
// page, where every other site of the action lives.
//
// Runs with the rest of the web suite: `npm test`.

import { cleanup, fireEvent, render, screen } from '../test/render';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { PhaseBlocker, PhaseDiagnosis } from '../api/types';
import { RunOutcomeModal } from './RunOutcomeModal';

vi.mock('../workspace/ProjectContext', () => ({
  useProjectWorkspace: () => ({
    slug: 'swarmery',
    project: { id: 3, slug: 'swarmery', name: 'swarmery' },
    projectId: 3,
    loading: false,
  }),
}));

const UNVERIFIED: PhaseBlocker = {
  kind: 'unverified',
  summary: 'verification could not conclude',
  detail: 'timeout: the verifier did not answer',
};

const diagnosis = (blockers: PhaseBlocker[]): PhaseDiagnosis => ({
  phaseId: 11,
  seq: 1,
  name: 'Backend',
  runOutcome: 'partial',
  criteriaTotal: 3,
  criteriaBefore: 0,
  criteriaAfter: 3,
  runStartedAt: null,
  runEndedAt: null,
  runError: null,
  verifyVerdict: 'inconclusive',
  verifyDetail: 'timeout',
  blockers,
  agentMessage: null,
});

let diag: PhaseDiagnosis = diagnosis([UNVERIFIED]);

beforeEach(() => {
  diag = diagnosis([UNVERIFIED]);
  vi.stubGlobal(
    'fetch',
    vi.fn(() => Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(diag) } as Response)),
  );
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function mount(reverify: { label: string; onClick: () => void; disabled?: boolean; tip?: string } | null, onClose = vi.fn()) {
  render(
    <MemoryRouter>
      <RunOutcomeModal
        taskId={77}
        phaseId={11}
        writesDisabled={false}
        writesDisabledReason=""
        onClose={onClose}
        onRetry={vi.fn()}
        reverify={reverify}
        onOpenRevisions={vi.fn()}
      />
    </MemoryRouter>,
  );
  return onClose;
}

describe('RunOutcomeModal — Verify again', () => {
  it('offers the action on the unverified blocker; a press fires it and closes', async () => {
    const onClick = vi.fn();
    const onClose = mount({ label: 'Verify again', onClick, tip: 'grade it again' });
    const btn = await screen.findByRole('button', { name: 'Verify again' });
    expect(btn.closest('li')?.textContent).toContain('verification could not conclude');
    fireEvent.click(btn);
    expect(onClick).toHaveBeenCalledTimes(1);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('renders the busy action disabled', async () => {
    mount({ label: 'verifying…', onClick: vi.fn(), disabled: true });
    const btn = (await screen.findByRole('button', { name: 'verifying…' })) as HTMLButtonElement;
    expect(btn.disabled).toBe(true);
  });

  it('offers nothing when the page passes no action (server: not re-gradable)', async () => {
    mount(null);
    await screen.findByText('verification could not conclude');
    expect(screen.queryByRole('button', { name: 'Verify again' })).toBeNull();
  });

  it('offers nothing on a diagnosis without an unverified blocker', async () => {
    diag = diagnosis([{ kind: 'no-criteria', summary: 'no criteria at all', detail: '' }]);
    mount({ label: 'Verify again', onClick: vi.fn() });
    await screen.findByText('no criteria at all');
    expect(screen.queryByRole('button', { name: 'Verify again' })).toBeNull();
  });
});
