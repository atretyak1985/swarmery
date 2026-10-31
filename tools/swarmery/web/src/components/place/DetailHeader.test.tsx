// @vitest-environment jsdom
//
// The one header band (Swarmery v4). The claims:
//
//   1. The band: page colour, a line under it, 18px above the content, 16px below it
//      unless tabs close it, and the design's negative margin when it bleeds.
//   2. A banner stands inside the band, above the content.
//   3. The blocker banner leads to Needs you and leaves out what it was not given.
//
// Runs with the rest of the web suite: `npm test`.

import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render, screen } from '../../test/render';
import { BlockerBanner, DetailHeader, NoticeBand } from './DetailHeader';

afterEach(cleanup);

function band(container: HTMLElement): HTMLElement {
  return container.firstElementChild as HTMLElement;
}

describe('DetailHeader', () => {
  it('is the page colour with a line under it, closed by 16px', () => {
    const { container } = render(<DetailHeader>content</DetailHeader>);
    expect(band(container).className).toContain('border-b');
    expect(band(container).className).toContain('bg-bg');
    const inner = screen.getByText('content');
    expect(inner.className).toContain('pt-[18px]');
    expect(inner.className).toContain('pb-4');
    expect(inner.className).toContain('desk:px-7');
  });

  it('has no foot when tabs close it', () => {
    render(<DetailHeader flush>content</DetailHeader>);
    expect(screen.getByText('content').className).not.toContain('pb-4');
  });

  it('reaches out of a padded column only when asked', () => {
    const { container } = render(<DetailHeader>content</DetailHeader>);
    expect(band(container).className).not.toContain('-mx-7');
    cleanup();
    const bled = render(<DetailHeader bleed>content</DetailHeader>);
    expect(band(bled.container).className).toContain('desk:-mx-7');
  });

  it('puts a banner above the content, inside the band', () => {
    const { container } = render(<DetailHeader banner={<NoticeBand>paused</NoticeBand>}>content</DetailHeader>);
    const notice = screen.getByText('paused');
    expect(band(container).contains(notice)).toBe(true);
    expect(notice.compareDocumentPosition(screen.getByText('content'))).toBe(Node.DOCUMENT_POSITION_FOLLOWING);
  });

  it('stays at the top of the scroll from desk up, and only there', () => {
    const { container } = render(<DetailHeader>content</DetailHeader>);
    const classes = band(container).className.split(' ');
    expect(classes).toContain('desk:sticky');
    expect(classes).toContain('desk:top-0');
    expect(classes).not.toContain('sticky');
  });

  it('publishes its height for what anchors beneath it, and takes it back', () => {
    let measure = (): void => {};
    vi.stubGlobal(
      'ResizeObserver',
      class {
        constructor(callback: () => void) {
          measure = callback;
        }
        observe(): void {}
        disconnect(): void {}
      },
    );
    const { container, unmount } = render(<DetailHeader>content</DetailHeader>);
    vi.spyOn(band(container), 'offsetHeight', 'get').mockReturnValue(120);
    measure();
    expect(document.documentElement.style.getPropertyValue('--detail-band-h')).toBe('120px');
    unmount();
    expect(document.documentElement.style.getPropertyValue('--detail-band-h')).toBe('');
    vi.unstubAllGlobals();
  });
});

describe('DetailHeader, two bands in one column', () => {
  it('sticks the child right under the parent, whose height it reads', () => {
    let measure = (): void => {};
    vi.stubGlobal(
      'ResizeObserver',
      class {
        constructor(callback: () => void) {
          measure = callback;
        }
        observe(): void {}
        disconnect(): void {}
      },
    );
    const { container } = render(<DetailHeader stack="parent">plan</DetailHeader>);
    vi.spyOn(band(container), 'offsetHeight', 'get').mockReturnValue(170);
    measure();
    expect(document.documentElement.style.getPropertyValue('--detail-parent-band-h')).toBe('170px');
    expect(document.documentElement.style.getPropertyValue('--detail-band-h')).toBe('');
    expect(band(container).className).toContain('desk:top-0');
    cleanup();
    vi.unstubAllGlobals();

    const child = render(<DetailHeader stack="child">phase</DetailHeader>);
    expect(band(child.container).style.top).toBe('var(--detail-parent-band-h, 0px)');
    expect(band(child.container).className).toContain('bg-surface');
    expect(band(child.container).className).not.toContain('desk:top-0');
  });
});

describe('BlockerBanner', () => {
  function banner(detail?: string): void {
    render(
      <MemoryRouter>
        <BlockerBanner to="/p/shop/needs-you" lead="2 phases wait on a decision from you" detail={detail} />
      </MemoryRouter>,
    );
  }

  it('leads to Needs you', () => {
    banner('1 guard stop, 1 manual check');
    const link = screen.getByRole('link');
    expect(link.getAttribute('href')).toBe('/p/shop/needs-you');
    expect(link.textContent).toBe('2 phases wait on a decision from you · 1 guard stop, 1 manual checkNeeds you →');
  });

  it('leaves the grey part out when there is none', () => {
    banner();
    expect(screen.getByRole('link').textContent).toBe('2 phases wait on a decision from youNeeds you →');
  });
});
