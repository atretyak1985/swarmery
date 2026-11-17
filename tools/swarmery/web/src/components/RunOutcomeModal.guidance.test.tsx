// @vitest-environment jsdom
//
// RunOutcomeModal says WHAT blocks and HOW to resolve it. The case it was
// rebuilt for: a phase stopped on `criterion-impossible`, its own branch holds
// the stopped run's commit, and two orphan branches from an older id generation
// sit in the repo. The modal must lead with the decision, answer it in place,
// keep the run's own branch out of the "delete it" advice, file the orphans
// under "does not block", and give every branch its own delete button.
//
// Runs with the rest of the web suite: `npm test`.

import { cleanup, fireEvent, render, screen, waitFor, within } from '../test/render';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { NeedsYouDecision, PhaseBlocker, PhaseDiagnosis } from '../api/types';
import { RunOutcomeModal } from './RunOutcomeModal';
import { withoutStopBlock } from './runOutcome/blockerGuide';

vi.mock('../workspace/ProjectContext', () => ({
  useProjectWorkspace: () => ({
    slug: 'swarmery',
    project: { id: 3, slug: 'swarmery', name: 'swarmery' },
    projectId: 3,
    loading: false,
  }),
}));

const DECISION_OPEN: PhaseBlocker = {
  kind: 'decision-open',
  summary: 'The run stopped (criterion-impossible) and is waiting for your answer to D-4',
  detail: 'npm run screenshots fails on unmodified main',
  decisionId: 'D-4',
  trigger: 'criterion-impossible',
};
const OWN_BRANCH: PhaseBlocker = {
  kind: 'branch-awaits-decision',
  summary: "Branch swarm/phase-52170 holds the stopped run's 1 commit",
  detail: 'feat(swarmery): base wash\nmeasured against the currently checked-out branch main',
  branch: 'swarm/phase-52170',
  commitsAhead: 1,
  base: 'main',
};
const orphan = (id: number, n: number): PhaseBlocker => ({
  kind: 'orphan-branch',
  summary: `swarm/phase-${String(id)} holds ${String(n)} commits but matches no phase in this plan`,
  detail: `swarm/phase-${String(id)} → ${String(n)} commits ahead`,
  branch: `swarm/phase-${String(id)}`,
  commitsAhead: n,
  base: 'main',
});

const DECISION: NeedsYouDecision = {
  taskId: 77,
  phaseId: 11,
  decisionId: 'D-4',
  source: 'stop',
  question: 'npm run screenshots fails on unmodified main',
  reason: 'npm run screenshots fails on unmodified main',
  trigger: 'criterion-impossible',
  freeText: true,
  guard: {} as NeedsYouDecision['guard'],
  options: [
    { id: 'fix-mobile-docs-scroll', label: 'fold the bug into this phase', consequence: 'PlaceLayout.tsx changes too' },
    { id: 'accept-preexisting-residual', label: 'land as scoped', consequence: 'screenshots stay red on one assertion' },
  ],
};

const STOP_TEXT =
  "Committed clean. I'm stopping here.\n\n```yaml\nkind: stop\ntrigger: criterion-impossible\noptions:\n  - id: fix";

const diagnosis = (blockers: PhaseBlocker[]): PhaseDiagnosis => ({
  phaseId: 11,
  seq: 1,
  name: 'Frame',
  runOutcome: 'partial',
  criteriaTotal: 10,
  criteriaBefore: 0,
  criteriaAfter: 9,
  runStartedAt: null,
  runEndedAt: null,
  runError: null,
  verifyVerdict: null,
  verifyDetail: null,
  blockers,
  agentMessage: { sessionUuid: 's-1', text: STOP_TEXT, truncated: true },
});

let diag: PhaseDiagnosis;
let fetchMock: ReturnType<typeof vi.fn>;

beforeEach(() => {
  diag = diagnosis([DECISION_OPEN, OWN_BRANCH, orphan(45263, 5), orphan(49764, 8)]);
  fetchMock = vi.fn((url: string, init?: RequestInit) => {
    if (init?.method === 'POST') {
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) } as Response);
    }
    void url;
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(diag) } as Response);
  });
  vi.stubGlobal('fetch', fetchMock);
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function mount(decision: NeedsYouDecision | null = DECISION, onChanged = vi.fn()) {
  render(
    <MemoryRouter>
      <RunOutcomeModal
        taskId={77}
        phaseId={11}
        writesDisabled={false}
        writesDisabledReason=""
        onClose={vi.fn()}
        onRetry={vi.fn()}
        onOpenRevisions={vi.fn()}
        decision={decision}
        onChanged={onChanged}
      />
    </MemoryRouter>,
  );
  return onChanged;
}

describe('RunOutcomeModal — blocker guidance', () => {
  it('leads with the decision and refuses Retry until it is answered', async () => {
    mount();
    const next = await screen.findByTestId('next-step');
    expect(next.textContent).toContain("Answer the executor's decision below");
    const retry = screen.getByRole('button', { name: 'Retry run' }) as HTMLButtonElement;
    expect(retry.disabled).toBe(true);
    expect(retry.getAttribute('data-tip')).toContain('answer the executor');
  });

  it("keeps the stopped run's branch out of the delete advice", async () => {
    mount();
    const card = (await screen.findByText(/The stopped run's work/)).closest('li') as HTMLElement;
    expect(card.textContent).toContain('Keep it');
    // The destructive option is there, but named for what it does.
    expect(within(card).getByRole('button', { name: 'Discard this work' })).toBeTruthy();
    expect(within(card).queryByRole('button', { name: 'Delete branch' })).toBeNull();
  });

  it('files orphan branches under "does not block", each with its own delete', async () => {
    mount();
    await screen.findByText('for information — does not block this phase');
    for (const id of [45263, 49764]) {
      const card = screen.getAllByText(new RegExp(`^swarm/phase-${String(id)} holds`))[0]?.closest('li') as HTMLElement;
      expect(card.getAttribute('data-kind')).toBe('orphan-branch');
      expect(card.textContent).toContain(`git log --oneline main..swarm/phase-${String(id)}`);
      expect(within(card).getByRole('button', { name: 'Delete branch' })).toBeTruthy();
    }
  });

  it('arms the delete of exactly the branch whose button was pressed', async () => {
    mount();
    const card = (await screen.findAllByText(/^swarm\/phase-49764 holds/))[0]?.closest('li') as HTMLElement;
    fireEvent.click(within(card).getByRole('button', { name: 'Delete branch' }));
    expect(card.textContent).toContain('Delete swarm/phase-49764?');
    expect(screen.queryByText('Delete swarm/phase-45263?')).toBeNull();
  });

  it('answers the decision in place and refreshes', async () => {
    const onChanged = mount();
    const option = await screen.findByLabelText(/land as scoped/);
    expect(screen.getByText(/screenshots stay red on one assertion/)).toBeTruthy();
    fireEvent.click(option);
    fireEvent.click(screen.getByRole('button', { name: 'Answer' }));
    await waitFor(() => expect(onChanged).toHaveBeenCalledTimes(1));
    const post = fetchMock.mock.calls.find(([, init]) => (init as RequestInit | undefined)?.method === 'POST');
    expect(String(post?.[0])).toContain('/api/epics/77/phases/11/decisions/D-4');
    expect(post).toBeDefined();
    expect(JSON.parse(String((post?.[1] as RequestInit | undefined)?.body))).toEqual({
      option: 'accept-preexisting-residual',
      note: '',
    });
  });

  it('links out when the page has no matching decision to answer', async () => {
    mount(null);
    expect(await screen.findByRole('link', { name: /open Needs you/ })).toBeTruthy();
  });

  it('hides the raw stop YAML the decision card already shows', async () => {
    mount();
    await screen.findByText("Committed clean. I'm stopping here.");
    expect(screen.queryByText(/kind: stop/)).toBeNull();
  });

  it('asks to delete or merge a foreign leftover before a retry', async () => {
    diag = diagnosis([{ ...OWN_BRANCH, kind: 'branch-dirty', summary: 'Branch holds 1 commit' }]);
    diag.agentMessage = null;
    mount(null);
    const next = await screen.findByTestId('next-step');
    expect(next.textContent).toContain('Resolve what blocks a retry');
    const card = screen.getByText(/A previous run left/).closest('li') as HTMLElement;
    expect(card.textContent).toContain('git merge --no-ff swarm/phase-52170');
    expect((screen.getByRole('button', { name: 'Retry run' }) as HTMLButtonElement).disabled).toBe(true);
  });
});

describe('RunOutcomeModal — an answered stop', () => {
  it('says a retry continues on the branch and leaves Retry enabled', async () => {
    diag = diagnosis([
      { ...OWN_BRANCH, kind: 'branch-continues', summary: 'Retry continues', decisionId: 'D-1' },
    ]);
    diag.agentMessage = null;
    mount(null);
    const next = await screen.findByTestId('next-step');
    expect(next.textContent).toContain('Nothing in the repo stops a retry');
    const card = screen.getByText(/^Retry run continues on swarm\/phase-52170/).closest('li') as HTMLElement;
    expect(within(card).getByRole('button', { name: 'Discard this work' })).toBeTruthy();
    expect((screen.getByRole('button', { name: 'Retry run' }) as HTMLButtonElement).disabled).toBe(false);
  });

  it('names the decision whose answer left protected edits unaccepted', async () => {
    diag = diagnosis([{ ...OWN_BRANCH, kind: 'branch-dirty', summary: 'x', decisionId: 'D-1' }]);
    diag.agentMessage = null;
    mount(null);
    expect(await screen.findByText(/Your answer to D-1 did not accept the protected edits/)).toBeTruthy();
  });
});

describe('RunOutcomeModal — keep-the-work action', () => {
  // POSTs answered per route: a phase land returns its landing, an orphan land
  // its change request; anything else is the decision answer.
  function routeFetch(landStatus = 200, landBody: unknown = null): void {
    fetchMock.mockImplementation((url: string, init?: RequestInit) => {
      if (init?.method === 'POST' && url.includes('/orphan-branch/land')) {
        return Promise.resolve({
          ok: landStatus < 400,
          status: landStatus,
          json: () =>
            Promise.resolve(landBody ?? { branch: 'swarm/phase-45263', base: 'main', url: 'https://x/pull/9', number: 9 }),
        } as Response);
      }
      if (init?.method === 'POST' && url.includes('/land')) {
        return Promise.resolve({
          ok: landStatus < 400,
          status: landStatus,
          json: () =>
            Promise.resolve(
              landBody ?? { branch: 'swarm/phase-52170', base: 'main', action: 'pr', landing: { prUrl: 'https://x/pull/5', prNumber: 5 } },
            ),
        } as Response);
      }
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(diag) } as Response);
    });
  }

  it('opens a change request for a leftover branch through its phase land route', async () => {
    diag = diagnosis([{ ...OWN_BRANCH, kind: 'branch-dirty', summary: 'x' }]);
    diag.agentMessage = null;
    routeFetch();
    const onChanged = mount(null);
    const card = (await screen.findByText(/A previous run left/)).closest('li') as HTMLElement;
    fireEvent.click(within(card).getByRole('button', { name: 'Open CR' }));
    const link = await within(card).findByRole('link', { name: 'CR #5' });
    expect(link.getAttribute('href')).toBe('https://x/pull/5');
    const post = fetchMock.mock.calls.find(([u, init]) => String(u).includes('/land') && init?.method === 'POST');
    expect(String(post?.[0])).toContain('/api/epics/77/phases/52170/land');
    expect(JSON.parse(String((post?.[1] as RequestInit | undefined)?.body))).toEqual({ action: 'pr' });
    expect(onChanged).toHaveBeenCalled();
    // The commands stay, folded under the button as the by-hand fallback.
    expect(within(card).getByText('or do it by hand in the repo')).toBeTruthy();
  });

  it('opens a change request for an orphan through the orphan route, naming the branch', async () => {
    routeFetch();
    mount();
    const card = (await screen.findByText(/swarm\/phase-45263 holds 5 commits from an older run/)).closest(
      'li',
    ) as HTMLElement;
    fireEvent.click(within(card).getByRole('button', { name: 'Open CR' }));
    expect(await within(card).findByRole('link', { name: 'CR #9' })).toBeTruthy();
    const post = fetchMock.mock.calls.find(([u]) => String(u).includes('/orphan-branch/land'));
    expect(String(post?.[0])).toContain('/api/epics/77/orphan-branch/land?branch=swarm%2Fphase-45263');
  });

  it("offers no change request on the stopped run's own branch — the decision comes first", async () => {
    mount();
    const card = (await screen.findByText(/The stopped run's work/)).closest('li') as HTMLElement;
    expect(within(card).queryByRole('button', { name: 'Open CR' })).toBeNull();
  });

  it('shows the manual commands a refused land carries', async () => {
    diag = diagnosis([{ ...OWN_BRANCH, kind: 'branch-dirty', summary: 'x' }]);
    diag.agentMessage = null;
    routeFetch(422, { error: 'not authenticated', code: 'not-authenticated', hint: 'gh auth login --hostname github.com' });
    mount(null);
    const card = (await screen.findByText(/A previous run left/)).closest('li') as HTMLElement;
    fireEvent.click(within(card).getByRole('button', { name: 'Open CR' }));
    const alert = await within(card).findByRole('alert');
    expect(alert.textContent).toContain('gh auth login --hostname github.com');
  });
});

describe('withoutStopBlock', () => {
  it('cuts the fenced and the bare stop block, and keeps text without one', () => {
    expect(withoutStopBlock(STOP_TEXT)).toBe("Committed clean. I'm stopping here.");
    expect(withoutStopBlock('done.\nkind: stop\ntrigger: x')).toBe('done.');
    expect(withoutStopBlock('kind: stop\ntrigger: x')).toBe('kind: stop\ntrigger: x');
    expect(withoutStopBlock('no block here')).toBe('no block here');
  });
});
