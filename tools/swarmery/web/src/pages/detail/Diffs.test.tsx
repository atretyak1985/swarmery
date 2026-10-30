// @vitest-environment jsdom
//
// The Diff tab's contract (handoff §5): deletions first, then edits, then new
// files; +N −M on every row; a 5-block share; the unified diff one click away.

import { afterEach, describe, expect, it } from 'vitest';
import type { FileChange, FileChangeType } from '../../api/types';
import { cleanup, fireEvent, render, screen } from '../../test/render';
import { Diffs, diffBlocks, groupByRisk } from './Diffs';

let nextId = 1;

function change(filePath: string, changeType: FileChangeType, additions: number, deletions: number, diff: string | null = null): FileChange {
  const id = nextId;
  nextId += 1;
  return { id, eventId: id, filePath, changeType, additions, deletions, diff, outOfScope: false };
}

afterEach(cleanup);

describe('groupByRisk', () => {
  it('sorts D, then M, then A', () => {
    const groups = groupByRisk([
      change('new.go', 'create', 10, 0),
      change('edited.go', 'edit', 3, 1),
      change('gone.go', 'delete', 0, 61),
    ]);
    expect(groups.map((g) => g.filePath)).toEqual(['gone.go', 'edited.go', 'new.go']);
  });

  it('sums a file edited twice into one row', () => {
    const groups = groupByRisk([change('a.go', 'edit', 3, 1), change('a.go', 'edit', 2, 4)]);
    expect(groups).toHaveLength(1);
    expect(groups[0]).toMatchObject({ additions: 5, deletions: 5 });
  });

  it('ranks a file both edited and deleted as a deletion', () => {
    const groups = groupByRisk([change('b.go', 'create', 1, 0), change('a.go', 'edit', 1, 0), change('a.go', 'delete', 0, 9)]);
    expect(groups[0]?.filePath).toBe('a.go');
    expect(groups[0]?.changeType).toBe('delete');
  });
});

describe('diffBlocks', () => {
  it('is always five blocks', () => {
    expect(diffBlocks(0, 0)).toHaveLength(5);
    expect(diffBlocks(142, 0)).toEqual(['add', 'add', 'add', 'add', 'add']);
    expect(diffBlocks(0, 61)).toEqual(['del', 'del', 'del', 'del', 'del']);
  });

  it('splits by the added share', () => {
    expect(diffBlocks(6, 14)).toEqual(['add', 'add', 'del', 'del', 'del']);
  });

  it('leaves a file with no counted lines empty', () => {
    expect(diffBlocks(0, 0)).toEqual(['none', 'none', 'none', 'none', 'none']);
  });
});

describe('Diffs', () => {
  it('shows +N −M and opens the unified diff on a click', () => {
    render(<Diffs changes={[change('a.go', 'edit', 7, 2, '@@ -1 +1 @@\n-old\n+new')]} />);
    expect(screen.getByText('+7')).toBeTruthy();
    expect(screen.getByText('−2')).toBeTruthy();
    expect(screen.queryByText('+new')).toBeNull();
    fireEvent.click(screen.getByRole('button', { expanded: false }));
    expect(screen.getByText('+new')).toBeTruthy();
  });

  it('opens the file a usage link pointed at', () => {
    render(<Diffs changes={[change('a.go', 'edit', 1, 0, '+line')]} openPath="a.go" />);
    expect(screen.getByText('+line')).toBeTruthy();
  });

  it('says so when nothing changed', () => {
    render(<Diffs changes={[]} />);
    expect(screen.getByText('No files changed yet.')).toBeTruthy();
  });
});
