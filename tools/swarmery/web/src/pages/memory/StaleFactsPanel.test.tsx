// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen } from '../../test/render';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { MemoryLintFinding, MemoryLintReport } from '../../api/types';
import { i18n } from '../../i18n';
import { STALE_FACTS_EMPTY, StaleFactsPanel } from './StaleFactsPanel';

function finding(over: Partial<MemoryLintFinding>): MemoryLintFinding {
  return {
    file: '/home/dev/.claude/projects/-work-demo/memory/MEMORY.md',
    lineNo: 5,
    pr: 212,
    claim: 'PR #212 open: settings.local.json as implicit overlay',
    mergeSha: 'a1b2c3d4e5f60718293a4b5c6d7e8f9012345678',
    mergedAt: new Date(Date.now() - 9 * 86_400_000).toISOString(),
    ...over,
  };
}

function report(findings: MemoryLintFinding[]): MemoryLintReport {
  return {
    dir: '/home/dev/.claude/projects/-work-demo/memory',
    project: '/work/demo',
    files: 3,
    claims: 7,
    findings,
  };
}

afterEach(cleanup);

describe('StaleFactsPanel', () => {
  it('renders one row per finding with file:line, the PR and the claim', () => {
    render(
      <StaleFactsPanel
        report={report([
          finding({}),
          finding({
            file: '/home/dev/.claude/projects/-work-demo/memory/opus5-prompting-alignment.md',
            lineNo: 12,
            pr: 224,
            claim: 'PR #224 open 2026-08-11: tech-lead xhigh',
          }),
        ])}
        onOpen={vi.fn()}
      />,
    );
    const rows = screen.getAllByRole('listitem');
    expect(rows).toHaveLength(2);
    expect(rows[0]?.textContent).toContain('MEMORY.md:5');
    expect(rows[0]?.textContent).toContain('PR #212');
    expect(rows[0]?.textContent).toContain('merged 9 d ago');
    expect(rows[0]?.textContent).toContain('settings.local.json as implicit overlay');
    expect(rows[1]?.textContent).toContain('opus5-prompting-alignment.md:12');
    expect(rows[1]?.textContent).toContain('PR #224');
    expect(screen.getByText('2')).toBeTruthy();
  });

  it('renders the empty state when the report has no findings', () => {
    render(<StaleFactsPanel report={report([])} onOpen={vi.fn()} />);
    expect(i18n._(STALE_FACTS_EMPTY)).toBe('No stale facts found');
    expect(screen.getByText('No stale facts found')).toBeTruthy();
    expect(screen.getByText('No stale facts found')).toBeTruthy();
    expect(screen.queryAllByRole('listitem')).toHaveLength(0);
  });

  it('clicking a row calls onOpen with the finding file path', () => {
    const onOpen = vi.fn();
    const file = '/home/dev/.claude/projects/-work-demo/memory/opus5-prompting-alignment.md';
    render(<StaleFactsPanel report={report([finding({ file, lineNo: 3 })])} onOpen={onOpen} />);
    fireEvent.click(screen.getByRole('button', { name: /opus5-prompting-alignment\.md:3/ }));
    expect(onOpen).toHaveBeenCalledTimes(1);
    expect(onOpen).toHaveBeenCalledWith(file);
  });

  it('shows the error inside the panel and keeps the retry', () => {
    const onRetry = vi.fn();
    render(<StaleFactsPanel report={null} error="lint failed: 500" onOpen={vi.fn()} onRetry={onRetry} />);
    expect(screen.getByRole('alert').textContent).toContain('lint failed: 500');
    fireEvent.click(screen.getByRole('button', { name: /retry/i }));
    expect(onRetry).toHaveBeenCalledTimes(1);
  });

  it('shows a loading state while the report is pending', () => {
    render(<StaleFactsPanel report={null} loading onOpen={vi.fn()} />);
    expect(screen.getByRole('status')).toBeTruthy();
  });
});
