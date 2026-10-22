// The architecture map is repository content, so the page frames it in a
// sandbox and talks to it only by postMessage: the iframe carries
// sandbox="allow-scripts", and the page never reads the frame's document
// (Architecture.source.test.ts pins the same at the source level).

import { afterEach, describe, expect, it, vi } from 'vitest';
import type { ArchitectureProject, ToolsResponse } from '../api/types';
import { render, screen, waitFor } from '../test/render';

const PROJECT: ArchitectureProject = {
  id: 7,
  slug: 'demo',
  name: 'Demo',
  hasMap: true,
  builtAt: '2026-10-22T10:00:00Z',
  mapPath: '/api/projects/7/architecture/architecture-map.html',
  analyzedAtCommit: null,
  headCommit: null,
  commitsBehind: null,
  touchedModules: null,
  moduleCount: null,
  provision: null,
};

vi.mock('../api', () => ({
  fetchTools: vi.fn(async () => ({ architecture: { projects: [PROJECT] } }) as unknown as ToolsResponse),
  fetchExploration: vi.fn(async () => {
    throw new Error('not under test');
  }),
  fetchArchitectureBlast: vi.fn(async () => {
    throw new Error('not under test');
  }),
  rebuildArchitectureMap: vi.fn(),
  toggleProjectPlugin: vi.fn(),
}));

import { Architecture } from './Architecture';

afterEach(() => {
  vi.restoreAllMocks();
});

describe('Architecture map embed', () => {
  it('sandboxes the frame without allow-same-origin', async () => {
    render(<Architecture />);
    const frame = await screen.findByTitle('Architecture map');
    expect(frame.getAttribute('sandbox')).toBe('allow-scripts');
  });

  it('pushes theme, highlight and expanded state by postMessage only', async () => {
    const docGetter = vi.spyOn(HTMLIFrameElement.prototype, 'contentDocument', 'get');
    render(<Architecture />);
    const frame = (await screen.findByTitle('Architecture map')) as HTMLIFrameElement;
    const win = frame.contentWindow;
    expect(win).not.toBeNull();
    const post = vi.spyOn(win as Window, 'postMessage').mockImplementation(() => undefined);

    frame.dispatchEvent(new Event('load'));

    await waitFor(() => {
      const types = post.mock.calls.map(([msg]) => (msg as { type: string }).type);
      expect(types).toEqual(
        expect.arrayContaining(['swarmery:theme', 'swarmery:highlight', 'swarmery:expanded']),
      );
    });
    const theme = post.mock.calls
      .map(([msg]) => msg as { type: string; theme?: string; vars?: unknown; css?: unknown })
      .find((m) => m.type === 'swarmery:theme');
    expect(theme?.theme === 'light' || theme?.theme === 'dark').toBe(true);
    expect(typeof theme?.css).toBe('string');
    expect(typeof theme?.vars).toBe('object');
    for (const call of post.mock.calls) expect(call[1]).toBe('*');
    expect(docGetter).not.toHaveBeenCalled();
  });
});
