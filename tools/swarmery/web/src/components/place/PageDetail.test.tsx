// @vitest-environment jsdom
//
// PageDetail and PagePlace (handoff §9). The claims:
//
//   1. A block renders only when it has data; an empty detail is eyebrow + title.
//   2. The trend colours its last bar only, and children render after the blocks.
//   3. The accordion keeps one group open, `?tab=` is the group and `?item=` the
//      row, and picking a group clears the row in one write.
//   4. j/k move inside the open group; the filter hides what does not match.

import { MemoryRouter, useLocation } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, within } from '../../test/render';
import { PageDetail, PagePlace, type PlaceGroup, usePlaceParams } from './PageDetail';

const GROUPS: readonly PlaceGroup[] = [
  {
    key: 'one',
    label: 'One',
    items: [
      { key: 'a', glyph: '●', title: 'Alpha', summary: 'first' },
      { key: 'b', glyph: '●', title: 'Beta', summary: 'second' },
    ],
  },
  { key: 'two', label: 'Two', items: [{ key: 'c', glyph: '●', title: 'Gamma' }] },
];

function Harness(): JSX.Element {
  const { tab, item, pick } = usePlaceParams<'one' | 'two'>(['one', 'two'], 'one');
  const { search } = useLocation();
  const open = GROUPS.find((g) => g.key === tab) ?? GROUPS[0];
  const selected = open?.items.find((i) => i.key === item)?.key ?? open?.items[0]?.key ?? null;
  return (
    <>
      <PagePlace
        title="Place"
        groups={GROUPS}
        openGroup={tab}
        selected={selected}
        onOpenGroup={(key) => pick(key as 'one' | 'two')}
        onSelect={(key) => pick(tab, key)}
        detail={<div data-testid="selected">{selected}</div>}
      />
      <div data-testid="search">{search}</div>
    </>
  );
}

function renderPlace(url: string): void {
  render(
    <MemoryRouter initialEntries={[url]}>
      <Harness />
    </MemoryRouter>,
  );
}

afterEach(cleanup);

describe('PageDetail', () => {
  it('shows only the blocks that have data', () => {
    render(
      <MemoryRouter>
        <PageDetail kind="health" title="A sentence." />
      </MemoryRouter>,
    );
    expect(screen.getByRole('heading', { level: 2, name: 'A sentence.' })).toBeTruthy();
    expect(screen.queryByRole('switch')).toBeNull();
    expect(screen.queryByRole('button')).toBeNull();
    expect(document.querySelector('pre')).toBeNull();
  });

  it('renders every block, the last trend bar coloured and children under them', () => {
    const onChange = vi.fn();
    const onPick = vi.fn();
    const { container } = render(
      <MemoryRouter>
        <PageDetail
          kind="kind"
          meta="meta"
          title="Title"
          lead="Lead."
          stats={[{ label: 'runs', value: '12' }]}
          barsTitle="bars"
          bars={[{ label: 'one', percent: 150, value: '150 %', tone: 'red' }]}
          trendTitle="trend"
          trendNote="note"
          trend={[1, 2, 4]}
          trendTone="green"
          kv={[{ k: 'key', v: 'value' }]}
          paras={['A paragraph.']}
          code="code"
          rowsTitle="rows"
          rows={[{ key: 'r', glyph: '✓', label: 'row', sub: 'sub', right: 'right', age: '1 h' }]}
          toggles={[
            { key: 's', label: 'Switch', on: true, onChange },
            {
              key: 'p',
              label: 'Pick',
              options: [
                { value: 'x', label: 'x' },
                { value: 'y', label: 'y' },
              ],
              value: 'x',
              onPick,
            },
          ]}
          actions={[{ label: 'act', tone: 'brand' }]}
          link={{ label: 'more →', to: '/more' }}
        >
          <p>below</p>
        </PageDetail>
      </MemoryRouter>,
    );
    expect(screen.getByText('· meta')).toBeTruthy();
    expect(screen.getByText('A paragraph.')).toBeTruthy();
    expect(screen.getByText('code').tagName).toBe('PRE');
    const bars = [...container.querySelectorAll('[title]')];
    expect(bars.map((bar) => bar.className.includes('bg-green'))).toEqual([false, false, true]);
    const fill = container.querySelector('.bg-red') as HTMLElement;
    expect(fill.style.width).toBe('100%');
    fireEvent.click(screen.getByRole('switch', { name: 'Switch' }));
    expect(onChange).toHaveBeenCalledWith(false);
    fireEvent.click(screen.getByRole('radio', { name: 'y' }));
    expect(onPick).toHaveBeenCalledWith('y');
    expect(screen.getByRole('link', { name: 'more →' }).getAttribute('href')).toBe('/more');
    const text = container.textContent ?? '';
    expect(text.indexOf('act')).toBeLessThan(text.indexOf('below'));
  });
});

describe('PagePlace', () => {
  it('opens the first group by default and only one at a time', () => {
    renderPlace('/');
    const list = within(screen.getByRole('region', { name: 'Place' }));
    expect(list.getAllByRole('button', { expanded: true })).toHaveLength(1);
    expect(screen.getByRole('button', { name: /Alpha/ }).getAttribute('aria-current')).toBe('true');
    expect(screen.queryByRole('button', { name: /Gamma/ })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: /^Two/ }));
    expect(screen.getByRole('button', { name: /Gamma/ })).toBeTruthy();
    expect(screen.queryByRole('button', { name: /Alpha/ })).toBeNull();
    expect(list.getAllByRole('button', { expanded: true })).toHaveLength(1);
  });

  it('keeps the group in ?tab= and the row in ?item=, and a new group clears the row', () => {
    renderPlace('/?item=b');
    expect(screen.getByTestId('selected').textContent).toBe('b');
    fireEvent.click(screen.getByRole('button', { name: /^Two/ }));
    expect(screen.getByTestId('search').textContent).toBe('?tab=two');
    expect(screen.getByTestId('selected').textContent).toBe('c');
  });

  it('moves with j/k inside the open group', () => {
    renderPlace('/');
    fireEvent.keyDown(window, { key: 'j' });
    expect(screen.getByTestId('selected').textContent).toBe('b');
    fireEvent.keyDown(window, { key: 'j' });
    expect(screen.getByTestId('selected').textContent).toBe('b');
    fireEvent.keyDown(window, { key: 'k' });
    expect(screen.getByTestId('selected').textContent).toBe('a');
  });

  it('filters rows and hides groups with no match', () => {
    renderPlace('/');
    const search = screen.getByRole('searchbox');
    fireEvent.change(search, { target: { value: 'gamma' } });
    expect(screen.getByRole('button', { name: /Gamma/ })).toBeTruthy();
    expect(screen.queryByRole('button', { name: /^One/ })).toBeNull();
    fireEvent.change(search, { target: { value: 'nothing' } });
    expect(screen.getByText('Nothing matches.')).toBeTruthy();
  });
});
