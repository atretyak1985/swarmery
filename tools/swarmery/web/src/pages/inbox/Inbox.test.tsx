// @vitest-environment jsdom
//
// The Inbox page (handoff §8). The claims:
//
//   1. It lists five kinds in their groups, and no approval: those block, and are
//      decided in Needs you.
//   2. j / k move the selection.
//   3. e runs the main action of the selected item, x the secondary one.
//   4. ?tab=lessons opens that tab; ?tab=approvals goes to Needs you.
//   5. Each kind's detail has exactly ONE primary button.
//   6. A failed source is a "couldn't load" row, not a blank Inbox.
//   7. A decided item leaves the list, and an empty list says "All reviewed."
//
// Runs with the rest of the web suite: `npm test` (vitest, also a swarmery-ci
// step). On its own: `npx vitest run src/pages/inbox`.

import { act, cleanup, fireEvent, render, screen, within } from '../../test/render';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../../api';
import * as alerts from '../../api/alerts';
import * as decisions from '../../api/decisions';
import * as lessons from '../../api/lessons';
import * as reviews from '../../api/reviews';
import type { Review } from '../../api/reviews';
import * as triage from '../../api/triage';
import type { TriageVerdict } from '../../api/triage';
import { Inbox } from './Inbox';

const NOW = Date.now();
const iso = (offsetSec: number): string => new Date(NOW + offsetSec * 1000).toISOString();

function approvalRow(id: number, toolName: string, input: unknown, expiresIn: number): Record<string, unknown> {
  return {
    id,
    sessionId: 40 + id,
    toolName,
    requestJson: JSON.stringify({ tool_name: toolName, tool_input: input }),
    status: 'pending',
    requestedAt: iso(-42),
    resolvedAt: null,
    resolvedVia: null,
    reason: null,
    expiresAt: iso(expiresIn),
  };
}

const ASK = {
  questions: [
    {
      question: 'Which layout?',
      header: 'Layout',
      options: [
        { label: 'split', description: 'two panes' },
        { label: 'stack', description: 'one column' },
      ],
      multiSelect: false,
    },
  ],
};

vi.mock('../../api', () => ({
  fetchApprovals: vi.fn(),
  fetchRecommendations: vi.fn(),
  fetchProjectRecommendations: vi.fn(),
  fetchProposals: vi.fn(),
  resolveApproval: vi.fn(async () => ({})),
  patchRecommendation: vi.fn(async () => ({})),
  patchProposal: vi.fn(async () => undefined),
}));

vi.mock('../../api/lessons', () => ({
  fetchLessons: vi.fn(async () => [
    {
      id: 5,
      phaseName: 'Phase 3',
      title: 'Run the fixture generator first',
      guidance: 'Run it before backend-tests.',
      areaGlobs: ['services/orders/**'],
      cause: 'fixtures were stale',
      sourceParagraph: '',
      surpriseIndex: 0.62,
      recurrences: 3,
      createdAt: new Date(Date.now() - 7200_000).toISOString(),
    },
  ]),
  fetchRetirements: vi.fn(async () => [
    {
      id: 8,
      title: 'Index every FK child column',
      guidance: 'Index them.',
      reason: 'ineffective',
      detail: 'no measured drop',
      proposedAt: new Date(Date.now() - 3 * 86400_000).toISOString(),
      autoRetireAt: null,
    },
  ]),
  acceptLesson: vi.fn(async () => ({})),
  dismissLesson: vi.fn(async () => ({})),
  confirmRetirement: vi.fn(async () => ({})),
  keepLesson: vi.fn(async () => ({})),
}));

vi.mock('../../api/decisions', () => ({
  fetchLabelQueue: vi.fn(),
  postGroundTruth: vi.fn(async () => undefined),
}));

// The seventh source. Empty by default, so the six-kind claims above count what
// they always counted; the alert test below fills it.
vi.mock('../../api/alerts', () => ({
  ACCOUNT_BREAKER_RULE: 'account_breaker_open',
  AUTO_MODE_NO_VERDICT_RULE: 'auto_mode_no_verdict',
  fetchAlerts: vi.fn(async () => []),
  resumeAccount: vi.fn(async () => undefined),
}));

// The triage agent: nothing open, nothing handled, no run by default.
vi.mock('../../api/triage', () => ({
  TriageBusyError: class TriageBusyError extends Error {},
  TriageConflictError: class TriageConflictError extends Error {
    readonly state = 'stale';
  },
  fetchActiveTriageRun: vi.fn(async () => null),
  fetchTriageRun: vi.fn(async () => null),
  fetchTriageVerdicts: vi.fn(async () => []),
  fetchTriageAudit: vi.fn(async () => ({ answered: 0, agree: 0, byQuestion: [] })),
  startTriageRun: vi.fn(async () => ({ id: 1 })),
  acceptTriageVerdict: vi.fn(async () => ({})),
  acceptAllTriageVerdicts: vi.fn(async () => ({ accepted: [], stale: [], failed: [], remaining: 0 })),
  undoTriageVerdict: vi.fn(async () => ({})),
}));

// The eighth source: unacknowledged plan branch reviews. Empty by default, like
// alerts; the review test fills it. countFindings stays the real one.
vi.mock('../../api/reviews', async (importOriginal) => {
  const real = await importOriginal<typeof import('../../api/reviews')>();
  return {
    ...real,
    fetchUnackedPlanReviews: vi.fn(async () => []),
    ackReview: vi.fn(async () => null),
  };
});

vi.mock('../../lib/ws', () => ({ useLiveUpdates: () => undefined }));

function defaultFetchers(): void {
  vi.mocked(triage.fetchTriageVerdicts).mockResolvedValue([]);
  vi.mocked(triage.fetchActiveTriageRun).mockResolvedValue(null);
  vi.mocked(triage.fetchTriageAudit).mockResolvedValue({ answered: 0, agree: 0, byQuestion: [] });
  vi.mocked(alerts.fetchAlerts).mockResolvedValue([]);
  vi.mocked(reviews.fetchUnackedPlanReviews).mockResolvedValue([]);
  vi.mocked(api.fetchApprovals).mockResolvedValue([
    approvalRow(1, 'Bash', { command: 'rm -rf node_modules && npm ci' }, 78),
    approvalRow(2, 'AskUserQuestion', ASK, 300),
  ] as never);
  vi.mocked(api.fetchRecommendations).mockResolvedValue({
    recommendations: [
      {
        id: 6,
        rule: 'R2',
        target: 'implementation-agent',
        title: 'implementation-agent fails 3× the fleet',
        detail: 'Most failures are one thing.',
        created_at: new Date(NOW - 86400_000).toISOString(),
      },
    ],
  } as never);
  vi.mocked(api.fetchProposals).mockResolvedValue({
    proposals: [
      {
        id: 7,
        agent: 'implementation-agent',
        agent_path: 'agents/implementation-agent.md',
        target_path: '',
        target_kind: 'agent',
        diff: '+ tool hygiene',
        rationale: 'writes outside its worktree',
        created_at: new Date(NOW - 2 * 86400_000).toISOString(),
      },
    ],
  } as never);
  vi.mocked(decisions.fetchLabelQueue).mockResolvedValue([
    {
      id: 9,
      questionId: 'd2.task_type',
      subject: 's1',
      sessionUuid: 's1',
      sessionTitle: 'Admin verification screens',
      answer: 'feature',
      confidence: 1,
      createdAt: new Date(NOW - 4 * 86400_000).toISOString(),
      options: ['feature', 'bugfix'],
    },
  ]);
}

async function renderInbox(path = '/inbox'): Promise<void> {
  render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/inbox" element={<Inbox />} />
        <Route path="/needs-you" element={<div>needs you place</div>} />
      </Routes>
    </MemoryRouter>,
  );
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

function rows(): HTMLElement[] {
  return screen.queryAllByTestId('inbox-row');
}

function selectedRow(): HTMLElement {
  const sel = rows().find((r) => r.querySelector('[aria-current="true"]') !== null);
  if (sel === undefined) throw new Error('no selected row');
  return sel;
}

function primaries(): Element[] {
  return [...document.querySelectorAll('article [data-primary]')];
}

beforeEach(() => {
  vi.clearAllMocks();
  defaultFetchers();
});

afterEach(cleanup);

describe('Inbox', () => {
  it('lists five kinds in their groups, and no approval', async () => {
    await renderInbox();
    const list = rows();
    expect(list).toHaveLength(5);
    expect(list.map((r) => r.dataset.kind)).toEqual(['lesson', 'advisor', 'classifier', 'proposal', 'retire']);
    expect(screen.getByText(/5 to review · nothing here blocks a session/)).toBeTruthy();
    for (const word of ['lesson candidate', 'advisor noticed', 'check the classifier', 'agent change']) {
      expect(screen.getByText(word), word).toBeTruthy();
    }
    expect(screen.getByText('stop using a lesson?')).toBeTruthy();
    expect(list.some((r) => r.textContent?.includes('Bash'))).toBe(false);
    expect(document.body.textContent).not.toContain('AskUserQuestion');
  });

  it('moves the selection with j and k', async () => {
    await renderInbox();
    expect(selectedRow().dataset.kind).toBe('lesson');
    fireEvent.keyDown(window, { key: 'j' });
    expect(selectedRow().dataset.kind).toBe('advisor');
    fireEvent.keyDown(window, { key: 'j' });
    fireEvent.keyDown(window, { key: 'k' });
    expect(selectedRow().dataset.kind).toBe('advisor');
  });

  it('e runs the main action and x the secondary one', async () => {
    await renderInbox();
    await act(async () => {
      fireEvent.keyDown(window, { key: 'e' });
    });
    expect(lessons.acceptLesson).toHaveBeenCalledWith(5);
    cleanup();
    await renderInbox();
    await act(async () => {
      fireEvent.keyDown(window, { key: 'x' });
    });
    expect(lessons.dismissLesson).toHaveBeenCalledWith(5, 'not useful');
  });

  it('x skips a classifier question: there is no wrong answer to send', async () => {
    await renderInbox('/inbox');
    const row = rows().find((r) => r.dataset.kind === 'classifier') as HTMLElement;
    fireEvent.click(row.firstElementChild as HTMLElement);
    expect(selectedRow().dataset.kind).toBe('classifier');
    await act(async () => {
      fireEvent.keyDown(window, { key: 'x' });
    });
    expect(selectedRow().dataset.kind).toBe('proposal');
    expect(decisions.postGroundTruth).not.toHaveBeenCalled();
  });

  it('?tab=retire opens that tab: its rows alone, under no group head', async () => {
    await renderInbox('/inbox?tab=retire');
    expect(rows()).toHaveLength(1);
    expect(selectedRow().dataset.kind).toBe('retire');
    expect(screen.getByRole('tab', { name: /retire a lesson/ }).getAttribute('aria-selected')).toBe('true');
  });

  it('shows all, the kinds with something waiting, and handled by agent', async () => {
    await renderInbox('/inbox');
    const labels = screen.getAllByRole('tab').map((tab) => tab.textContent ?? '');
    expect(labels[0]).toMatch(/^all/);
    expect(labels[labels.length - 1]).toMatch(/^handled by agent/);
    expect(labels.some((label) => label.startsWith('approvals'))).toBe(false);
  });

  it('a tab with nothing in it says so', async () => {
    await renderInbox('/inbox?tab=reviews');
    expect(rows()).toHaveLength(0);
    expect(screen.getByText('Nothing here.')).toBeTruthy();
  });

  it('?tab=approvals goes to Needs you, where the approvals are', async () => {
    await renderInbox('/inbox?tab=approvals');
    expect(screen.getByText('needs you place')).toBeTruthy();
  });

  it('has one primary per kind, and no triage banner', async () => {
    await renderInbox();
    for (const kind of ['lesson', 'advisor', 'classifier', 'proposal', 'retire']) {
      expect(selectedRow().dataset.kind).toBe(kind);
      expect(primaries(), kind).toHaveLength(1);
      fireEvent.keyDown(window, { key: 'j' });
    }
    expect(screen.queryByText(/run triage/)).toBeNull();
  });

  it('says All reviewed. when nothing is left, an open approval included', async () => {
    const realLessons = vi.mocked(lessons.fetchLessons).getMockImplementation();
    const realRetirements = vi.mocked(lessons.fetchRetirements).getMockImplementation();
    vi.mocked(lessons.fetchLessons).mockImplementation(async () => []);
    vi.mocked(lessons.fetchRetirements).mockImplementation(async () => []);
    vi.mocked(api.fetchRecommendations).mockResolvedValue({ recommendations: [] } as never);
    vi.mocked(api.fetchProposals).mockResolvedValue({ proposals: [] } as never);
    vi.mocked(decisions.fetchLabelQueue).mockResolvedValue([]);
    await renderInbox();
    vi.mocked(lessons.fetchLessons).mockImplementation(realLessons as never);
    vi.mocked(lessons.fetchRetirements).mockImplementation(realRetirements as never);
    expect(rows()).toHaveLength(0);
    expect(screen.getAllByText('All reviewed.').length).toBeGreaterThan(0);
  });

  it('shows a failed source as a row, not a blank Inbox', async () => {
    vi.mocked(api.fetchProposals).mockRejectedValue(new Error('boom'));
    await renderInbox();
    expect(screen.getByRole('alert').textContent).toContain("couldn't load agent changes");
    expect(rows()).toHaveLength(4);
  });

  it('puts a paused account first, and e probes and resumes it', async () => {
    vi.mocked(alerts.fetchAlerts).mockResolvedValue([
      {
        id: 4,
        rule: 'account_breaker_open',
        target: 'account:work',
        severity: 'error',
        message: 'Claude refused this account. Runs on it are paused until a probe succeeds.',
        detectedAt: iso(-600),
        account: 'work',
        kind: 'auth',
        reason: "Claude refused this account's access",
        openedAt: iso(-600),
      },
    ]);
    await renderInbox();
    const list = rows();
    expect(list).toHaveLength(6);
    expect(list[0]?.textContent).toContain('Account work is paused');
    expect(selectedRow().textContent).toContain('Account work is paused');
    expect(primaries()).toHaveLength(1);
    expect(screen.getByRole('button', { name: /probe & resume/ })).toBeTruthy();
    await act(async () => {
      fireEvent.keyDown(window, { key: 'e' });
    });
    expect(alerts.resumeAccount).toHaveBeenCalledWith('work');
    // x is a no-op: an alert cannot be dismissed.
    cleanup();
    await renderInbox('/inbox?tab=alerts');
    expect(selectedRow().dataset.kind).toBe('alert');
    await act(async () => {
      fireEvent.keyDown(window, { key: 'x' });
    });
    expect(selectedRow().dataset.kind).toBe('alert');
  });

  it('lists an unacked plan review with its verdict and findings; e acks it and it leaves the list', async () => {
    const review: Review = {
      id: 12,
      scope: 'plan',
      taskId: 9,
      planTitle: 'Order line items',
      projectSlug: 'shop',
      phaseId: null,
      phaseName: '',
      sessionUuid: 'rev-1',
      runSessionUuid: '',
      branchSetKey: 'abc',
      verdict: 'fail',
      detail: '',
      findings: '- P0 api/orders.go:40 — phase 2 reads a column phase 1 never added\n- P1 web/x.ts:3 — wrong field\nVERDICT: FAIL',
      fixRound: 0,
      costUsd: null,
      treeBefore: 't',
      treeAfter: 't',
      startedAt: iso(-3600),
      finishedAt: iso(-3000),
      ackedAt: null,
    };
    vi.mocked(reviews.fetchUnackedPlanReviews).mockResolvedValueOnce([review]).mockResolvedValue([]);
    await renderInbox('/inbox?tab=reviews');
    expect(rows()).toHaveLength(1);
    const card = selectedRow().textContent ?? '';
    expect(card).toContain('Plan review: Order line items');
    expect(card).toContain('failed');
    expect(card).toContain('2 findings');
    const article = document.querySelector('article');
    expect(within(article as HTMLElement).getByLabelText('review findings').textContent).toContain(
      'phase 2 reads a column phase 1 never added',
    );
    expect(primaries()).toHaveLength(1);
    expect(screen.getByRole('button', { name: /^ack/ })).toBeTruthy();
    await act(async () => {
      fireEvent.keyDown(window, { key: 'e' });
    });
    expect(reviews.ackReview).toHaveBeenCalledWith(12);
    await act(async () => {
      await Promise.resolve();
    });
    expect(rows().some((r) => r.dataset.kind === 'review')).toBe(false);
  });

  it('shows an auto mode outage as an alert with nothing to press', async () => {
    const message =
      "9 permission checks got no verdict in the last 10 minutes across 2 sessions — Claude Code's server-side classifier is failing; affected sessions pause until it recovers.";
    vi.mocked(alerts.fetchAlerts).mockResolvedValue([
      {
        id: 5,
        rule: 'auto_mode_no_verdict',
        target: 'auto-mode-classifier',
        severity: 'warn',
        message,
        detectedAt: iso(-120),
      },
    ]);
    await renderInbox('/inbox?tab=alerts');
    expect(selectedRow().textContent).toContain('Permission checks are getting no verdict');
    // The daemon's sentence is the body; there is no button, and e does nothing.
    expect(document.querySelector('article')?.textContent).toContain(message);
    expect(primaries()).toHaveLength(0);
    expect(screen.queryByRole('button', { name: /probe & resume/ })).toBeNull();
    await act(async () => {
      fireEvent.keyDown(window, { key: 'e' });
    });
    expect(alerts.resumeAccount).not.toHaveBeenCalled();
  });

  describe('triage agent', () => {
    function verdict(over: Partial<TriageVerdict>): TriageVerdict {
      return {
        id: 1,
        runId: 7,
        kind: 'lesson',
        class: '',
        ref: '5',
        itemKey: '',
        title: 'Run the fixture generator first',
        value: 'accept',
        reason: 'Seen in three runs.',
        payload: null,
        prior: null,
        state: 'suggested',
        createdAt: new Date(Date.now() - 3600_000).toISOString(),
        decidedAt: null,
        projectId: null,
        ...over,
      };
    }

    function serve(byState: Partial<Record<string, TriageVerdict[]>>): void {
      vi.mocked(triage.fetchTriageVerdicts).mockImplementation(async (states) =>
        states.flatMap((st) => byState[st] ?? []),
      );
    }

    async function openLesson(): Promise<void> {
      await renderInbox('/inbox?tab=lessons');
    }

    it('shows the reasoning on a suggested lesson; e accepts the verdict, x still dismisses manually', async () => {
      serve({ suggested: [verdict({ id: 41 })] });
      await openLesson();
      expect(screen.getByText('agent suggests: accept')).toBeTruthy();
      expect(selectedRow().textContent).toContain('agent: accept');
      expect(screen.getByText('Seen in three runs.')).toBeTruthy();
      expect(screen.getByRole('button', { name: /^accept · agent's suggestion/ })).toBeTruthy();
      expect(screen.getByText('if you press e')).toBeTruthy();
      expect(screen.getByText(/Every future run touching/)).toBeTruthy();
      expect(primaries()).toHaveLength(1);
      await act(async () => {
        fireEvent.keyDown(window, { key: 'e' });
      });
      expect(triage.acceptTriageVerdict).toHaveBeenCalledWith(41);
      expect(lessons.acceptLesson).not.toHaveBeenCalled();
      cleanup();
      await openLesson();
      await act(async () => {
        fireEvent.keyDown(window, { key: 'x' });
      });
      expect(lessons.dismissLesson).toHaveBeenCalledWith(5, 'not useful');
    });

    it('a not-useful suggestion: the button, the box and e all say not useful', async () => {
      serve({ suggested: [verdict({ id: 42, value: 'not-useful' })] });
      await openLesson();
      expect(screen.getByRole('button', { name: /mark not useful/ })).toBeTruthy();
      expect(screen.getByText('The candidate is closed as not useful. Nothing is added to any brief.')).toBeTruthy();
      expect(screen.queryByText(/Every future run touching/)).toBeNull();
      expect(screen.getByText(/e mark not useful/)).toBeTruthy();
      await act(async () => {
        fireEvent.keyDown(window, { key: 'e' });
      });
      expect(triage.acceptTriageVerdict).toHaveBeenCalledWith(42);
    });

    it('keep, dismiss and fix-card suggestions each describe their own action', async () => {
      serve({ suggested: [verdict({ id: 43, kind: 'retire', ref: '8', value: 'keep' })] });
      await renderInbox('/inbox?tab=retire');
      expect(screen.getByRole('button', { name: /^keep it · agent's suggestion/ })).toBeTruthy();
      expect(
        screen.getByText('The proposal closes and this reason is held off for 30 days. The lesson stays in use.'),
      ).toBeTruthy();
      expect(screen.queryByText('if you confirm')).toBeNull();
      cleanup();

      serve({ suggested: [verdict({ id: 44, kind: 'advisor', ref: '6', value: 'dismiss' })] });
      await renderInbox('/inbox?tab=advisor');
      expect(screen.getByRole('button', { name: /^dismiss · agent's suggestion/ })).toBeTruthy();
      expect(screen.getByText(/The recommendation is closed\. It reopens on its own/)).toBeTruthy();
      cleanup();

      serve({
        suggested: [
          verdict({ id: 45, kind: 'advisor', ref: '6', value: 'fix-card', payload: { title: 'Fix it', prompt: 'Do it' } }),
        ],
      });
      await renderInbox('/inbox?tab=advisor');
      expect(screen.getByRole('button', { name: /^open the fix task · agent's suggestion/ })).toBeTruthy();
      expect(screen.getByText(/A task is created on the project's board with the text below/)).toBeTruthy();
    });


    it('answering a sampled classifier question reloads the triage state', async () => {
      serve({ sample: [verdict({ id: 51, kind: 'classifier', ref: '9', value: 'bugfix', state: 'sample' })] });
      vi.mocked(triage.fetchTriageAudit).mockResolvedValue({ answered: 10, agree: 9, byQuestion: [] });
      await renderInbox('/inbox?tab=classifier');
      expect(screen.getByText('agent matched you on 9 of 10')).toBeTruthy();
      const before = vi.mocked(triage.fetchTriageAudit).mock.calls.length;
      vi.mocked(triage.fetchTriageAudit).mockResolvedValue({ answered: 11, agree: 10, byQuestion: [] });
      await act(async () => {
        fireEvent.click(screen.getByRole('button', { name: 'feature' }));
      });
      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });
      expect(vi.mocked(triage.fetchTriageAudit).mock.calls.length).toBeGreaterThan(before);
      expect(screen.queryByText('agent matched you on 9 of 10')).toBeNull();
    });

    it('undo in the handled list reloads the Inbox items as well as the triage lists', async () => {
      serve({ applied: [verdict({ id: 62, state: 'applied', title: 'Dup lesson', value: 'dismiss' })] });
      await renderInbox('/inbox?tab=handled');
      const items = vi.mocked(api.fetchApprovals).mock.calls.length;
      const lists = vi.mocked(triage.fetchTriageVerdicts).mock.calls.length;
      await act(async () => {
        fireEvent.click(screen.getByRole('button', { name: /^undo/ }));
      });
      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });
      expect(vi.mocked(api.fetchApprovals).mock.calls.length).toBeGreaterThan(items);
      expect(vi.mocked(triage.fetchTriageVerdicts).mock.calls.length).toBeGreaterThan(lists);
    });

    it('an item without a suggestion behaves as before', async () => {
      await openLesson();
      expect(screen.queryByText(/agent suggests/)).toBeNull();
      expect(screen.getByText('if you accept')).toBeTruthy();
      expect(screen.queryByText('if you press e')).toBeNull();
      expect(screen.getByText('e accept')).toBeTruthy();
      await act(async () => {
        fireEvent.keyDown(window, { key: 'e' });
      });
      expect(lessons.acceptLesson).toHaveBeenCalledWith(5);
      expect(triage.acceptTriageVerdict).not.toHaveBeenCalled();
    });

    it('marks the agent label on a sampled classifier item; e posts the local answers', async () => {
      serve({ sample: [verdict({ id: 51, kind: 'classifier', ref: '9', value: 'bugfix', state: 'sample' })] });
      vi.mocked(triage.fetchTriageAudit).mockResolvedValue({ answered: 10, agree: 9, byQuestion: [] });
      await renderInbox('/inbox?tab=classifier');
      expect(screen.getByRole('button', { name: /bugfix.*agent says/ })).toBeTruthy();
      expect(screen.getByRole('button', { name: 'feature' })).toBeTruthy();
      expect(screen.getByText('agent matched you on 9 of 10')).toBeTruthy();
      await act(async () => {
        fireEvent.keyDown(window, { key: 'e' });
      });
      expect(decisions.postGroundTruth).toHaveBeenCalledWith(9, 'feature');
      expect(triage.acceptTriageVerdict).not.toHaveBeenCalled();
    });

    it('lists applied verdicts under the handled tab and undo calls the endpoint', async () => {
      serve({ applied: [verdict({ id: 61, state: 'applied', title: 'Dup lesson', value: 'dismiss' })] });
      await renderInbox('/inbox?tab=handled');
      expect(screen.getByRole('tab', { name: /handled by agent/ }).getAttribute('aria-selected')).toBe('true');
      expect(screen.getAllByTestId('inbox-handled-row')).toHaveLength(1);
      expect(screen.getByText('e undo')).toBeTruthy();
      await act(async () => {
        fireEvent.keyDown(window, { key: 'e' });
      });
      expect(triage.undoTriageVerdict).toHaveBeenCalledWith(61);
    });
  });
});
