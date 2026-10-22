// @vitest-environment jsdom
//
// The needs-you phase_decision row (verify-not-trust phase 4): a phase run that
// touched protected paths. The claims:
//
//   1. The row shows the decision id, the guard badge, the question and a link
//      to the phase's Review tab (the same-instant rank is the server's,
//      TestNeedsYouPhaseDecisionSortAndDedupe; the page keeps it).
//   2. Each option is a button; clicking one POSTs the answer with the typed
//      note, then the list is refetched.
//   3. A refused answer (409) is shown inline and nothing is refetched.
//
// `npx vitest run src/pages/NeedsYou.phaseDecision.test.tsx`.

import { cleanup, fireEvent, render, screen, waitFor, within } from '../test/render';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../api';
import * as guardApi from '../api/guard';
import type { NeedsYouItem } from '../api/types';
import { NeedsYou } from './NeedsYou';

vi.mock('../api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../api')>()),
  getNeedsYou: vi.fn(),
  fetchApprovals: vi.fn(async () => []),
}));
vi.mock('../api/guard', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../api/guard')>()),
  answerGuardDecision: vi.fn(async () => undefined),
}));
vi.mock('../lib/ws', () => ({ useLiveUpdates: () => undefined }));

const NOW = Date.now();
const at = new Date(NOW - 20 * 60_000).toISOString();

const DECISION: NeedsYouItem = {
  kind: 'phase_decision',
  sessionId: 0,
  sessionUuid: '',
  sessionName: 'Trap plan · Phase 1: Make the test suite pass',
  projectSlug: 'trap',
  requestId: null,
  toolName: '',
  preview: 'D-4: The run touched protected paths: trap_test.go. Allow (add to scope) or reject?',
  question: 'The run touched protected paths: trap_test.go. Allow (add to scope) or reject?',
  asksQuestion: false,
  blockingSince: at,
  blockingSeconds: 1200,
  termFocusUrl: null,
  suggestion: null,
  phase: { taskId: 7, planExternalId: '2026-10-22-trap', planTitle: 'Trap plan', phaseId: 31, seq: 1, name: 'Make the test suite pass', manualOpen: 0 },
  decision: {
    taskId: 7,
    phaseId: 31,
    decisionId: 'D-4',
    question: 'The run touched protected paths: trap_test.go. Allow (add to scope) or reject?',
    options: [
      { id: 'allow-add-to-scope', label: 'Allow (add to scope)' },
      { id: 'reject', label: 'Reject' },
    ],
    guard: { protectedChanged: ['trap_test.go'], guardBlocksTotal: 2 },
  },
};

const FAILED: NeedsYouItem = { ...DECISION, kind: 'failed', sessionId: 2, sessionName: 'nightly build', preview: 'exit 1', phase: undefined, decision: undefined };

function serve(items: NeedsYouItem[]): void {
  vi.mocked(api.getNeedsYou).mockResolvedValue({ items, generatedAt: new Date(NOW).toISOString() });
}

async function renderPage(): Promise<HTMLElement> {
  render(
    <MemoryRouter initialEntries={['/needs-you']}>
      <NeedsYou />
    </MemoryRouter>,
  );
  await screen.findByText(/blocked on you/);
  const row = screen.getAllByTestId('needs-you-row').find((r) => r.dataset.kind === 'phase_decision');
  if (row === undefined) throw new Error('no phase_decision row');
  return row;
}

beforeEach(() => {
  vi.clearAllMocks();
});
afterEach(cleanup);

describe('NeedsYou phase_decision', () => {
  it('shows the decision, the guard badge and the question', async () => {
    serve([FAILED, DECISION]);
    const row = await renderPage();
    expect(within(row).getByText('Guard decision')).toBeTruthy();
    expect(within(row).getByText('D-4')).toBeTruthy();
    expect(within(row).getByText('1 protected file changed')).toBeTruthy();
    expect(within(row).getByText('2 blocked attempts')).toBeTruthy();
    expect(within(row).getByText(DECISION.question)).toBeTruthy();
    expect(within(row).getByRole('link', { name: 'open review →' }).getAttribute('href')).toContain('review');
  });

  it('answers with the clicked option and the note, then refetches', async () => {
    serve([DECISION]);
    const row = await renderPage();
    const calls = vi.mocked(api.getNeedsYou).mock.calls.length;
    fireEvent.change(within(row).getByRole('textbox'), { target: { value: ' the test was wrong ' } });
    serve([]);
    fireEvent.click(within(row).getByRole('button', { name: 'Allow (add to scope)' }));
    await waitFor(() =>
      expect(guardApi.answerGuardDecision).toHaveBeenCalledWith(7, 31, 'D-4', 'allow-add-to-scope', 'the test was wrong'),
    );
    await waitFor(() => expect(vi.mocked(api.getNeedsYou).mock.calls.length).toBeGreaterThan(calls));
  });

  it('shows a refused answer inline', async () => {
    serve([DECISION]);
    const row = await renderPage();
    vi.mocked(guardApi.answerGuardDecision).mockRejectedValueOnce(new Error('the decision is already answered'));
    fireEvent.click(within(row).getByRole('button', { name: 'Reject' }));
    expect(await within(row).findByRole('alert')).toBeTruthy();
    expect(within(row).getByRole('alert').textContent).toBe('the decision is already answered');
    expect(guardApi.answerGuardDecision).toHaveBeenCalledWith(7, 31, 'D-4', 'reject', '');
  });
});
