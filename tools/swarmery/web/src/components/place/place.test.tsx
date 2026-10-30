// @vitest-environment jsdom
//
// The place primitives (Swarmery v4). The claims:
//
//   1. StateGlyph: each state has its own shape, and the guard stop differs from
//      the plain stop only in colour on purpose — same square, red.
//   2. ListRow: the design's grid, a selected row is marked, and it renders as a
//      link when selection lives in the URL.
//   3. ListGroup + useAccordion: one group open, the first by default, a click on
//      another closes the first, and every count stays visible.
//   4. ListPane: wires the row keys, and they stay dead while typing in its search.
//   5. NumbersStrip: a bar only where a limit was given, clamped to 100.
//   6. DecisionCard: the main button repeats the picked option, tiers differ by
//      shape.
//
// Runs with the rest of the web suite: `npm test`.

import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '../../test/render';
import { DecisionCard } from './DecisionCard';
import { ListGroup, useAccordion } from './ListGroup';
import { ListPane } from './ListPane';
import { ListRow } from './ListRow';
import { NumbersStrip } from './NumbersStrip';
import { PlaceLayout } from './PlaceLayout';
import { StateGlyph, type GlyphState } from './StateGlyph';

afterEach(cleanup);

describe('StateGlyph', () => {
  const cases: [GlyphState, string, string, string][] = [
    ['needs', '●', 'text-amber', 'needs you'],
    ['working', '✽', 'text-green', 'working'],
    ['done', '✓', 'text-ink-dim', 'done'],
    ['failed', '✗', 'text-red', 'failed'],
    ['stopped', '■', 'text-ink-faint', 'stopped'],
    ['pending', '○', 'text-ink-faint', 'pending'],
    ['guard', '■', 'text-red', 'guard stop'],
    ['manual', '☐', 'text-ink-dim', 'manual check'],
    ['permission', '▲', 'text-amber', 'permission'],
    ['question', '?', 'text-amber', 'question'],
  ];

  it.each(cases)('%s renders %s in %s, named for a screen reader', (state, glyph, tone, name) => {
    render(<StateGlyph state={state} />);
    const el = screen.getByRole('img', { name });
    expect(el.textContent).toBe(glyph);
    expect(el.className).toContain(tone);
  });

  it('moves only the two states that are happening now', () => {
    render(
      <>
        <StateGlyph state="needs" />
        <StateGlyph state="working" />
        <StateGlyph state="done" />
      </>,
    );
    expect(screen.getByRole('img', { name: 'needs you' }).className).toContain('animate-blink-dot');
    expect(screen.getByRole('img', { name: 'working' }).className).toContain('animate-glyph-spin');
    expect(screen.getByRole('img', { name: 'done' }).className).not.toContain('animate-');
  });
});

describe('ListRow', () => {
  it('lays out glyph, title, tag, summary, age and PR on the three-column grid', () => {
    render(
      <ListRow
        glyph={<StateGlyph state="working" />}
        title="Add WebSocket updates"
        tag="plan run"
        summary="Editing ws.ts"
        age="3h"
        pr="#318"
      />,
    );
    const row = screen.getByRole('button');
    expect(row.className).toContain('grid-cols-[18px_minmax(0,1fr)_auto]');
    for (const text of ['Add WebSocket updates', 'plan run', 'Editing ws.ts', '3h', '#318']) {
      expect(screen.getByText(text)).toBeTruthy();
    }
  });

  it('marks the selected row with the brand edge on the selection fill', () => {
    render(<ListRow glyph={null} title="one" selected />);
    const row = screen.getByRole('button');
    expect(row.getAttribute('aria-current')).toBe('true');
    expect(row.className).toContain('border-brand');
    expect(row.className).toContain('bg-sel');
  });

  it('uses the tighter padding when compact', () => {
    render(<ListRow glyph={null} title="one" compact />);
    expect(screen.getByRole('button').className).toContain('py-1.5');
  });

  it('colours a failed summary red and leaves the rest grey', () => {
    render(<ListRow glyph={null} title="one" summary="it broke" summaryTone="failed" />);
    expect(screen.getByText('it broke').className).toContain('text-red');
  });

  it('is a link when selection lives in the URL, and reports the click', () => {
    const onSelect = vi.fn();
    render(
      <MemoryRouter>
        <ListRow glyph={null} title="one" to="/sessions/1" onSelect={onSelect} />
      </MemoryRouter>,
    );
    const link = screen.getByRole('link');
    expect(link.getAttribute('href')).toBe('/sessions/1');
    fireEvent.click(link);
    expect(onSelect).toHaveBeenCalledOnce();
  });
});

function Groups({ keys }: { keys: string[] }): JSX.Element {
  const accordion = useAccordion(keys);
  return (
    <>
      {keys.map((key, i) => (
        <ListGroup
          key={key}
          label={key}
          count={i + 1}
          open={accordion.isOpen(key)}
          onToggle={() => accordion.open(key)}
        >
          <p>{`${key} body`}</p>
        </ListGroup>
      ))}
    </>
  );
}

describe('ListGroup', () => {
  it('opens only the first group by default and keeps every count visible', () => {
    render(<Groups keys={['Overview', 'Friction', 'Agents']} />);
    expect(screen.getByText('Overview body')).toBeTruthy();
    expect(screen.queryByText('Friction body')).toBeNull();
    expect(screen.queryByText('Agents body')).toBeNull();
    for (const n of ['1', '2', '3']) expect(screen.getByText(n)).toBeTruthy();
  });

  it('closes the open group when another one opens', () => {
    render(<Groups keys={['Overview', 'Friction', 'Agents']} />);
    fireEvent.click(screen.getByRole('button', { name: /Agents/ }));
    expect(screen.getByText('Agents body')).toBeTruthy();
    expect(screen.queryByText('Overview body')).toBeNull();
    expect(screen.getByRole('button', { name: /Overview/ }).getAttribute('aria-expanded')).toBe('false');
  });

  it('keeps a group open when its own header is clicked: a place always shows one', () => {
    render(<Groups keys={['Overview', 'Friction']} />);
    fireEvent.click(screen.getByRole('button', { name: /Overview/ }));
    expect(screen.getByText('Overview body')).toBeTruthy();
  });

  it('falls back to the first group when the open one leaves the list', () => {
    const { rerender } = render(<Groups keys={['Overview', 'Friction']} />);
    fireEvent.click(screen.getByRole('button', { name: /Friction/ }));
    rerender(<Groups keys={['Overview']} />);
    expect(screen.getByText('Overview body')).toBeTruthy();
  });

  it('is plain text, not a control, when the group cannot collapse', () => {
    render(
      <ListGroup label="Working" count={2}>
        <p>row</p>
      </ListGroup>,
    );
    expect(screen.queryByRole('button')).toBeNull();
    expect(screen.getByText('row')).toBeTruthy();
  });
});

describe('ListPane', () => {
  function renderPane(onNext: () => void): void {
    render(
      <ListPane
        title="Sessions"
        sub="4 sessions"
        query=""
        onQueryChange={() => undefined}
        searchHint="filter"
        keys={{ onNext }}
      />,
    );
  }

  it('moves down the list on j', () => {
    const onNext = vi.fn();
    renderPane(onNext);
    fireEvent.keyDown(document.body, { key: 'j' });
    expect(onNext).toHaveBeenCalledOnce();
  });

  it('leaves j to the search field while the operator types there', () => {
    const onNext = vi.fn();
    renderPane(onNext);
    fireEvent.keyDown(screen.getByRole('searchbox'), { key: 'j' });
    expect(onNext).not.toHaveBeenCalled();
  });

  it('shows `+ new` only where the place dispatches', () => {
    renderPane(() => undefined);
    expect(screen.queryByRole('button', { name: '+ new' })).toBeNull();
  });

  it('hides the x hint when x does nothing here', () => {
    renderPane(() => undefined);
    expect(screen.queryByText(/^x /)).toBeNull();
    expect(screen.getByText('e open')).toBeTruthy();
  });
});

describe('PlaceLayout', () => {
  it('stacks below desk and only becomes columns at desk', () => {
    const { container } = render(<PlaceLayout list={<p>list</p>} detail={<p>detail</p>} />);
    const frame = container.firstElementChild as HTMLElement;
    expect(frame.className).toContain('flex-col');
    expect(frame.className).toContain('desk:grid-cols-[clamp(280px,28vw,360px)_minmax(0,1fr)]');
    expect(screen.getByText('list').compareDocumentPosition(screen.getByText('detail'))).toBe(
      Node.DOCUMENT_POSITION_FOLLOWING,
    );
  });
});

describe('NumbersStrip', () => {
  it('draws a bar only where a limit was given, and clamps it', () => {
    const { container } = render(
      <NumbersStrip
        items={[
          { label: 'cost', value: '$12' },
          { label: 'context', value: '120 %', tone: 'red', barPercent: 120 },
        ]}
      />,
    );
    const bars = container.querySelectorAll<HTMLElement>('[style]');
    expect(bars).toHaveLength(1);
    expect(bars[0]?.style.width).toBe('100%');
    expect(bars[0]?.className).toContain('bg-red');
    expect(screen.getByText('120 %').className).toContain('text-red');
  });
});

describe('DecisionCard', () => {
  const options = [
    { id: 'a', label: 'Accept doctor at 19/20', consequence: 'Ships now.' },
    { id: 'b', label: 'Fix the last check', consequence: 'One more run.' },
  ];

  it('repeats the picked option on the main button', () => {
    const onPick = vi.fn();
    const { rerender } = render(
      <DecisionCard
        title="Which one?"
        options={options}
        picked="a"
        onPick={onPick}
        primary={{ label: 'answer', onClick: () => undefined }}
      />,
    );
    expect(screen.getByRole('button', { name: /answer: Accept doctor at 19\/20/ })).toBeTruthy();
    fireEvent.click(screen.getByRole('radio', { name: /Fix the last check/ }));
    expect(onPick).toHaveBeenCalledWith('b');
    rerender(
      <DecisionCard
        title="Which one?"
        options={options}
        picked="b"
        onPick={onPick}
        primary={{ label: 'answer', onClick: () => undefined }}
      />,
    );
    expect(screen.getByRole('button', { name: /answer: Fix the last check/ })).toBeTruthy();
  });

  it('tells tiers apart by shape, not only by colour', () => {
    const primary = { label: 'allow once', onClick: () => undefined };
    render(
      <>
        <DecisionCard title="a" tier="destructive" primary={primary} />
        <DecisionCard title="b" tier="write" primary={primary} />
        <DecisionCard title="c" tier="question" primary={primary} />
      </>,
    );
    expect(screen.getByText('▲ destructive').className).toContain('rounded-[3px]');
    expect(screen.getByText('◆ write').className).toContain('border-dashed');
    expect(screen.getByText('? question').className).toContain('rounded-full');
  });

  it('wires every action and leaves out what was not given', () => {
    const primary = vi.fn();
    const secondary = vi.fn();
    const link = vi.fn();
    render(
      <DecisionCard
        kind="guard stop"
        kindTone="red"
        meta="phase 3"
        title="Stopped"
        code="psql -f drop.sql"
        facts="touched 2 protected files"
        primary={{ label: 'resume', onClick: primary }}
        secondary={{ label: 'stop', onClick: secondary }}
        link={{ label: 'phase review →', onClick: link }}
      />,
    );
    expect(screen.getByText('guard stop').className).toContain('text-red');
    expect(screen.getByText('psql -f drop.sql')).toBeTruthy();
    expect(screen.getByText('touched 2 protected files')).toBeTruthy();
    expect(screen.queryByRole('radiogroup')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: /resume/ }));
    fireEvent.click(screen.getByRole('button', { name: /stop/ }));
    fireEvent.click(screen.getByRole('button', { name: 'phase review →' }));
    expect(primary).toHaveBeenCalledOnce();
    expect(secondary).toHaveBeenCalledOnce();
    expect(link).toHaveBeenCalledOnce();
  });
});
