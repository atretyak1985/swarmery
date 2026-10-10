// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen } from '../test/render';
import { afterEach, describe, expect, it } from 'vitest';
import type { TaskDiff } from '../api/types';
import { DiffView, splitPatch } from './DiffView';

afterEach(cleanup);

const PATCH = [
  'diff --git a/alpha.txt b/alpha.txt',
  '+one',
  'diff --git a/old.txt b/new.txt',
  '-x',
].join('\n');

const DIFF: TaskDiff = {
  base: '0123456789abcdef',
  branch: 'swarm/phase-7',
  commits: [{ sha: 'deadbeefcafe', subject: 'add alpha' }],
  files: [{ path: 'alpha.txt', additions: 1, deletions: 0 }],
  patch: PATCH,
  patchTruncated: true,
};

describe('splitPatch', () => {
  it('splits on diff --git headers and keeps the b/ side of a rename', () => {
    expect(splitPatch(PATCH).map((s) => s.path)).toEqual(['alpha.txt', 'new.txt']);
  });

  it('keeps a header-less leading slice under an empty path', () => {
    expect(splitPatch('@@ -1 +1 @@\n-a\n+b').map((s) => s.path)).toEqual(['']);
  });

  it('returns nothing for an empty patch', () => {
    expect(splitPatch('  \n')).toEqual([]);
  });
});

describe('DiffView', () => {
  it('renders the branch line, commits, file counts, per-file patches and the truncation note', () => {
    render(<DiffView diff={DIFF} />);
    expect(screen.getByText('swarm/phase-7 · base 0123456789')).toBeTruthy();
    expect(screen.getByText('add alpha')).toBeTruthy();
    expect(screen.getByText('deadbeef')).toBeTruthy();
    expect(screen.getByText(/diff truncated at 200 KB/)).toBeTruthy();
    const block = screen.getByRole('button', { name: /new\.txt/ });
    expect(block.getAttribute('aria-expanded')).toBe('false');
    fireEvent.click(block);
    expect(block.getAttribute('aria-expanded')).toBe('true');
  });

  it('says so when the branch carries no commits', () => {
    render(<DiffView diff={{ ...DIFF, commits: [], files: [], patch: '', patchTruncated: false }} />);
    expect(screen.getByText(/the agent\s+changed nothing/)).toBeTruthy();
  });
});
