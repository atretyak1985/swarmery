// @vitest-environment jsdom
//
// The first-request card (verify-not-trust phase 14, SC-15): the measured
// number is the headline, the stacked bar is labelled as an ESTIMATE
// («estimate (~bytes/4)») with one segment per non-zero part, and the card
// fetches its own payload by project id.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { FirstRequest } from '../../api/types';
import { cleanup, render, screen, waitFor } from '../../test/render';
import { FirstRequestCard, FirstRequestView } from './FirstRequestCard';

const api = vi.hoisted(() => ({ fetchProjectFirstRequest: vi.fn() }));

vi.mock('../../api', async (importOriginal) => {
  const real = await importOriginal<typeof import('../../api')>();
  return { ...real, fetchProjectFirstRequest: api.fetchProjectFirstRequest };
});

afterEach(cleanup);
beforeEach(() => {
  api.fetchProjectFirstRequest.mockReset();
});

const payload = (over: Partial<FirstRequest> = {}): FirstRequest => ({
  windowDays: 30,
  measured: { medianTokens: 41_000, medianPromptTokens: 58_000, sessions: 216 },
  estimate: {
    claudeMd: 6_000,
    mcp: 200,
    skills: 0,
    other: 51_800,
    note: 'estimate (~bytes/4): …',
    claudeMdFiles: ['/h/.claude/CLAUDE.md'],
    mcpTools: 4,
    mcpSchemas: 0,
    skillCount: 0,
  },
  mcpServers: [],
  ...over,
});

describe('FirstRequestView', () => {
  it('headlines the measured size and labels the bar as an estimate', () => {
    render(<FirstRequestView data={payload()} />);
    expect(screen.getByTestId('first-request-measured').textContent).toBe('58K');
    expect(screen.getByText(/median of 216 sessions over 30 days · cache write 41K/)).toBeTruthy();
    expect(screen.getByText('estimate (~bytes/4)')).toBeTruthy();
    expect(screen.getByRole('img', { name: /estimated split: CLAUDE\.md ~6K, MCP tools ~200, skills ~0, other parts ~52K/ }))
      .toBeTruthy();
  });

  it('draws one segment per non-zero part, sized by its share', () => {
    render(<FirstRequestView data={payload()} />);
    expect(screen.queryByTestId('first-request-seg-skills')).toBeNull(); // 0 → no segment
    const md = screen.getByTestId('first-request-seg-claudeMd');
    expect(md.style.width).toBe(`${String((6_000 / 58_000) * 100)}%`);
    expect(screen.getByTestId('first-request-part-skills').textContent).toBe('~0'); // still in the legend
  });

  it('says how many MCP tools were counted by name only', () => {
    render(<FirstRequestView data={payload()} />);
    expect(screen.getByText(/MCP: 4 of 4 tools counted by name only/)).toBeTruthy();
  });

  it('shows an empty measurement without inventing a number', () => {
    render(
      <FirstRequestView
        data={payload({ measured: { medianTokens: 0, medianPromptTokens: 0, sessions: 0 } })}
      />,
    );
    expect(screen.getByTestId('first-request-measured').textContent).toBe(
      'no session in the last 30 days to measure',
    );
  });
});

describe('FirstRequestCard', () => {
  it('fetches the project payload and renders it', async () => {
    api.fetchProjectFirstRequest.mockResolvedValue(payload());
    render(<FirstRequestCard projectId={11} />);
    await waitFor(() => expect(screen.getByTestId('first-request-card')).toBeTruthy());
    expect(api.fetchProjectFirstRequest).toHaveBeenCalledWith(11);
  });

  it('shows the error with a retry', async () => {
    api.fetchProjectFirstRequest.mockRejectedValue(new Error('boom'));
    render(<FirstRequestCard projectId={11} />);
    await waitFor(() => expect(screen.getByText(/boom/)).toBeTruthy());
  });
});
