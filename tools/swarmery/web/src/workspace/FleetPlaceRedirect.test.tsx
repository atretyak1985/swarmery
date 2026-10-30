import { cleanup, render, screen } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { loadLastProject } from '../lib/lastProject';
import { FleetPlaceRedirect } from './FleetPlaceRedirect';

vi.mock('../lib/lastProject', () => ({ loadLastProject: vi.fn() }));

function Where(): JSX.Element {
  const { pathname, search } = useLocation();
  return <div data-testid="where">{`${pathname}${search}`}</div>;
}

function renderAt(url: string): string | null {
  render(
    <MemoryRouter initialEntries={[url]}>
      <Routes>
        <Route path="plans/*" element={<FleetPlaceRedirect />} />
        <Route path="knowledge" element={<FleetPlaceRedirect />} />
        <Route path="*" element={<Where />} />
      </Routes>
    </MemoryRouter>,
  );
  return screen.getByTestId('where').textContent;
}

beforeEach(() => vi.mocked(loadLastProject).mockReturnValue('orders-api'));
afterEach(cleanup);

describe('FleetPlaceRedirect', () => {
  it('lands Plans and Knowledge on the last project, keeping the path and query', () => {
    expect(renderAt('/knowledge?tab=serena')).toBe('/p/orders-api/knowledge?tab=serena');
    cleanup();
    expect(renderAt('/plans/p1/phase/3?status=done')).toBe('/p/orders-api/plans/p1/phase/3?status=done');
  });

  it('with no project ever opened, lands on the project list', () => {
    vi.mocked(loadLastProject).mockReturnValue(null);
    expect(renderAt('/knowledge')).toBe('/projects');
    cleanup();
    expect(renderAt('/plans')).toBe('/projects');
  });
});
