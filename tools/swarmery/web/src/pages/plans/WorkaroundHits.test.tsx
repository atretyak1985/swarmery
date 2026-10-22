// @vitest-environment jsdom
//
// WorkaroundHits: the phase card's "changed the plan" list — the matched word
// marked inside its sentence, one sentence of context each side, the fold after
// five, the note for hits past the per-session cap, and nothing at all when the
// agent never said such a word.

import { cleanup, fireEvent, render, screen, within } from '../../test/render';
import { afterEach, describe, expect, it } from 'vitest';
import type { WorkaroundHit } from '../../api/types';
import { HITS_FOLDED, WorkaroundHits, markWord } from './WorkaroundHits';

const hit = (over: Partial<WorkaroundHit>): WorkaroundHit => ({
  id: 1,
  sessionUuid: 'run-1',
  turnSeq: 9,
  turnAt: '2026-10-09T12:00:00Z',
  agentName: '',
  word: 'instead',
  sentence: 'That would have turned the continuation into a planned task instead of a mid-phase discovery.',
  before: 'The run teardown removes the worktree but keeps the branch.',
  after: 'PHASE DONE',
  ...over,
});

afterEach(cleanup);

describe('WorkaroundHits', () => {
  it('lists each hit with its word marked and the sentence on each side', () => {
    render(<WorkaroundHits phaseId={4} hits={[hit({})]} total={1} />);
    const section = screen.getByRole('region', { name: 'Places where the agent changed the plan' });
    expect(within(section).getByText('Changed the plan · 1')).toBeTruthy();
    expect(within(section).getByText('turn 9')).toBeTruthy();
    expect(within(section).getByText('The run teardown removes the worktree but keeps the branch.')).toBeTruthy();
    expect(within(section).getByText('PHASE DONE')).toBeTruthy();
    const mark = section.querySelector('mark');
    expect(mark?.textContent).toBe('instead');
    expect(mark?.parentElement?.textContent).toBe(hit({}).sentence);
  });

  it('names the subagent and marks a Ukrainian phrase across whitespace', () => {
    render(
      <WorkaroundHits
        phaseId={4}
        hits={[hit({ word: 'поки що', sentence: 'Поки  що лишаю як є.', agentName: 'core:implementation-agent', before: '', after: '' })]}
        total={1}
      />,
    );
    expect(screen.getByText('core:implementation-agent')).toBeTruthy();
    expect(document.querySelector('mark')?.textContent).toBe('Поки  що');
  });

  it('folds after five and notes the hits past the cap', () => {
    const hits = Array.from({ length: HITS_FOLDED + 2 }, (_, i) =>
      hit({ id: i + 1, word: 'skip', sentence: `Step ${i}: I skip it.`, before: '', after: '' }),
    );
    render(<WorkaroundHits phaseId={4} hits={hits} total={hits.length + 3} />);
    expect(screen.getAllByRole('listitem')).toHaveLength(HITS_FOLDED);
    expect(screen.getByText('3 more past the per-session cap — counted, not stored.')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Show all 7' }));
    expect(screen.getAllByRole('listitem')).toHaveLength(HITS_FOLDED + 2);
    fireEvent.click(screen.getByRole('button', { name: 'Show fewer' }));
    expect(screen.getAllByRole('listitem')).toHaveLength(HITS_FOLDED);
  });

  it('renders nothing for a phase without hits', () => {
    const { container } = render(<WorkaroundHits phaseId={4} hits={[]} total={0} />);
    expect(container.innerHTML).toBe('');
  });
});

describe('markWord', () => {
  it('marks every case-insensitive occurrence and keeps the rest', () => {
    expect(markWord('Skip it, then skip that.', 'skip')).toEqual([
      { text: 'Skip', hit: true },
      { text: ' it, then ', hit: false },
      { text: 'skip', hit: true },
      { text: ' that.', hit: false },
    ]);
  });
  it('escapes regex characters and falls back when the word is absent', () => {
    expect(markWord('a (b) c', '(b)')).toEqual([
      { text: 'a ', hit: false },
      { text: '(b)', hit: true },
      { text: ' c', hit: false },
    ]);
    expect(markWord('nothing here', 'instead')).toEqual([{ text: 'nothing here', hit: false }]);
  });
});
