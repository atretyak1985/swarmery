// @vitest-environment jsdom
//
// Discard-guard coverage for the "new project" onboarding modal — mirrors
// workspace/NewTaskModal.test.tsx's "discard guard" block: type into a dirty
// field, Escape asks first, a second Escape dismisses the confirm (typed
// value survives), and confirming discard actually closes.
//
// The web app ships no committed test runner (CI is `npm run build` only, and
// the Go coverage gate excludes web/), so this suite is dev-only. Run it with
//   npx vitest run --environment jsdom src/components/NewProjectButton.test.tsx
// after fetching the runner on demand:
//   npm i --no-save vitest jsdom @testing-library/react @testing-library/dom

import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { NewProjectButton } from './NewProjectButton';

vi.mock('../api', () => ({
  fetchOnboardConfig: vi.fn(async () => ({
    enabled: true,
    workspaceRoot: '/default/workspace',
    roots: ['/absolute'],
  })),
  onboardProject: vi.fn(async () => {
    throw new Error('discard-guard coverage never submits');
  }),
}));

afterEach(() => {
  cleanup();
});

async function openModal(): Promise<void> {
  render(<NewProjectButton />);
  fireEvent.click(screen.getByRole('button', { name: '+ new project' }));
  await waitFor(() => {
    expect(screen.getByRole('dialog', { name: 'New project' })).toBeDefined();
  });
}

describe('NewProjectButton — discard guard', () => {
  it('confirms on Escape once dirty, a second Escape dismisses the confirm, and confirming discard closes', async () => {
    await openModal();

    const slugInput = screen.getByPlaceholderText('my-project');
    fireEvent.change(slugInput, { target: { value: 'a-project' } });

    fireEvent.keyDown(window, { key: 'Escape' });
    expect(await screen.findByText('Discard new project?')).toBeDefined();
    // The modal is still mounted — no onClose has fired.
    expect(screen.getByRole('dialog', { name: 'New project' })).toBeDefined();

    // A second Escape dismisses the confirm, not the modal — the slug survives.
    fireEvent.keyDown(window, { key: 'Escape' });
    await waitFor(() => {
      expect(screen.queryByText('Discard new project?')).toBeNull();
    });
    expect((screen.getByPlaceholderText('my-project') as HTMLInputElement).value).toBe(
      'a-project',
    );

    fireEvent.keyDown(window, { key: 'Escape' });
    fireEvent.click(await screen.findByRole('button', { name: 'discard' }));
    await waitFor(() => {
      expect(screen.queryByRole('dialog', { name: 'New project' })).toBeNull();
    });
  });

  it('closes immediately on Escape when nothing has been typed', async () => {
    await openModal();

    fireEvent.keyDown(window, { key: 'Escape' });
    await waitFor(() => {
      expect(screen.queryByRole('dialog', { name: 'New project' })).toBeNull();
    });
  });
});
