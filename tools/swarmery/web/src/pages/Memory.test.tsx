// @vitest-environment jsdom
//
// Memory page (memory-engineering phase 2). The claims:
//
//   1. The file list renders even when the stale-fact lint request rejects —
//      the lint error stays inside its own panel.
//   2. Clicking a stale-fact row opens that file in the editor.

import { Profiler } from 'react';
import { cleanup, fireEvent, render, screen, waitFor } from '../test/render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../api';
import type { MemoryFile, MemoryFileContent, MemoryLintReport } from '../api/types';
import { Memory } from './Memory';

vi.mock('../workspace/ProjectContext', () => ({
  useProjectWorkspace: () => ({
    slug: 'shop',
    project: { id: 3, slug: 'shop', name: 'shop' },
    projectId: 3,
    loading: false,
  }),
}));

vi.mock('../api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../api')>()),
  fetchMemoryList: vi.fn(),
  fetchMemoryLint: vi.fn(),
  fetchMemoryFile: vi.fn(),
}));

const INDEX = '/home/dev/.claude/projects/-work-shop/memory/MEMORY.md';
const TOPIC = '/home/dev/.claude/projects/-work-shop/memory/checkout-rollout.md';

function file(path: string, kind: MemoryFile['kind'] = 'auto-memory'): MemoryFile {
  return {
    kind,
    path,
    name: path.slice(path.lastIndexOf('/') + 1),
    sizeBytes: 120,
    updatedAt: new Date().toISOString(),
    writable: true,
  };
}

function content(path: string): MemoryFileContent {
  return { path, kind: 'auto-memory', content: `# ${path}`, hash: 'h', writable: true };
}

beforeEach(() => {
  vi.mocked(api.fetchMemoryList).mockReset();
  vi.mocked(api.fetchMemoryLint).mockReset();
  vi.mocked(api.fetchMemoryFile).mockReset();
  vi.mocked(api.fetchMemoryList).mockResolvedValue({ files: [file(INDEX), file(TOPIC)] });
  vi.mocked(api.fetchMemoryFile).mockImplementation(async (_p, path) => content(path));
});
afterEach(cleanup);

describe('Memory', () => {
  it('renders the file list when the lint request rejects', async () => {
    vi.mocked(api.fetchMemoryLint).mockRejectedValue(new Error('lint failed: 500'));
    render(<Memory />);
    const nav = await screen.findByRole('navigation', { name: 'Memory files' });
    expect(nav.textContent).toContain('MEMORY.md');
    expect(nav.textContent).toContain('checkout-rollout.md');
    const panel = screen.getByRole('region', { name: 'Stale facts' });
    await waitFor(() => expect(panel.textContent).toContain('lint failed: 500'));
    // The editor still opens the first file.
    await waitFor(() => expect(api.fetchMemoryFile).toHaveBeenCalledWith('shop', INDEX));
  });

  it('clicking a stale fact opens that file in the editor', async () => {
    const report: MemoryLintReport = {
      dir: '/home/dev/.claude/projects/-work-shop/memory',
      project: '/work/shop',
      files: 2,
      claims: 1,
      findings: [
        {
          file: TOPIC,
          lineNo: 2,
          pr: 41,
          claim: 'PR #41 open: flip the guard',
          mergeSha: 'abc',
          mergedAt: new Date(Date.now() - 86_400_000).toISOString(),
        },
      ],
    };
    vi.mocked(api.fetchMemoryLint).mockResolvedValue(report);
    render(<Memory />);
    const row = await screen.findByRole('button', { name: /checkout-rollout\.md:2/ });
    fireEvent.click(row);
    await waitFor(() => expect(api.fetchMemoryFile).toHaveBeenCalledWith('shop', TOPIC));
    const nav = screen.getByRole('navigation', { name: 'Memory files' });
    const active = nav.querySelector('[aria-current="true"]');
    expect(active?.textContent).toContain('checkout-rollout.md');
  });

  // W2 of the flaky-tests plan, reproduced without load. The click lands during
  // the commit that first shows the list, before React has flushed that
  // commit's passive effects — the window a loaded runner hits between findBy*
  // resolving and fireEvent. The probe is a Profiler onRender callback: it runs
  // in the commit's layout phase on every commit of the Memory subtree (a
  // sibling component's layout effect would not re-run on Memory's own state
  // updates).
  it('a stale-fact click in the commit that shows the list is not overwritten by the auto-select', async () => {
    vi.mocked(api.fetchMemoryLint).mockResolvedValue(staleReport());
    // The lint resolves first, the list one macrotask later, so the stale-fact
    // row first appears in the same commit that sets `files`.
    vi.mocked(api.fetchMemoryList).mockImplementation(
      () =>
        new Promise((resolve) => {
          setTimeout(() => resolve({ files: [file(INDEX), file(TOPIC)] }), 0);
        }),
    );
    let clicked = false;
    const probe = (): void => {
      if (clicked) return;
      const row = screen.queryByRole('button', { name: /checkout-rollout\.md:2/ });
      if (row === null) return;
      clicked = true;
      row.click();
    };
    render(
      <Profiler id="memory" onRender={probe}>
        <Memory />
      </Profiler>,
    );
    await waitFor(() => expect(clicked).toBe(true));
    await waitFor(() => expect(api.fetchMemoryFile).toHaveBeenCalledWith('shop', TOPIC));
    // The broken order shows TOPIC for one commit and then INDEX, so read the
    // selection only after every queued render has run.
    await new Promise((resolve) => setTimeout(resolve, 50));
    const nav = screen.getByRole('navigation', { name: 'Memory files' });
    const active = nav.querySelector('[aria-current="true"]');
    expect(active?.textContent, 'the selected file in the list').toContain('checkout-rollout.md');
  });
});

function staleReport(): MemoryLintReport {
  return {
    dir: '/home/dev/.claude/projects/-work-shop/memory',
    project: '/work/shop',
    files: 2,
    claims: 1,
    findings: [
      {
        file: TOPIC,
        lineNo: 2,
        pr: 41,
        claim: 'PR #41 open: flip the guard',
        mergeSha: 'abc',
        mergedAt: new Date(Date.now() - 86_400_000).toISOString(),
      },
    ],
  };
}
