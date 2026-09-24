// @vitest-environment jsdom
//
// Discard-guard coverage for the shared DecisionDialog's reject-note path —
// mirrors workspace/NewTaskModal.test.tsx's "discard guard" block. The Apply
// dialog never renders a note field (withNote defaults to false), so `dirty`
// is always false there and its Escape/backdrop/Cancel behavior is unchanged
// — this suite only exercises Reject, the one path with something to lose.
//
// The web app ships no committed test runner (CI is `npm run build` only, and
// the Go coverage gate excludes web/), so this suite is dev-only. Run it with
//   npx vitest run --environment jsdom src/pages/planning/RevisionReview.test.tsx
// after fetching the runner on demand:
//   npm i --no-save vitest jsdom @testing-library/react @testing-library/dom

import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { PlanRevision } from '../../api/types';
import { RevisionReview } from './RevisionReview';

const REV: PlanRevision = {
  id: 1,
  status: 'staged',
  origin: 'operator_revise',
  reason: 'tighten the acceptance criteria',
  createdAt: '2026-01-01T00:00:00Z',
  files: [{ docPath: 'plan/phase-1-foo.md', action: 'update', diff: '@@ -1 +1 @@\n-a\n+b\n' }],
};

vi.mock('../../api', () => ({
  fetchRevision: vi.fn(async () => REV),
  applyRevision: vi.fn(async () => {
    throw new Error('discard-guard coverage never applies');
  }),
  rejectRevision: vi.fn(async () => {
    throw new Error('discard-guard coverage never rejects');
  }),
}));

afterEach(() => {
  cleanup();
});

async function openRejectDialog(): Promise<void> {
  render(
    <MemoryRouter>
      <RevisionReview revisionId={1} onDecided={() => {}} />
    </MemoryRouter>,
  );
  fireEvent.click(await screen.findByRole('button', { name: 'Reject' }));
  await waitFor(() => {
    expect(screen.getByRole('dialog', { name: 'Reject this revision?' })).toBeDefined();
  });
}

describe('RevisionReview reject dialog — discard guard', () => {
  it('confirms on Escape once the note is dirty, a second Escape dismisses the confirm, and confirming discard closes the reject dialog', async () => {
    await openRejectDialog();

    const note = screen.getByLabelText('note (optional)');
    fireEvent.change(note, { target: { value: 'does not fit the epic' } });

    fireEvent.keyDown(window, { key: 'Escape' });
    expect(await screen.findByText('Discard note?')).toBeDefined();
    // The reject dialog itself is still open underneath the confirm.
    expect(screen.getByRole('dialog', { name: 'Reject this revision?' })).toBeDefined();

    // A second Escape dismisses the confirm, not the reject dialog — the note survives.
    fireEvent.keyDown(window, { key: 'Escape' });
    await waitFor(() => {
      expect(screen.queryByText('Discard note?')).toBeNull();
    });
    expect((screen.getByLabelText('note (optional)') as HTMLTextAreaElement).value).toBe(
      'does not fit the epic',
    );

    fireEvent.keyDown(window, { key: 'Escape' });
    fireEvent.click(await screen.findByRole('button', { name: 'discard' }));
    await waitFor(() => {
      expect(screen.queryByRole('dialog', { name: 'Reject this revision?' })).toBeNull();
    });
  });

  it('closes the reject dialog immediately on Escape when the note is empty', async () => {
    await openRejectDialog();

    fireEvent.keyDown(window, { key: 'Escape' });
    await waitFor(() => {
      expect(screen.queryByRole('dialog', { name: 'Reject this revision?' })).toBeNull();
    });
  });
});
