// scrollToDiffGroup: the rail → Diffs jump. Regression for the live bug where
// clicking a file in "files changed" while the Diffs tab was already open did
// nothing (the tab-switch effect never re-ran).

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { scrollToDiffGroup } from './diffJump';

function panelWith(paths: string[]): HTMLElement {
  const panel = document.createElement('div');
  for (const path of paths) {
    const group = document.createElement('div');
    group.dataset.diffPath = path;
    panel.appendChild(group);
  }
  document.body.appendChild(panel);
  return panel;
}

describe('scrollToDiffGroup', () => {
  const scrollIntoView = vi.fn();
  beforeEach(() => {
    // jsdom has no layout: stub the one call that matters.
    Element.prototype.scrollIntoView = scrollIntoView;
  });
  afterEach(() => {
    scrollIntoView.mockReset();
    document.body.innerHTML = '';
  });

  it('scrolls exactly the group whose path matches and reports success', () => {
    const panel = panelWith(['/repo/a.ts', '/repo/b.ts']);
    expect(scrollToDiffGroup(panel, '/repo/b.ts')).toBe(true);
    expect(scrollIntoView).toHaveBeenCalledTimes(1);
    expect(scrollIntoView.mock.instances[0]).toBe(panel.children[1]);
    expect(scrollIntoView).toHaveBeenCalledWith({ block: 'start' });
  });

  it('reports false and scrolls nothing when the path is not rendered', () => {
    const panel = panelWith(['/repo/a.ts']);
    expect(scrollToDiffGroup(panel, '/repo/missing.ts')).toBe(false);
    expect(scrollIntoView).not.toHaveBeenCalled();
  });

  it('matches paths with quotes and backslashes without any escaping', () => {
    const odd = '/repo/it\'s "quoted"\\weird.md';
    const panel = panelWith(['/repo/a.ts', odd]);
    expect(scrollToDiffGroup(panel, odd)).toBe(true);
    expect(scrollIntoView.mock.instances[0]).toBe(panel.children[1]);
  });
});
