// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen } from '../../test/render';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { TriageBanner, type TriageBannerProps } from './TriageBanner';

afterEach(cleanup);

function props(over: Partial<TriageBannerProps> = {}): TriageBannerProps {
  return {
    offer: { agent: 0, you: 0, show: false },
    running: null,
    summary: null,
    error: null,
    suggestions: { total: 0, acceptable: 0, fixTasks: 0, parts: [] },
    acceptResult: null,
    dismissed: false,
    busy: false,
    onStart: vi.fn(),
    onAcceptAll: vi.fn(),
    onOpenHandled: vi.fn(),
    onDismiss: vi.fn(),
    onDismissError: vi.fn(),
    ...over,
  };
}

const THREE = { total: 3, acceptable: 3, fixTasks: 0, parts: ['2 accept', '1 dismiss'] };

describe('TriageBanner', () => {
  it('renders nothing when nothing applies', () => {
    const { container } = render(<TriageBanner {...props()} />);
    expect(container.firstChild).toBeNull();
  });

  it('offers a run and calls onStart', () => {
    const p = props({ offer: { agent: 12, you: 3, show: true } });
    render(<TriageBanner {...p} />);
    expect(screen.getByText('12 can be handled by an agent · 3 need you')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'run triage' }));
    expect(p.onStart).toHaveBeenCalledTimes(1);
  });

  it('shows progress as a polite status and no run-triage offer', () => {
    render(<TriageBanner {...props({ running: { done: 12, total: 40 }, offer: { agent: 1, you: 0, show: true } })} />);
    const status = screen.getByRole('status');
    expect(status.textContent).toBe('triage running · 12 of 40');
    expect(status.getAttribute('aria-live')).toBe('polite');
    expect(screen.queryByRole('button', { name: 'run triage' })).toBeNull();
  });

  it('shows the suggestions line with its breakdown whenever suggestions are open, without any finished run', () => {
    const p = props({ suggestions: { ...THREE, fixTasks: 1, total: 4 } });
    render(<TriageBanner {...p} />);
    expect(screen.getByText(/4 suggestions from the agent/)).toBeTruthy();
    expect(screen.getByText(/2 accept · 1 dismiss/)).toBeTruthy();
    expect(screen.getByText(/1 fix task to read first/)).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'accept 3 suggestions' }));
    expect(p.onAcceptAll).toHaveBeenCalledTimes(1);
  });

  it('with only fix tasks there is no accept control, just the read-first line', () => {
    render(<TriageBanner {...props({ suggestions: { total: 1, acceptable: 0, fixTasks: 1, parts: [] } })} />);
    expect(screen.getByText(/1 fix task to read first/)).toBeTruthy();
    expect(screen.queryByRole('button', { name: /^accept/ })).toBeNull();
  });

  it('summarises a finished run; dismissing it keeps the suggestions line', () => {
    const base = props({ summary: { applied: 7, suggested: 3, failed: 0 }, suggestions: THREE });
    const { rerender } = render(<TriageBanner {...base} />);
    expect(screen.getByText('agent closed 7 · left 3 suggestions')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'see what it closed' }));
    expect(base.onOpenHandled).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole('button', { name: 'dismiss' }));
    expect(base.onDismiss).toHaveBeenCalledTimes(1);
    rerender(<TriageBanner {...base} dismissed />);
    expect(screen.queryByText(/agent closed/)).toBeNull();
    expect(screen.getByText(/3 suggestions from the agent/)).toBeTruthy();
    expect(screen.getByRole('button', { name: 'accept 3 suggestions' })).toBeTruthy();
  });

  it('uses the singular and adds failures to the run result; the accept result is its own status line', () => {
    render(<TriageBanner {...props({ summary: { applied: 1, suggested: 1, failed: 2 }, acceptResult: 'accepted 3' })} />);
    expect(screen.getByText('agent closed 1 · left 1 suggestion · 2 failed')).toBeTruthy();
    expect(screen.getByText('accepted 3')).toBeTruthy();
    expect(screen.queryByRole('button', { name: /^accept/ })).toBeNull();
  });

  it('shows a start error as an alert with retry', () => {
    const p = props({ error: 'POST /api/triage/runs: 500' });
    render(<TriageBanner {...p} />);
    expect(screen.getByRole('alert').textContent).toBe('POST /api/triage/runs: 500');
    fireEvent.click(screen.getByRole('button', { name: 'retry' }));
    expect(p.onStart).toHaveBeenCalledTimes(1);
  });

  it('lets the error be dismissed and does not hide the offer or the suggestions', () => {
    const p = props({
      error: 'budget exhausted',
      offer: { agent: 2, you: 1, show: true },
      suggestions: THREE,
    });
    const { rerender } = render(<TriageBanner {...p} />);
    expect(screen.getByRole('alert').textContent).toBe('budget exhausted');
    expect(screen.getByRole('button', { name: 'run triage' })).toBeTruthy();
    expect(screen.getByText(/3 suggestions from the agent/)).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'dismiss error' }));
    expect(p.onDismissError).toHaveBeenCalledTimes(1);
    // The page answers a dismissal with `error: null`: the whole line goes, retry included.
    rerender(<TriageBanner {...p} error={null} />);
    expect(screen.queryByRole('alert')).toBeNull();
    expect(screen.queryByRole('button', { name: 'retry' })).toBeNull();
    expect(screen.getByRole('button', { name: 'run triage' })).toBeTruthy();
  });

  it('shows no error line and no retry while a run is active', () => {
    render(<TriageBanner {...props({ error: 'budget exhausted', running: { done: 0, total: 4 } })} />);
    expect(screen.getByRole('status').textContent).toBe('triage running · 0 of 4');
    expect(screen.queryByRole('alert')).toBeNull();
    expect(screen.queryByRole('button', { name: 'retry' })).toBeNull();
  });

  it('names the accept control "accepting…" while the batch runs', () => {
    render(<TriageBanner {...props({ suggestions: THREE, busy: true })} />);
    const btn = screen.getByRole('button', { name: 'accepting…' });
    expect((btn as HTMLButtonElement).disabled).toBe(true);
  });
});
