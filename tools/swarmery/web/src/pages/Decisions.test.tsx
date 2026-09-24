// @vitest-environment jsdom
//
// The Decisions page's setup card: with no local model configured the page must
// say the feature is optional and link the setup guide, instead of a bare
// env-var name; once configured, the card goes away and the backend line shows.
//
// Dev-only suite (the web app ships no committed test runner). Run it with
//   npx vitest run src/pages/Decisions.test.tsx
// after fetching the runner on demand:
//   npm i --no-save vitest jsdom @testing-library/react @testing-library/dom

import { cleanup, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { DecisionsResponse } from '../api/decisions';
import { Decisions } from './Decisions';

let response: DecisionsResponse;

vi.mock('../api/decisions', () => ({
  fetchDecisions: () => Promise.resolve(response),
  putDecisionMode: () => Promise.resolve(response),
}));

function renderPage(): void {
  render(
    <MemoryRouter>
      <Decisions />
    </MemoryRouter>,
  );
}

afterEach(cleanup);

describe('Decisions setup card', () => {
  it('explains the optional setup and links the guide when no model is configured', async () => {
    response = { configured: false, local: false, claude: false, questions: [] };
    renderPage();
    expect(await screen.findByText(/No local model configured — this is optional/)).toBeTruthy();
    const link = screen.getByRole('link', { name: /Setup guide/ });
    expect(link.getAttribute('href')).toBe('/docs/guide-decisions');
    expect(screen.queryByText(/backend:/)).toBeNull();
  });

  it('shows the backend line and no card once a local model is configured', async () => {
    response = { configured: true, local: true, claude: false, questions: [] };
    renderPage();
    expect(await screen.findByText(/backend: local/)).toBeTruthy();
    expect(screen.queryByText(/No local model configured/)).toBeNull();
  });
});
