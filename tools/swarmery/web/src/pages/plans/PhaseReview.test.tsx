// @vitest-environment jsdom
//
// The phase Review tab (Phase 5, SC-6 / SC-11): what it offers in each landing
// state, what it sends, and how it renders a refusal. The API module is mocked
// except LandError, which is the real class (the component tells a 409 from a
// 422 by `instanceof`).

import { act, cleanup, fireEvent, render, screen, waitFor, within } from '../../test/render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Epic, EpicPhase, NeedsYouDecision, PhaseLanding, PhaseReview as PhaseReviewData, ProviderTerms } from '../../api/types';
import { PhaseReview } from './PhaseReview';

const api = vi.hoisted(() => ({
  getPhaseReview: vi.fn(),
  landPhase: vi.fn(),
  refreshPhaseLanding: vi.fn(),
}));

vi.mock('../../api', async (importOriginal) => {
  const real = await importOriginal<typeof import('../../api')>();
  return {
    ...real,
    getPhaseReview: api.getPhaseReview,
    landPhase: api.landPhase,
    refreshPhaseLanding: api.refreshPhaseLanding,
  };
});

const guardApi = vi.hoisted(() => ({ answerGuardDecision: vi.fn() }));
vi.mock('../../api/guard', async (importOriginal) => {
  const real = await importOriginal<typeof import('../../api/guard')>();
  return { ...real, answerGuardDecision: guardApi.answerGuardDecision };
});

const { LandError } = await import('../../api');

afterEach(cleanup);

// Two neutral vocabularies: the component may only ever read them.
const PR_TERMS: ProviderTerms = { provider: 'Host A', change: 'Pull Request', changeShort: 'PR' };
const MR_TERMS: ProviderTerms = { provider: 'Host B', change: 'Merge Request', changeShort: 'MR' };

const landing = (over: Partial<PhaseLanding> = {}): PhaseLanding => ({
  state: 'ready',
  prUrl: null,
  prNumber: null,
  prProvider: null,
  prStatus: null,
  landedAt: null,
  error: null,
  ...over,
});

const phase = (over: Partial<EpicPhase> = {}): EpicPhase =>
  ({
    id: 207,
    seq: 2,
    name: 'Phase 2 — Line-item CRUD',
    runState: 'done',
    runSessionUuid: 'sess-207',
    verifyVerdict: 'pass',
    verifyDetail: 'all green',
    landing: landing(),
    ...over,
  }) as unknown as EpicPhase;

const EPIC = { taskId: 9, phases: [] } as unknown as Epic;

const review = (over: Partial<PhaseReviewData> = {}): PhaseReviewData => ({
  base: '0123456789abcdef',
  branch: 'swarm/phase-207',
  headerBranch: '',
  commits: [{ sha: 'deadbeefcafe', subject: 'add line items' }],
  files: [{ path: 'orders/line_items.go', additions: 12, deletions: 1 }],
  patch: 'diff --git a/orders/line_items.go b/orders/line_items.go\n+x',
  patchTruncated: false,
  verifyVerdict: 'pass',
  verifyDetail: 'all green',
  landing: landing(),
  terms: PR_TERMS,
  ...over,
});

beforeEach(() => {
  api.getPhaseReview.mockReset();
  api.landPhase.mockReset();
  api.refreshPhaseLanding.mockReset();
  guardApi.answerGuardDecision.mockReset();
  api.getPhaseReview.mockResolvedValue(review());
});

const guardDecision = (over: Partial<NeedsYouDecision> = {}): NeedsYouDecision => ({
  taskId: 9,
  phaseId: 207,
  decisionId: 'D-4',
  question: 'The run touched protected paths: trap_test.go. Allow (add to scope) or reject?',
  options: [
    { id: 'allow-add-to-scope', label: 'Allow · add to scope' },
    { id: 'reject', label: 'Reject' },
  ],
  guard: { protectedChanged: ['trap_test.go'] },
  paths: ['trap_test.go'],
  ...over,
});

function renderReview(p: EpicPhase = phase(), terms: ProviderTerms | null = PR_TERMS, onLanded = vi.fn()) {
  render(<PhaseReview epic={EPIC} phase={p} terms={terms} onLanded={onLanded} />);
  return { onLanded };
}

const button = (name: string | RegExp): HTMLButtonElement => screen.getByRole('button', { name }) as HTMLButtonElement;

describe('PhaseReview', () => {
  it('ready: shows the diff, the verdict, the strip and Push / Push + open <change> (+Draft) / Return', async () => {
    renderReview();
    expect(await screen.findByText('add line items')).toBeTruthy();
    expect(api.getPhaseReview).toHaveBeenCalledWith(9, 207);
    expect(screen.getByText('ready to land')).toBeTruthy();
    expect(screen.getByText('verified')).toBeTruthy();
    expect(button('Push').disabled).toBe(false);
    expect(button('Push + open Pull Request').disabled).toBe(false);
    expect((screen.getByRole('checkbox', { name: 'Draft' }) as HTMLInputElement).checked).toBe(false);
    expect(button('Return to agent…').disabled).toBe(false);
  });

  it('labels every control from terms, with no provider branching', async () => {
    api.getPhaseReview.mockResolvedValue(review({ terms: MR_TERMS }));
    renderReview(phase(), MR_TERMS);
    expect(await screen.findByRole('button', { name: 'Push + open Merge Request' })).toBeTruthy();
  });

  it('falls back to the review response terms until the project terms load', async () => {
    api.getPhaseReview.mockResolvedValue(review({ terms: MR_TERMS }));
    renderReview(phase(), null);
    expect(await screen.findByRole('button', { name: 'Push + open Merge Request' })).toBeTruthy();
  });

  it('pr_open: links the change request with text built from terms', async () => {
    const open = landing({ state: 'pr_open', prUrl: 'https://host.example/acme/w/pull/77', prNumber: 77 });
    api.getPhaseReview.mockResolvedValue(review({ landing: open }));
    renderReview(phase({ landing: open }));
    await screen.findByText('add line items');
    const pr = screen.getByTestId('review-ledger-pr');
    expect(within(pr).getByText('PR #77')).toBeTruthy();
    const link = within(pr).getByRole('link', { name: 'open ↗' }) as HTMLAnchorElement;
    expect(link.href).toBe('https://host.example/acme/w/pull/77');
    expect(screen.getByTestId('review-ledger-branch').textContent).toContain('PR open');
    // A change request is already open: Push is no longer offered.
    expect(screen.queryByRole('button', { name: 'Push' })).toBeNull();
  });

  it('pr_open embeds the status chips; a refresh that finds it merged replaces the strip and fires onLanded', async () => {
    const checkedAt = new Date().toISOString();
    const open = landing({
      state: 'pr_open',
      prUrl: 'https://host.example/acme/w/pull/77',
      prNumber: 77,
      prStatus: { state: 'open', draft: false, ci: 'pending', review: 'review_required', checkedAt },
    });
    api.getPhaseReview.mockResolvedValue(review({ landing: open }));
    const merged = landing({
      ...open,
      state: 'merged',
      landedAt: checkedAt,
      prStatus: { state: 'merged', draft: false, ci: 'success', review: 'approved', checkedAt },
    });
    api.refreshPhaseLanding.mockResolvedValue(merged);
    const { onLanded } = renderReview(phase({ landing: open }));

    expect((await screen.findByTestId('review-ledger-ci')).textContent).toContain('CI running');
    expect(screen.queryByTestId('review-ledger-branch')?.textContent).not.toMatch(/merged \d/);
    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }));
    await waitFor(() => expect(onLanded).toHaveBeenCalledTimes(1));
    expect(api.refreshPhaseLanding).toHaveBeenCalledWith(EPIC.taskId, 207);
    expect(screen.getByTestId('review-ledger-ci').textContent).toContain('CI passed');
    // landedAt (the merge time) shows only now that the phase is merged.
    expect(screen.getByTestId('review-ledger-branch').textContent).toMatch(/merged \d+ s ago/);
  });

  it('pr_open under the other vocabulary reads MR #n', async () => {
    const open = landing({ state: 'pr_open', prUrl: 'https://host.example/acme/w/-/merge_requests/12', prNumber: 12 });
    api.getPhaseReview.mockResolvedValue(review({ landing: open, terms: MR_TERMS }));
    renderReview(phase({ landing: open }), MR_TERMS);
    await screen.findByText('add line items');
    expect(within(screen.getByTestId('review-ledger-pr')).getByText('MR #12')).toBeTruthy();
  });

  it('Push sends {action:"push"}; the returned landing replaces the strip and onLanded fires', async () => {
    api.landPhase.mockResolvedValue({
      branch: 'swarm/phase-207',
      base: 'main',
      action: 'push',
      landing: landing({ state: 'pushed' }),
    });
    const { onLanded } = renderReview();
    await screen.findByText('add line items');
    fireEvent.click(button('Push'));
    await waitFor(() => {
      expect(screen.getByText('pushed')).toBeTruthy();
    });
    expect(api.landPhase).toHaveBeenCalledWith(9, 207, { action: 'push' });
    expect(onLanded).toHaveBeenCalledTimes(1);
  });

  it('sends the Draft flag with Push + open', async () => {
    api.landPhase.mockResolvedValue({
      branch: 'swarm/phase-207',
      base: 'main',
      action: 'pr',
      landing: landing({ state: 'pr_open', prUrl: 'https://host.example/p/1', prNumber: 1 }),
    });
    renderReview();
    await screen.findByText('add line items');
    fireEvent.click(screen.getByRole('checkbox', { name: 'Draft' }));
    fireEvent.click(button('Push + open Pull Request'));
    await waitFor(() => {
      expect(api.landPhase).toHaveBeenCalledWith(9, 207, { action: 'pr', draft: true });
    });
  });

  it('marks the in-flight button aria-busy and disables every action meanwhile', async () => {
    api.landPhase.mockReturnValue(new Promise(() => {}));
    renderReview();
    await screen.findByText('add line items');
    fireEvent.click(button('Push'));
    const pushing = button('Pushing…');
    expect(pushing.getAttribute('aria-busy')).toBe('true');
    expect(pushing.disabled).toBe(true);
    expect(button('Push + open Pull Request').disabled).toBe(true);
    expect(button('Push + open Pull Request').getAttribute('aria-busy')).toBe('false');
    expect(button('Return to agent…').disabled).toBe(true);
  });

  it('renders a 422 as error + hint + detail in a <pre>', async () => {
    api.landPhase.mockRejectedValue(
      new LandError(
        422,
        {
          error: 'not authenticated',
          code: 'not-authenticated',
          hint: 'the code host rejected the credentials. Log in, then land again:\ngh auth login --hostname github.com',
          detail: 'HTTP 401: Bad credentials',
        },
        'land failed',
      ),
    );
    renderReview();
    await screen.findByText('add line items');
    fireEvent.click(button('Push'));
    const alert = await screen.findByRole('alert');
    expect(alert.tagName).toBe('PRE');
    expect(alert.textContent).toContain('not authenticated');
    expect(alert.textContent).toContain('gh auth login --hostname github.com');
    expect(alert.textContent).toContain('HTTP 401: Bad credentials');
  });

  it('maps 409 codes to inline sentences', async () => {
    api.landPhase.mockRejectedValue(
      new LandError(
        409,
        { error: 'raw server text', code: 'push-to-base-refused', branch: 'main', base: 'main' },
        'land failed',
      ),
    );
    renderReview();
    await screen.findByText('add line items');
    fireEvent.click(button('Push'));
    const alert = await screen.findByRole('alert');
    expect(alert.tagName).toBe('DIV');
    expect(alert.textContent).toContain('the run branch main is the base branch');
    expect(alert.textContent).toContain('allowPushToBase');

    api.landPhase.mockRejectedValue(
      new LandError(409, { error: 'raw', code: 'fork-workflow-unsupported' }, 'land failed'),
    );
    fireEvent.click(button('Push + open Pull Request'));
    await waitFor(() => {
      expect(screen.getByRole('alert').textContent).toContain('open the Pull Request by hand');
    });

    api.landPhase.mockRejectedValue(new LandError(409, { error: 'raw', code: 'phase-running' }, 'land failed'));
    fireEvent.click(button('Push'));
    await waitFor(() => {
      expect(screen.getByRole('alert').textContent).toContain('still running');
    });
  });

  it('a gone run branch: says why and refetches only when the phase was settled as landed', async () => {
    const { onLanded } = renderReview();
    await screen.findByText('add line items');

    api.landPhase.mockRejectedValue(
      new LandError(409, { error: 'raw', code: 'run-branch-gone', branch: 'swarm/phase-207' }, 'land failed'),
    );
    fireEvent.click(button('Push'));
    await waitFor(() => {
      expect(screen.getByRole('alert').textContent).toContain('run it again for a new branch');
    });
    expect(onLanded).not.toHaveBeenCalled();

    api.landPhase.mockRejectedValue(
      new LandError(
        409,
        { error: 'raw', code: 'run-branch-gone', branch: 'swarm/phase-207', landing: landing({ state: 'merged' }) },
        'land failed',
      ),
    );
    fireEvent.click(button('Push'));
    await waitFor(() => {
      expect(screen.getByRole('alert').textContent).toContain('the phase is counted as landed');
    });
    expect(onLanded).toHaveBeenCalledTimes(1);
  });

  it('a running phase offers no action — nothing is rendered disabled, it is just not there', async () => {
    renderReview(phase({ runState: 'running', landing: landing({ state: 'none' }) }));
    await screen.findByText('add line items');
    expect(screen.queryByRole('button', { name: 'Push' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Push + open Pull Request' })).toBeNull();
    expect(screen.queryByRole('checkbox', { name: 'Draft' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Return to agent…' })).toBeNull();
  });

  it('Return needs feedback text before it can be sent', async () => {
    api.landPhase.mockResolvedValue({
      branch: 'swarm/phase-207',
      base: 'main',
      action: 'return',
      landing: landing({ state: 'returned' }),
    });
    renderReview();
    await screen.findByText('add line items');
    fireEvent.click(button('Return to agent…'));
    const dialog = screen.getByRole('dialog', { name: 'Return to agent?' });
    const confirm = within(dialog).getByRole('button', { name: 'return' }) as HTMLButtonElement;
    expect(confirm.disabled).toBe(true);
    fireEvent.change(within(dialog).getByRole('textbox', { name: 'feedback for the agent' }), {
      target: { value: '  handle the empty order  ' },
    });
    expect(confirm.disabled).toBe(false);
    await act(async () => {
      fireEvent.click(confirm);
    });
    expect(api.landPhase).toHaveBeenCalledWith(9, 207, { action: 'return', feedback: 'handle the empty order' });
    await waitFor(() => {
      expect(screen.queryByRole('dialog')).toBeNull();
    });
    expect(screen.getByText('returned to agent')).toBeTruthy();
  });

  it('Escape closes the Return dialog without sending', async () => {
    renderReview();
    await screen.findByText('add line items');
    fireEvent.click(button('Return to agent…'));
    expect(screen.getByRole('dialog')).toBeTruthy();
    fireEvent.keyDown(window, { key: 'Escape' });
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(api.landPhase).not.toHaveBeenCalled();
  });

  it('a 409 no-run-branch review says so and offers no actions', async () => {
    api.getPhaseReview.mockRejectedValue(
      new LandError(409, { error: 'this phase has no run branch', code: 'no-run-branch' }, 'review failed'),
    );
    renderReview(phase({ landing: landing({ state: 'none' }), runState: 'failed' }));
    expect(await screen.findByText(/This phase has no run branch yet/)).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Push' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Push + open Pull Request' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Return to agent…' })).toBeNull();
    expect(screen.getByText('The run branch is gone')).toBeTruthy();
  });

  describe('header branch vs run branch', () => {
    it('warns when the plan header names a different branch than the run', async () => {
      api.getPhaseReview.mockResolvedValue(review({ headerBranch: 'feat/x' }));
      renderReview();
      const note = await screen.findByRole('note');
      expect(note.textContent).toContain('feat/x');
      expect(note.textContent).toContain('swarm/phase-207');
      // The banner only informs: Push still pushes the run branch.
      expect(button('Push').disabled).toBe(false);
    });

    it('stays silent when the header names the run branch', async () => {
      api.getPhaseReview.mockResolvedValue(review({ headerBranch: 'swarm/phase-207' }));
      renderReview();
      expect(await screen.findByText('add line items')).toBeTruthy();
      expect(screen.queryByRole('note')).toBeNull();
    });

    it('stays silent when the header names no branch', async () => {
      api.getPhaseReview.mockResolvedValue(review({ headerBranch: '' }));
      renderReview();
      expect(await screen.findByText('add line items')).toBeTruthy();
      expect(screen.queryByRole('note')).toBeNull();
    });
  });
});

// ↑/↓ step navigation keeps the tab and re-renders the SAME instance with
// another phase: whatever comes back late for the previous phase must not be
// written onto the one now shown.
describe('PhaseReview — a late response for another phase', () => {
  const phaseB = (): EpicPhase => phase({ id: 308, seq: 3, name: 'Phase 3 — Totals', runSessionUuid: 'sess-308' });
  const reviewB = (): PhaseReviewData => review({ branch: 'swarm/phase-308', commits: [{ sha: 'b0b', subject: 'add totals' }] });

  function deferred<T>(): { promise: Promise<T>; resolve: (v: T) => void; reject: (e: unknown) => void } {
    let resolve!: (v: T) => void;
    let reject!: (e: unknown) => void;
    const promise = new Promise<T>((res, rej) => {
      resolve = res;
      reject = rej;
    });
    return { promise, resolve, reject };
  }

  beforeEach(() => {
    api.getPhaseReview.mockImplementation((_task: number, id: number) =>
      Promise.resolve(id === 308 ? reviewB() : review()),
    );
  });

  it('a land on phase A that resolves after the switch to B leaves B untouched', async () => {
    const land = deferred<unknown>();
    api.landPhase.mockReturnValue(land.promise);
    const onLanded = vi.fn();
    const { rerender } = render(<PhaseReview epic={EPIC} phase={phase()} terms={PR_TERMS} onLanded={onLanded} />);
    await screen.findByText('add line items');
    fireEvent.click(button('Push + open Pull Request'));
    expect(api.landPhase).toHaveBeenCalledWith(9, 207, { action: 'pr', draft: false });

    rerender(<PhaseReview epic={EPIC} phase={phaseB()} terms={PR_TERMS} onLanded={onLanded} />);
    await screen.findByText('add totals');
    await act(async () => {
      land.resolve({
        branch: 'swarm/phase-207',
        base: 'main',
        action: 'pr',
        landing: landing({ state: 'pr_open', prUrl: 'https://host.example/acme/w/pull/77', prNumber: 77 }),
      });
      await land.promise;
    });

    expect(screen.queryByRole('link', { name: 'PR #77' })).toBeNull();
    expect(screen.queryByText('PR open')).toBeNull();
    expect(screen.getByText('ready to land')).toBeTruthy();
    expect(screen.queryByRole('alert')).toBeNull();
    expect(button('Push').disabled).toBe(false);
    expect(button('Push + open Pull Request').disabled).toBe(false);
    expect(button('Return to agent…').disabled).toBe(false);
    // The land did happen, so the page still refetches.
    expect(onLanded).toHaveBeenCalledTimes(1);
  });

  it('a land on phase A that fails after the switch to B shows B no error', async () => {
    const land = deferred<unknown>();
    api.landPhase.mockReturnValue(land.promise);
    const { rerender } = render(<PhaseReview epic={EPIC} phase={phase()} terms={PR_TERMS} />);
    await screen.findByText('add line items');
    fireEvent.click(button('Push'));

    rerender(<PhaseReview epic={EPIC} phase={phaseB()} terms={PR_TERMS} />);
    await screen.findByText('add totals');
    await act(async () => {
      land.reject(new LandError(409, { error: 'raw', code: 'phase-running' }, 'land failed'));
      await land.promise.catch(() => undefined);
    });

    expect(screen.queryByRole('alert')).toBeNull();
    expect(button('Push').disabled).toBe(false);
  });

  it("phase A's review arriving after B's is dropped", async () => {
    const slowA = deferred<PhaseReviewData>();
    api.getPhaseReview.mockImplementation((_task: number, id: number) =>
      id === 308 ? Promise.resolve(reviewB()) : slowA.promise,
    );
    const { rerender } = render(<PhaseReview epic={EPIC} phase={phase()} terms={PR_TERMS} />);
    rerender(<PhaseReview epic={EPIC} phase={phaseB()} terms={PR_TERMS} />);
    await screen.findByText('add totals');
    await act(async () => {
      slowA.resolve(review());
      await slowA.promise;
    });
    expect(screen.getByText('add totals')).toBeTruthy();
    expect(screen.queryByText('add line items')).toBeNull();
  });
});

describe('PhaseReview — Find <change> (land-criteria UX, phase 4)', () => {
  it.each(['ready', 'pushed'] as const)('%s: offers Find PR, labelled from terms', async (state) => {
    const l = landing({ state });
    api.getPhaseReview.mockResolvedValue(review({ landing: l }));
    renderReview(phase({ landing: l }));
    await screen.findByText('add line items');
    expect(button('Find PR').disabled).toBe(false);
  });

  it('labels Find from the other vocabulary', async () => {
    api.getPhaseReview.mockResolvedValue(review({ terms: MR_TERMS }));
    renderReview(phase(), MR_TERMS);
    expect(await screen.findByRole('button', { name: 'Find MR' })).toBeTruthy();
  });

  it('falls back to the neutral word when the terms carry no short name', async () => {
    const bare: ProviderTerms = { ...PR_TERMS, changeShort: '' };
    api.getPhaseReview.mockResolvedValue(review({ terms: bare }));
    renderReview(phase(), bare);
    expect(await screen.findByRole('button', { name: 'Find change request' })).toBeTruthy();
  });

  it.each([
    ['pr_open', landing({ state: 'pr_open', prUrl: 'https://host.example/acme/w/pull/77', prNumber: 77 })],
    ['merged', landing({ state: 'merged', prUrl: 'https://host.example/acme/w/pull/77', prNumber: 77 })],
    ['returned', landing({ state: 'returned' })],
  ])('%s: no Find button', async (_state, l) => {
    api.getPhaseReview.mockResolvedValue(review({ landing: l }));
    renderReview(phase({ landing: l }));
    await screen.findByText('add line items');
    expect(screen.queryByRole('button', { name: /^Find / })).toBeNull();
  });

  it('a found change request replaces the strip and fires onLanded', async () => {
    const found = landing({ state: 'pr_open', prUrl: 'https://host.example/acme/w/pull/88', prNumber: 88 });
    api.refreshPhaseLanding.mockResolvedValue(found);
    const { onLanded } = renderReview();
    await screen.findByText('add line items');
    fireEvent.click(button('Find PR'));
    await waitFor(() => expect(within(screen.getByTestId('review-ledger-pr')).getByText('PR #88')).toBeTruthy());
    expect(api.refreshPhaseLanding).toHaveBeenCalledWith(9, 207);
    expect(onLanded).toHaveBeenCalledTimes(1);
    expect(screen.getByTestId('review-ledger-branch').textContent).toContain('PR open');
    expect(screen.queryByRole('button', { name: /^Find / })).toBeNull();
  });

  it('marks Find busy and disables the other actions meanwhile', async () => {
    api.refreshPhaseLanding.mockReturnValue(new Promise(() => {}));
    renderReview();
    await screen.findByText('add line items');
    fireEvent.click(button('Find PR'));
    const looking = button('Looking…');
    expect(looking.getAttribute('aria-busy')).toBe('true');
    expect(looking.disabled).toBe(true);
    expect(button('Push').disabled).toBe(true);
    expect(button('Return to agent…').disabled).toBe(true);
  });

  it('a 409 no-change-request names the run branch', async () => {
    api.refreshPhaseLanding.mockRejectedValue(
      new LandError(409, { error: 'no open or merged change request', code: 'no-change-request' as never, branch: 'swarm/phase-207' }, 'x'),
    );
    const { onLanded } = renderReview();
    await screen.findByText('add line items');
    fireEvent.click(button('Find PR'));
    const status = await screen.findByRole('status');
    expect(status.textContent).toContain('No open or merged PR found for swarm/phase-207');
    expect(onLanded).not.toHaveBeenCalled();
    expect(button('Find PR').disabled).toBe(false);
  });

  it('any other failure renders as a land failure', async () => {
    api.refreshPhaseLanding.mockRejectedValue(
      new LandError(422, { error: 'not authenticated', code: 'not-authenticated', hint: 'log in', detail: '' }, 'x'),
    );
    renderReview();
    await screen.findByText('add line items');
    fireEvent.click(button('Find PR'));
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('not authenticated');
    expect(alert.textContent).toContain('log in');
  });
});

describe('PhaseReview — guard decision (plan D24/D25/D26)', () => {
  it('an open guard decision is the only next step, and answering it in place fires onLanded', async () => {
    guardApi.answerGuardDecision.mockResolvedValue(undefined);
    const decision = guardDecision();
    const { onLanded } = renderReview(phase({ guardDecision: decision }));
    await screen.findByText('add line items');
    expect(screen.getByText('Decision D-4 is still open')).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Push' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Return to agent…' })).toBeNull();

    fireEvent.click(button('Answer the decision'));
    const dialog = screen.getByRole('dialog');
    // Exactly allow-add-to-scope + reject: the two-way shape, answered by one
    // button each — no radio group to pick from (guardDecisionView.ts).
    fireEvent.click(within(dialog).getByRole('button', { name: /^Allow · add to scope/ }));
    await waitFor(() => expect(onLanded).toHaveBeenCalledTimes(1));
    expect(guardApi.answerGuardDecision).toHaveBeenCalledWith(9, 207, 'D-4', 'allow-add-to-scope', '');
  });

  it('D26: an empty guard decision closes with one button in the Guard row, not a choice', async () => {
    guardApi.answerGuardDecision.mockResolvedValue(undefined);
    const decision = guardDecision({ paths: [] });
    const { onLanded } = renderReview(phase({ guardDecision: decision }));
    await screen.findByText('add line items');
    expect(button('Close · allow nothing')).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Answer the decision' })).toBeNull();
    // D26: an empty decision is never named as the reason anything stopped —
    // the verdict's own lead clause never says "Decision D-n is still open".
    expect(screen.queryByText('Decision D-4 is still open')).toBeNull();
    expect(screen.getByText('Nothing to land yet')).toBeTruthy();

    fireEvent.click(button('Close · allow nothing'));
    await waitFor(() => expect(onLanded).toHaveBeenCalledTimes(1));
    expect(guardApi.answerGuardDecision).toHaveBeenCalledWith(9, 207, 'D-4', 'reject', '');
  });

  it('a stop decision with no paths is never treated as empty', async () => {
    const decision = guardDecision({ paths: [], trigger: 'protected-path', reason: 'touched a hook' });
    renderReview(phase({ guardDecision: decision }));
    await screen.findByText('add line items');
    expect(button('Answer the decision')).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Close · allow nothing' })).toBeNull();
  });
});

describe('PhaseReview — "what git said" disclosure (plan D24/D25)', () => {
  it('keeps the stored landing failure out of the main flow, visible only once opened', async () => {
    const failed = landing({ state: 'pushed', error: 'push rejected: non-fast-forward (raw git stderr)' });
    api.getPhaseReview.mockResolvedValue(review({ landing: failed }));
    renderReview(phase({ landing: failed }));
    await screen.findByText('add line items');
    const details = screen.getByText('what git said').closest('details');
    expect(details).not.toBeNull();
    expect(details?.textContent).toContain('push rejected: non-fast-forward');
    // Not sitting on the tab as a loose line outside the disclosure.
    expect(screen.queryByText(/^last landing failure:/)).toBeNull();
  });

  it('renders nothing when there is no stored failure', async () => {
    renderReview();
    await screen.findByText('add line items');
    expect(screen.queryByText('what git said')).toBeNull();
  });
});
