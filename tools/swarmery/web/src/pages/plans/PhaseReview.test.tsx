// @vitest-environment jsdom
//
// The phase Review tab (Phase 5, SC-6 / SC-11): what it offers in each landing
// state, what it sends, and how it renders a refusal. The API module is mocked
// except LandError, which is the real class (the component tells a 409 from a
// 422 by `instanceof`).

import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Epic, EpicPhase, PhaseLanding, PhaseReview as PhaseReviewData, ProviderTerms } from '../../api/types';
import { PhaseReview } from './PhaseReview';

const api = vi.hoisted(() => ({
  getPhaseReview: vi.fn(),
  landPhase: vi.fn(),
}));

vi.mock('../../api', async (importOriginal) => {
  const real = await importOriginal<typeof import('../../api')>();
  return { ...real, getPhaseReview: api.getPhaseReview, landPhase: api.landPhase };
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
  api.getPhaseReview.mockResolvedValue(review());
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
    const link = (await screen.findByRole('link', { name: 'PR #77' })) as HTMLAnchorElement;
    expect(link.href).toBe('https://host.example/acme/w/pull/77');
    expect(screen.getByText('PR open')).toBeTruthy();
    expect(button('Push').disabled).toBe(true);
  });

  it('pr_open under the other vocabulary reads MR #n', async () => {
    const open = landing({ state: 'pr_open', prUrl: 'https://host.example/acme/w/-/merge_requests/12', prNumber: 12 });
    api.getPhaseReview.mockResolvedValue(review({ landing: open, terms: MR_TERMS }));
    renderReview(phase({ landing: open }), MR_TERMS);
    expect(await screen.findByRole('link', { name: 'MR #12' })).toBeTruthy();
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

  it('a running phase disables every action', async () => {
    renderReview(phase({ runState: 'running', landing: landing({ state: 'none' }) }));
    await screen.findByText('add line items');
    expect(button('Push').disabled).toBe(true);
    expect(button('Push + open Pull Request').disabled).toBe(true);
    expect((screen.getByRole('checkbox', { name: 'Draft' }) as HTMLInputElement).disabled).toBe(true);
    expect(button('Return to agent…').disabled).toBe(true);
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
    expect(await screen.findByText(/no run branch yet/)).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Push' })).toBeNull();
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
