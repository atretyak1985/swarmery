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
// Dev-only suite. Run with
//   npx vitest run src/pages/inbox
// after `npm i --no-save vitest jsdom @testing-library/react @testing-library/dom`.

import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../../api';
import * as decisions from '../../api/decisions';
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

vi.mock('../../lib/ws', () => ({ useLiveUpdates: () => undefined }));

function defaultFetchers(): void {
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
});
