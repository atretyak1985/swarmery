// @vitest-environment jsdom
//
// The sidebar icon set (phase 3): every place has a drawn outline, all at the
// same stroke weight, and the component never falls back to a glyph character.

import { describe, expect, it } from 'vitest';
import { render } from '../test/render';
import { PLACES } from '../lib/nav';
import { NavIcon, type NavIconName } from './NavIcon';

const NAMES: readonly NavIconName[] = [...PLACES.map((p) => p.id), 'search'];

describe('NavIcon', () => {
  it('draws every place, plus search, as a stroked outline on the shared viewBox', () => {
    for (const name of NAMES) {
      const { container, unmount } = render(<NavIcon name={name} />);
      const svg = container.querySelector('svg');
      expect(svg, name).toBeTruthy();
      expect(svg?.getAttribute('viewBox')).toBe('0 0 24 24');
      expect(svg?.getAttribute('stroke-width')).toBe('1.6');
      expect(svg?.getAttribute('aria-hidden')).toBe('true');
      // No font-glyph text content anywhere in the icon.
      expect(svg?.textContent).toBe('');
      unmount();
    }
  });

  it('sizes the svg from the size prop, defaulting to 16', () => {
    const { container: a } = render(<NavIcon name="today" />);
    expect(a.querySelector('svg')?.getAttribute('width')).toBe('16');
    const { container: b } = render(<NavIcon name="today" size={20} />);
    expect(b.querySelector('svg')?.getAttribute('width')).toBe('20');
  });
});
