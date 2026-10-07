// @vitest-environment jsdom
//
// Memory page (memory-engineering phase 2). The claims:
//
//   1. The file list renders even when the stale-fact lint request rejects —
//      the lint error stays inside its own panel.
//   2. Clicking a stale-fact row opens that file in the editor.

import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
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
});
