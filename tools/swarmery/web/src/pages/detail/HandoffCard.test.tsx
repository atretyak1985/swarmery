// @vitest-environment jsdom
//
// The detail rail's handoff section renders the brief through the generic
// markdown renderer — so the `## Rejected` section (verify-not-trust phase 19)
// shows up like every other section, with no component of its own.
// Runs with the rest of the web suite: `npm test`. On its own:
// `npx vitest run src/pages/detail/HandoffCard.test.tsx`.

import { cleanup, fireEvent, render, screen, waitFor } from '../../test/render';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { HandoffCard } from './HandoffCard';

const brief = `# Handoff: ship the cache layer
## Non-goals — what this session deliberately did not do or ruled out
- no cache metrics
## Rejected
- Redis — chose SQLite table instead [decision D-1]
- LRU eviction — Evicts the least recently used entry when full; chose TTL only instead [interview D-2]
- … and 2 more

## Open risks & failed checks
- unknown
`;

vi.mock('../../api', () => ({
  fetchSessionHandoff: vi.fn(async () => ({
    markdown: brief,
    path: '/home/u/.swarmery/handoffs/abc.md',
    createdAt: '2026-10-22T20:00:00Z',
  })),
}));

afterEach(cleanup);

describe('HandoffCard', () => {
  it('renders the Rejected section between Non-goals and Open risks', async () => {
    // HandoffCard reads the URL hash (useLocation), so it needs a router.
    render(
      <MemoryRouter>
        <HandoffCard sessionId={7} handoffPath="/home/u/.swarmery/handoffs/abc.md" />
      </MemoryRouter>,
    );
    fireEvent.click(screen.getByRole('button', { name: /handoff/i }));

    const rejected = await waitFor(() => screen.getByRole('heading', { name: 'Rejected' }));
    const headings = screen.getAllByRole('heading').map((h) => h.textContent ?? '');
    const at = headings.indexOf('Rejected');
    expect(headings[at - 1]).toMatch(/^Non-goals/);
    expect(headings[at + 1]).toMatch(/^Open risks/);

    // The section's lines render as list items under its heading, verbatim.
    const list = rejected.nextElementSibling;
    expect(list?.tagName).toBe('UL');
    const items = Array.from(list?.querySelectorAll('li') ?? []).map((li) => li.textContent);
    expect(items).toEqual([
      'Redis — chose SQLite table instead [decision D-1]',
      'LRU eviction — Evicts the least recently used entry when full; chose TTL only instead [interview D-2]',
      '… and 2 more',
    ]);
  });
});
