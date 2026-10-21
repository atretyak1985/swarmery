// @vitest-environment jsdom
//
// The Criteria tab's [LAND] action (land-criteria-ux phase 1, SC-1…SC-4): the
// Review link, Push + open <change> through the Review tab's own land call, the
// open change request's link, and the no-run-branch explanation — with the
// row's manual toggle untouched in every state.

import { cleanup, fireEvent, render, screen, waitFor } from '../../test/render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { EpicPhase, PhaseLanding, ProviderTerms } from '../../api/types';
import { LandCriterionAction, type LandCriterionActionProps } from './LandCriterionAction';

const api = vi.hoisted(() => ({ landPhase: vi.fn() }));

vi.mock('../../api', async (importOriginal) => {
  const real = await importOriginal<typeof import('../../api')>();
  return { ...real, landPhase: api.landPhase };
});

const { LandError } = await import('../../api');

afterEach(cleanup);
// A block body: a function returned from beforeEach is run as its teardown.
beforeEach(() => {
  api.landPhase.mockReset();
});

const PR_TERMS: ProviderTerms = { provider: 'Host A', change: 'Pull Request', changeShort: 'PR' };
const LAND_TEXT = '[LAND] PR фази злито в main';

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
    runState: 'done',
    runSessionUuid: 'sess-207',
    runBranch: 'swarm/phase-207',
    landing: landing(),
    ...over,
  }) as unknown as EpicPhase;

function renderAction(over: Partial<LandCriterionActionProps> = {}) {
  const props: LandCriterionActionProps = {
    epic: { taskId: 9 },
    phase: phase(),
    text: LAND_TEXT,
    done: false,
    terms: PR_TERMS,
    onOpenReview: vi.fn(),
    onDocChanged: vi.fn(),
    ...over,
  };
  render(<LandCriterionAction {...props} />);
  return props;
}

/** The row as ChecksList renders it: the toggle button, then the action. */
function renderRow(p: EpicPhase, onToggle: () => void) {
  render(
    <ul>
      <li>
        <button type="button" aria-pressed={false} onClick={onToggle}>
          {LAND_TEXT}
        </button>
        <LandCriterionAction
          epic={{ taskId: 9 }}
          phase={p}
          text={LAND_TEXT}
          done={false}
          terms={PR_TERMS}
          onOpenReview={vi.fn()}
          onDocChanged={vi.fn()}
        />
      </li>
    </ul>,
  );
}

describe('LandCriterionAction', () => {
  it('renders nothing for a ticked or a non-[LAND] criterion', () => {
    renderAction({ done: true });
    renderAction({ text: '[MANUAL] checked on prod' });
    renderAction({ text: 'POST creates a line item' });
    expect(screen.queryByTestId('land-criterion-action')).toBeNull();
  });

  it('Review: the button switches the panel to the Review tab (SC-1)', () => {
    const props = renderAction();
    fireEvent.click(screen.getByRole('button', { name: 'Open in Review →' }));
    expect(props.onOpenReview).toHaveBeenCalledTimes(1);
  });

  it('Push + open <change>: calls landPhase with action "pr", then refetches (SC-2)', async () => {
    api.landPhase.mockResolvedValue({ branch: 'swarm/phase-207', base: 'main', action: 'pr', landing: landing() });
    const props = renderAction();
    fireEvent.click(screen.getByRole('button', { name: 'Push + open PR' }));
    expect(api.landPhase).toHaveBeenCalledWith(9, 207, { action: 'pr', draft: false });
    await waitFor(() => expect(props.onDocChanged).toHaveBeenCalledTimes(1));
  });

  it('Push + open <change>: shows a refusal inline and does not refetch', async () => {
    api.landPhase.mockRejectedValue(new LandError(409, { error: 'raw', code: 'phase-running' }, 'land failed'));
    const props = renderAction();
    fireEvent.click(screen.getByRole('button', { name: 'Push + open PR' }));
    expect((await screen.findByRole('alert')).textContent).toMatch(/still running/);
    expect(props.onDocChanged).not.toHaveBeenCalled();
  });

  it('is busy while the land is in flight', () => {
    api.landPhase.mockReturnValue(new Promise(() => {}));
    renderAction();
    fireEvent.click(screen.getByRole('button', { name: 'Push + open PR' }));
    const btn = screen.getByRole('button', { name: 'Opening…' }) as HTMLButtonElement;
    expect(btn.getAttribute('aria-busy')).toBe('true');
    expect(btn.disabled).toBe(true);
  });

  it('PR link: an open change request links to it, labelled "PR #N", with no Push (SC-3)', () => {
    const prUrl = 'https://host.example/acme/widgets/pull/77';
    renderAction({ phase: phase({ landing: landing({ state: 'pr_open', prUrl, prNumber: 77 }) }) });
    const link = screen.getByRole('link', { name: 'PR #77' });
    expect(link.getAttribute('href')).toBe(prUrl);
    expect(link.getAttribute('target')).toBe('_blank');
    expect(screen.queryByRole('button', { name: 'Push + open PR' })).toBeNull();
    expect(screen.getByRole('button', { name: 'Open in Review →' })).toBeTruthy();
  });

  it('review-only: a merged phase keeps the Review link and offers no Push', () => {
    renderAction({ phase: phase({ landing: landing({ state: 'merged' }) }) });
    expect(screen.getByRole('button', { name: 'Open in Review →' })).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Push + open PR' })).toBeNull();
  });

  it('no-run-branch: explains there is nothing to land, and the row toggle still works (SC-4)', () => {
    const onToggle = vi.fn();
    renderRow(phase({ runBranch: '', runState: 'idle', runSessionUuid: null, landing: landing({ state: 'none' }) }), onToggle);
    expect(screen.getByText(/not run by the daemon/)).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Push + open PR' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Open in Review →' })).toBeNull();
    const toggle = screen.getByRole('button', { name: LAND_TEXT }) as HTMLButtonElement;
    expect(toggle.disabled).toBe(false);
    fireEvent.click(toggle);
    expect(onToggle).toHaveBeenCalledTimes(1);
  });
});
