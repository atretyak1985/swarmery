// @vitest-environment jsdom
//
// The Inbox page (Canvas v3 phase 3). The claims:
//
//   1. Six kinds from six mocked fetchers (two approvals) render as seven rows,
//      urgent first.
//   2. j / k move the selection.
//   3. e on an approval calls resolveApproval(id, 'approve'); x calls 'deny'.
//   4. ?tab=lessons shows lessons only.
//   5. Each kind's detail has exactly ONE primary button; an AskUserQuestion
//      approval renders QuestionForm instead of approve.
//   6. A failed source is a "couldn't load" row, not a blank Inbox.
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
import { TRIAGE_INBOX_KINDS } from './inboxModel';

const NOW = Date.now();
// The fixture's agent-eligible items: one classifier guess, one advisor finding,
// one lesson and one retirement (the banner's shown count, and the run's cap).
const AGENT_ELIGIBLE = 4;
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
      </Routes>
    </MemoryRouter>,
  );
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

function rows(): HTMLElement[] {
  return within(screen.getByRole('listbox', { name: 'waiting decisions' })).getAllByRole('option');
}

function selectedRow(): HTMLElement {
  const sel = rows().find((r) => r.getAttribute('aria-selected') === 'true');
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
  it('renders the six kinds, urgent approvals first', async () => {
    await renderInbox();
    const list = rows();
    expect(list).toHaveLength(7);
    expect(list[0]?.textContent).toContain('Bash');
    expect(list[1]?.textContent).toContain('AskUserQuestion');
    expect(list[0]?.textContent).toContain('expires soon');
    expect(screen.getByText(/7 waiting/)).toBeTruthy();
    for (const word of ['Bash', 'New lesson', 'Advisor', 'Change to', 'Check the classifier', 'Stop using']) {
      expect(list.some((r) => r.textContent?.includes(word)), word).toBe(true);
    }
  });

  it('moves the selection with j and k', async () => {
    await renderInbox();
    expect(selectedRow().textContent).toContain('Bash');
    fireEvent.keyDown(window, { key: 'j' });
    expect(selectedRow().textContent).toContain('AskUserQuestion');
    fireEvent.keyDown(window, { key: 'j' });
    fireEvent.keyDown(window, { key: 'k' });
    expect(selectedRow().textContent).toContain('AskUserQuestion');
  });

  it('e approves and x denies the selected approval', async () => {
    await renderInbox();
    await act(async () => {
      fireEvent.keyDown(window, { key: 'e' });
    });
    expect(api.resolveApproval).toHaveBeenCalledWith(1, 'approve');
    cleanup();
    await renderInbox();
    await act(async () => {
      fireEvent.keyDown(window, { key: 'x' });
    });
    expect(api.resolveApproval).toHaveBeenCalledWith(1, 'deny');
  });

  it('?tab=lessons filters to lessons', async () => {
    await renderInbox('/inbox?tab=lessons');
    const list = rows();
    expect(list).toHaveLength(1);
    expect(list[0]?.textContent).toContain('New lesson');
    expect(screen.getByRole('tab', { name: /lessons/ }).getAttribute('aria-selected')).toBe('true');
  });

  it('renders one primary button per kind, QuestionForm for AskUserQuestion', async () => {
    await renderInbox();
    const seen: string[] = [];
    for (let i = 0; i < 7; i += 1) {
      const label = selectedRow().textContent ?? '';
      seen.push(label);
      if (label.includes('AskUserQuestion')) {
        expect(primaries()).toHaveLength(0);
        expect(screen.getByRole('button', { name: /submit answers/ })).toBeTruthy();
        expect(screen.queryByRole('button', { name: /^approve/ })).toBeNull();
      } else {
        expect(primaries(), label).toHaveLength(1);
      }
      fireEvent.keyDown(window, { key: 'j' });
    }
    expect(new Set(seen)).toHaveProperty('size', 7);
  });

  it('shows a failed source as a row, not a blank Inbox', async () => {
    vi.mocked(api.fetchProposals).mockRejectedValue(new Error('boom'));
    await renderInbox();
    expect(screen.getByRole('alert').textContent).toContain("couldn't load proposals");
    expect(rows()).toHaveLength(6);
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
    expect(list).toHaveLength(8);
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
    expect(rows()).toHaveLength(1);
    await act(async () => {
      fireEvent.keyDown(window, { key: 'x' });
    });
    expect(api.resolveApproval).not.toHaveBeenCalled();
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
    expect(card).toContain('review failed');
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
    expect(screen.queryByRole('listbox', { name: 'waiting decisions' })).toBeNull();
    expect(screen.getByText('Nothing is waiting on you.')).toBeTruthy();
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
    expect(rows()).toHaveLength(1);
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

    it('drops the old header sentence', async () => {
      await renderInbox();
      expect(screen.queryByText(/Everything the system/)).toBeNull();
      expect(screen.getByText(/Decisions that wait on you\./)).toBeTruthy();
    });

    it('offers a run for an old agent-eligible item and starts it unscoped on /inbox', async () => {
      await renderInbox();
      expect(screen.getByText(`${String(AGENT_ELIGIBLE)} can be handled by an agent · 3 need you`)).toBeTruthy();
      await act(async () => {
        fireEvent.click(screen.getByRole('button', { name: 'run triage' }));
      });
      expect(triage.startTriageRun).toHaveBeenCalledWith(null, { kinds: TRIAGE_INBOX_KINDS, cap: AGENT_ELIGIBLE });
    });

    it('starts the run with the project slug on /p/:slug/inbox', async () => {
      render(
        <MemoryRouter initialEntries={['/p/shop/inbox']}>
          <Routes>
            <Route path="/p/:slug/inbox" element={<Inbox />} />
          </Routes>
        </MemoryRouter>,
      );
      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });
      // The project's recommendations are not mocked, so the advisor finding is
      // absent here: the banner shows 3, and 3 is the cap the click sends.
      expect(screen.getByText('3 can be handled by an agent · 3 need you')).toBeTruthy();
      await act(async () => {
        fireEvent.click(screen.getByRole('button', { name: 'run triage' }));
      });
      expect(triage.startTriageRun).toHaveBeenCalledWith('shop', { kinds: TRIAGE_INBOX_KINDS, cap: 3 });
    });

    function triageRun(over: Record<string, unknown>): never {
      return { id: 9, status: 'running', error: '', total: 4, done: 0, applied: 0, suggested: 0, failed: 0, ...over } as never;
    }

    it('a failed run shows its error; dismissing it removes the whole line and keeps the offer', async () => {
      vi.mocked(triage.startTriageRun).mockResolvedValueOnce({ id: 9 });
      vi.mocked(triage.fetchTriageRun).mockResolvedValueOnce(triageRun({ status: 'failed', error: 'budget exhausted' }));
      await renderInbox();
      await act(async () => {
        fireEvent.click(screen.getByRole('button', { name: 'run triage' }));
      });
      expect(await screen.findByText('budget exhausted')).toBeTruthy();
      expect(screen.queryByText(/agent closed/)).toBeNull();
      await act(async () => {
        fireEvent.click(screen.getByRole('button', { name: 'dismiss error' }));
      });
      expect(screen.queryByText('budget exhausted')).toBeNull();
      expect(screen.queryByText('The triage run failed.')).toBeNull();
      expect(screen.queryByRole('button', { name: 'retry' })).toBeNull();
      expect(screen.getByRole('button', { name: 'run triage' })).toBeTruthy();
    });

    it('retry after a failed run shows the new run only, never the old failure beside it', async () => {
      vi.mocked(triage.startTriageRun).mockResolvedValueOnce({ id: 9 });
      vi.mocked(triage.fetchTriageRun).mockResolvedValueOnce(triageRun({ status: 'failed', error: 'budget exhausted' }));
      await renderInbox();
      await act(async () => {
        fireEvent.click(screen.getByRole('button', { name: 'run triage' }));
      });
      expect(await screen.findByText('budget exhausted')).toBeTruthy();

      vi.mocked(triage.startTriageRun).mockResolvedValueOnce({ id: 10 });
      vi.mocked(triage.fetchActiveTriageRun).mockResolvedValue(triageRun({ id: 10 }));
      await act(async () => {
        fireEvent.click(screen.getByRole('button', { name: 'retry' }));
      });
      expect(await screen.findByText('triage running · 0 of 4')).toBeTruthy();
      expect(screen.queryByText('budget exhausted')).toBeNull();
      expect(screen.queryByRole('button', { name: 'retry' })).toBeNull();
      expect(screen.queryByRole('button', { name: 'run triage' })).toBeNull();
      // The retry sends the same body as the first click.
      expect(vi.mocked(triage.startTriageRun).mock.calls).toEqual([
        [null, { kinds: TRIAGE_INBOX_KINDS, cap: AGENT_ELIGIBLE }],
        [null, { kinds: TRIAGE_INBOX_KINDS, cap: AGENT_ELIGIBLE }],
      ]);
    });

    it('shows the reasoning on a suggested lesson; e accepts the verdict, x still dismisses manually', async () => {
      serve({ suggested: [verdict({ id: 41 })] });
      await openLesson();
      expect(screen.getByText('agent suggests: accept')).toBeTruthy();
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

    it('accept all confirms the open suggestions except fix tasks, one call each, and says so first', async () => {
      serve({
        suggested: [
          verdict({ id: 71, value: 'accept' }),
          verdict({ id: 72, ref: '99', value: 'not-useful' }),
          verdict({ id: 73, kind: 'advisor', ref: '6', value: 'fix-card' }),
          verdict({ id: 74, kind: 'retire', ref: '8', value: 'keep' }),
        ],
      });
      vi.mocked(triage.acceptTriageVerdict).mockImplementation(async (id: number) => {
        if (id === 72) throw new Error('boom');
        return {} as never;
      });
      await renderInbox();
      expect(screen.getByText(/4 suggestions from the agent/)).toBeTruthy();
      expect(screen.getByText(/1 accept · 1 not useful · 1 keep it/)).toBeTruthy();
      expect(screen.getByText(/1 fix task to read first/)).toBeTruthy();
      await act(async () => {
        fireEvent.click(screen.getByRole('button', { name: 'accept 3 suggestions' }));
      });
      expect(triage.acceptTriageVerdict).toHaveBeenCalledTimes(3);
      const ids = vi.mocked(triage.acceptTriageVerdict).mock.calls.map((c) => c[0]);
      expect(ids).toEqual([71, 72, 74]);
      expect(ids).not.toContain(73);
      expect(triage.acceptAllTriageVerdicts).not.toHaveBeenCalled();
      expect(screen.getByText('accepted 2 · 1 failed')).toBeTruthy();
    });

    it('with only fix-task suggestions there is no accept-all control', async () => {
      serve({ suggested: [verdict({ id: 73, kind: 'advisor', ref: '6', value: 'fix-card' })] });
      await renderInbox();
      expect(screen.getByText(/1 fix task to read first/)).toBeTruthy();
      expect(screen.queryByRole('button', { name: /^accept \d+ suggestion/ })).toBeNull();
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
        fireEvent.click(screen.getByRole('button', { name: 'undo Dup lesson' }));
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
      expect(screen.getByText('e approve')).toBeTruthy();
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
      expect(screen.getByRole('tab', { name: /handled by agent/ }).textContent).toContain('1');
      await act(async () => {
        fireEvent.click(screen.getByRole('button', { name: 'undo Dup lesson' }));
      });
      expect(triage.undoTriageVerdict).toHaveBeenCalledWith(61);
    });
  });
});
