// @vitest-environment jsdom
//
// Discard-guard coverage for the create-account modal — mirrors
// workspace/NewTaskModal.test.tsx's "discard guard" block. Only the form stage
// (an account key that has not been submitted yet) is exercised: the post-
// success "done"/connect stage has nothing left to lose, so it is untouched by
// the guard and out of scope here.
//
// Runs with the rest of the web suite: `npm test` (vitest, also a swarmery-ci
// step). On its own: `npx vitest run src/components/CreateAccountModal.test.tsx`.

import { cleanup, fireEvent, render, screen, waitFor } from '../test/render';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { CreateAccountModal } from './CreateAccountModal';

vi.mock('../api', () => ({
  createAccount: vi.fn(async () => {
    throw new Error('discard-guard coverage never submits');
  }),
}));

afterEach(() => {
  cleanup();
});

describe('CreateAccountModal — discard guard', () => {
  it('confirms on Escape once dirty, a second Escape dismisses the confirm, and confirming discard closes', async () => {
    const onClose = vi.fn();
    render(
      <MemoryRouter>
        <CreateAccountModal open onClose={onClose} onCreated={() => {}} />
      </MemoryRouter>,
    );

    // The outer dialog stays mounted for the whole test — capture it once, before
    // the ConfirmDialog (also role="dialog") appears and makes the query ambiguous.
    const dialog = screen.getByRole('dialog');

    fireEvent.change(screen.getByLabelText('account key'), { target: { value: 'my-account' } });

    fireEvent.keyDown(dialog, { key: 'Escape' });
    expect(await screen.findByText('Discard account key?')).toBeDefined();
    expect(onClose).not.toHaveBeenCalled();

    // A second Escape dismisses the confirm, not the modal — the key survives.
    fireEvent.keyDown(dialog, { key: 'Escape' });
    await waitFor(() => {
      expect(screen.queryByText('Discard account key?')).toBeNull();
    });
    expect(onClose).not.toHaveBeenCalled();
    expect((screen.getByLabelText('account key') as HTMLInputElement).value).toBe('my-account');

    fireEvent.keyDown(dialog, { key: 'Escape' });
    fireEvent.click(await screen.findByRole('button', { name: 'discard' }));
    expect(onClose).toHaveBeenCalled();
  });

  it('closes immediately on Escape when the key field is empty', async () => {
    const onClose = vi.fn();
    render(
      <MemoryRouter>
        <CreateAccountModal open onClose={onClose} onCreated={() => {}} />
      </MemoryRouter>,
    );

    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' });
    expect(onClose).toHaveBeenCalled();
  });
});
