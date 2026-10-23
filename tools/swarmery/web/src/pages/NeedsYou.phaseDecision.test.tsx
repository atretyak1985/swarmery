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
  answerPhaseDecision: vi.fn(async () => undefined),
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

// An agent-declared stop (verify-not-trust phase 10): the row is labelled a stop
// decision and carries the trigger, the reason, the executor's two options with
// what each leads to, and a free-text answer recorded as option `other`.
const STOP: NeedsYouItem = {
  ...DECISION,
  preview: 'D-6: criterion b needs scripts/tests/run.sh',
  question: 'criterion b needs scripts/tests/run.sh',
  decision: {
    taskId: 7,
    phaseId: 31,
    decisionId: 'D-6',
    source: 'stop',
    question: 'criterion b needs scripts/tests/run.sh',
    options: [
      { id: 'allow', label: 'Allow editing scripts/tests/run.sh' },
      { id: 'skip', label: 'Keep the script protected' },
      { id: 'other', label: 'Other (free text)' },
    ],
    guard: {},
    stop: {
      trigger: 'protected-path',
      reason: 'criterion b needs scripts/tests/run.sh',
      paths: ['scripts/tests/run.sh'],
      options: [
        { id: 'allow', label: 'Allow editing scripts/tests/run.sh', consequence: 'the re-run wires the suite' },
        { id: 'skip', label: 'Keep the script protected', consequence: 'b stays open' },
      ],
    },
  },
};

describe('NeedsYou agent stop decision', () => {
  it('shows the trigger, the reason and the two options with their consequences', async () => {
    serve([STOP]);
    const row = await renderPage();
    expect(within(row).getByText('Stop decision')).toBeTruthy();
    expect(within(row).getByText('stop: protected path')).toBeTruthy();
    expect(within(row).getByText('criterion b needs scripts/tests/run.sh')).toBeTruthy();
    const group = within(row).getByRole('group', { name: 'answer D-6' });
    expect(within(group).getAllByRole('button').map((b) => b.textContent)).toEqual([
      'Allow editing scripts/tests/run.sh',
      'Keep the script protected',
    ]);
    expect(within(row).getByText('→ the re-run wires the suite')).toBeTruthy();
    expect(within(row).queryByText('Guard decision')).toBeNull();
  });

  it('answers an option, and free text as `other`', async () => {
    serve([STOP]);
    const row = await renderPage();
    fireEvent.click(within(row).getByRole('button', { name: 'Allow editing scripts/tests/run.sh' }));
    await waitFor(() => expect(guardApi.answerPhaseDecision).toHaveBeenCalledWith(7, 31, 'D-6', 'allow', ''));

    const answer = within(row).getByRole('button', { name: 'Answer' });
    expect((answer as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(within(row).getByRole('textbox'), { target: { value: ' move it to the Makefile ' } });
    fireEvent.click(answer);
    await waitFor(() =>
      expect(guardApi.answerPhaseDecision).toHaveBeenCalledWith(7, 31, 'D-6', 'other', 'move it to the Makefile'),
    );
  });
});
