// @vitest-environment jsdom
//
// "Which service hosts <host>?" (Phase 6): one labelled group with two
// keyboard-operable buttons; a choice PUTs the answer and calls onSaved; the
// group is aria-busy and both buttons disabled while saving; a failure shows
// its text and keeps the question up. The API is mocked at putProjectVcsProvider.

import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { VcsProviderAsk } from './VcsProviderAsk';

const api = vi.hoisted(() => ({ putProjectVcsProvider: vi.fn() }));

vi.mock('../api', async (importOriginal) => {
  const real = await importOriginal<typeof import('../api')>();
  return { ...real, putProjectVcsProvider: api.putProjectVcsProvider };
});

afterEach(cleanup);

beforeEach(() => {
  api.putProjectVcsProvider.mockReset();
});

describe('VcsProviderAsk', () => {
  it('asks which service hosts the host, in a labelled group with two buttons', () => {
    render(<VcsProviderAsk projectId={7} host="git.example.com" onSaved={vi.fn()} />);
    const group = screen.getByRole('group', { name: 'Which service hosts git.example.com?' });
    expect(group.getAttribute('aria-busy')).toBe('false');
    expect(screen.getByRole('button', { name: 'GitHub' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'GitLab' })).toBeTruthy();
    expect(screen.getByRole('region', { name: 'Code host' })).toBeTruthy();
  });

  it('falls back to "this repository" without a host', () => {
    render(<VcsProviderAsk projectId={7} host="" onSaved={vi.fn()} />);
    expect(screen.getByRole('group', { name: 'Which service hosts this repository?' })).toBeTruthy();
  });

  it('stores the chosen provider, is busy while saving, then calls onSaved', async () => {
    let resolve: () => void = () => undefined;
    api.putProjectVcsProvider.mockReturnValue(
      new Promise<void>((r) => {
        resolve = r;
      }),
    );
    const onSaved = vi.fn();
    render(<VcsProviderAsk projectId={7} host="git.example.com" onSaved={onSaved} />);

    const gitlab = screen.getByRole('button', { name: 'GitLab' });
    gitlab.focus();
    expect(document.activeElement).toBe(gitlab);
    fireEvent.click(gitlab);

    expect(api.putProjectVcsProvider).toHaveBeenCalledWith(7, 'gitlab');
    expect(screen.getByRole('group').getAttribute('aria-busy')).toBe('true');
    expect(gitlab.getAttribute('aria-busy')).toBe('true');
    expect((gitlab as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole('button', { name: 'GitHub' }) as HTMLButtonElement).disabled).toBe(true);
    expect(onSaved).not.toHaveBeenCalled();

    await act(async () => {
      resolve();
      await Promise.resolve();
    });
    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1));
    expect(screen.getByRole('group').getAttribute('aria-busy')).toBe('false');
  });

  it('shows the error and keeps the question when saving fails', async () => {
    api.putProjectVcsProvider.mockRejectedValue(new Error('settings.local.json is not a JSON object'));
    const onSaved = vi.fn();
    render(<VcsProviderAsk projectId={7} host="git.example.com" onSaved={onSaved} />);

    fireEvent.click(screen.getByRole('button', { name: 'GitHub' }));

    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('settings.local.json is not a JSON object');
    expect(onSaved).not.toHaveBeenCalled();
    expect(api.putProjectVcsProvider).toHaveBeenCalledWith(7, 'github');
    const github = screen.getByRole('button', { name: 'GitHub' }) as HTMLButtonElement;
    expect(github.disabled).toBe(false);
  });
});
