// @vitest-environment jsdom
//
// FrictionTriageStrip: the idle, running, ended and error states of Health →
// Friction's trigger, the controls each one offers, and that nothing but the
// progress shows while a run is active.

import { cleanup, fireEvent, render, screen } from '../../test/render';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { FrictionTriageStrip, type FrictionTriageStripProps } from './FrictionTriageStrip';

afterEach(cleanup);

function props(over: Partial<FrictionTriageStripProps> = {}): FrictionTriageStripProps {
  return {
    untriaged: 0,
    running: null,
    summary: null,
    dismissed: false,
    error: null,
    busy: false,
    onStart: vi.fn(),
    onDismiss: vi.fn(),
    onDismissError: vi.fn(),
    ...over,
  };
}

describe('FrictionTriageStrip', () => {
  it('idle: counts the untriaged groups and offers a run', () => {
    const p = props({ untriaged: 3 });
    render(<FrictionTriageStrip {...p} />);
    expect(screen.getByText('3 untriaged groups · agents are checked by rule')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'run triage' }));
    expect(p.onStart).toHaveBeenCalledTimes(1);
  });

  it('idle with one group reads singular', () => {
    render(<FrictionTriageStrip {...props({ untriaged: 1 })} />);
    expect(screen.getByText('1 untriaged group · agents are checked by rule')).toBeTruthy();
  });

  it('idle with no untriaged group still offers a run: a failing agent needs no friction', () => {
    render(<FrictionTriageStrip {...props()} />);
    expect(screen.getByText('no untriaged groups · agents are checked by rule')).toBeTruthy();
    expect(screen.getByRole('button', { name: 'run triage' })).toBeTruthy();
  });

  it('disables the run while a start is in flight', () => {
    render(<FrictionTriageStrip {...props({ busy: true })} />);
    expect((screen.getByRole('button', { name: 'run triage' }) as HTMLButtonElement).disabled).toBe(true);
  });

  it('running: shows progress as a status and offers no run', () => {
    render(<FrictionTriageStrip {...props({ untriaged: 3, running: { done: 2, total: 9 } })} />);
    expect(screen.getByRole('status').textContent).toBe('triage running · 2 of 9');
    expect(screen.queryByRole('button', { name: 'run triage' })).toBeNull();
    expect(screen.queryByText(/agents are checked by rule/)).toBeNull();
  });

  it('ended: shows the run result in the Inbox wording, dismissable', () => {
    const p = props({ summary: { applied: 2, suggested: 1, failed: 1 } });
    render(<FrictionTriageStrip {...p} />);
    expect(screen.getByRole('status').textContent).toBe('agent closed 2 · left 1 suggestion · 1 failed');
    fireEvent.click(screen.getByRole('button', { name: 'dismiss' }));
    expect(p.onDismiss).toHaveBeenCalledTimes(1);
    // The offer stays: another run can start after this one.
    expect(screen.getByRole('button', { name: 'run triage' })).toBeTruthy();
  });

  it('ended without failures leaves the failed count out', () => {
    render(<FrictionTriageStrip {...props({ summary: { applied: 0, suggested: 3, failed: 0 } })} />);
    expect(screen.getByRole('status').textContent).toBe('agent closed 0 · left 3 suggestions');
  });

  it('a dismissed result is hidden', () => {
    render(<FrictionTriageStrip {...props({ summary: { applied: 2, suggested: 1, failed: 0 }, dismissed: true })} />);
    expect(screen.queryByText(/agent closed/)).toBeNull();
  });

  it('error: an alert with retry and dismiss', () => {
    const p = props({ error: 'POST /api/triage/runs: 503' });
    render(<FrictionTriageStrip {...p} />);
    expect(screen.getByRole('alert').textContent).toBe('POST /api/triage/runs: 503');
    fireEvent.click(screen.getByRole('button', { name: 'retry' }));
    expect(p.onStart).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole('button', { name: 'dismiss error' }));
    expect(p.onDismissError).toHaveBeenCalledTimes(1);
  });

  it('hides the error while a run is active', () => {
    render(<FrictionTriageStrip {...props({ error: 'budget exhausted', running: { done: 0, total: 4 } })} />);
    expect(screen.queryByRole('alert')).toBeNull();
    expect(screen.queryByRole('button', { name: 'retry' })).toBeNull();
  });
});
