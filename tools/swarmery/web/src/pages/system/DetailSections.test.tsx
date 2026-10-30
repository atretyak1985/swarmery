import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { useState } from 'react';
import { afterEach, describe, expect, it } from 'vitest';
import { DetailSections } from './SectionTabs';

const TABS = ['overview', 'docs'] as const;

function Harness({ initial }: { initial: string | null }): JSX.Element {
  const [active, setActive] = useState<string | null>(initial);
  return (
    <DetailSections
      tabs={TABS}
      labels={{ overview: 'Overview', docs: 'Docs' }}
      active={active}
      onSelect={setActive}
      idPrefix="t"
      ariaLabel="sections"
    >
      <div>panel {active}</div>
    </DetailSections>
  );
}

afterEach(cleanup);

describe('DetailSections', () => {
  it('starts folded: the tabs show, no panel mounts, the first tab stays reachable', () => {
    render(<Harness initial={null} />);
    expect(screen.getAllByRole('tab')).toHaveLength(2);
    expect(screen.queryByRole('tabpanel')).toBeNull();
    expect(screen.getByRole('tab', { name: 'Overview' }).getAttribute('tabindex')).toBe('0');
    expect(screen.getByRole('tab', { name: 'Overview' }).getAttribute('aria-selected')).toBe('false');
  });

  it('opens on a click and folds on a second click of the same tab', () => {
    render(<Harness initial={null} />);
    fireEvent.click(screen.getByRole('tab', { name: 'Overview' }));
    expect(screen.getByRole('tabpanel').textContent).toBe('panel overview');
    fireEvent.click(screen.getByRole('tab', { name: 'Overview' }));
    expect(screen.queryByRole('tabpanel')).toBeNull();
  });

  it('a deep link opens its section, and arrow keys move between sections without folding', () => {
    render(<Harness initial="docs" />);
    expect(screen.getByRole('tabpanel').textContent).toBe('panel docs');
    fireEvent.keyDown(screen.getByRole('tab', { name: 'Docs' }), { key: 'ArrowLeft' });
    expect(screen.getByRole('tabpanel').textContent).toBe('panel overview');
    fireEvent.keyDown(screen.getByRole('tab', { name: 'Overview' }), { key: 'Home' });
    expect(screen.getByRole('tabpanel').textContent).toBe('panel overview');
  });
});
