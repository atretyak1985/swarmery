// @vitest-environment jsdom
//
// The old System routes land on the one place, keeping the row and `?tab=`.
//   /agents(/:id)                       → /system/agents(/:id)
//   /system-hub(/:category(/:id))       → /system/:category(/:id)
// and the same under /p/:slug. An unknown category lands on Skills, as it did.
//
// Runs with the rest of the web suite: `npm test`.

import { cleanup, render, screen } from '../../test/render';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { afterEach, describe, expect, it } from 'vitest';
import { AgentHub } from '../AgentHub';
import { SystemHub } from '../SystemHub';

function Where(): JSX.Element {
  const { pathname, search } = useLocation();
  return <div data-testid="where">{`${pathname}${search}`}</div>;
}

function landsOn(url: string): string | null {
  render(
    <MemoryRouter initialEntries={[url]}>
      <Routes>
        <Route path="/agents" element={<AgentHub />} />
        <Route path="/agents/:id" element={<AgentHub />} />
        <Route path="/system-hub" element={<SystemHub />} />
        <Route path="/system-hub/:category" element={<SystemHub />} />
        <Route path="/system-hub/:category/:id" element={<SystemHub />} />
        <Route path="/p/:slug/agents/:id" element={<AgentHub />} />
        <Route path="/p/:slug/system-hub/:category/:id" element={<SystemHub />} />
        <Route path="*" element={<Where />} />
      </Routes>
    </MemoryRouter>,
  );
  return screen.getByTestId('where').textContent;
}

afterEach(cleanup);

describe('old System routes', () => {
  it.each([
    ['/agents', '/system/agents'],
    ['/agents/5?tab=runs', '/system/agents/5?tab=runs'],
    ['/system-hub', '/system/skills'],
    ['/system-hub/hooks', '/system/hooks'],
    ['/system-hub/insights', '/system/insights'],
    ['/system-hub/skills/9?tab=definition', '/system/skills/9?tab=definition'],
    ['/system-hub/templates/adr%20template', '/system/templates/adr%20template'],
    ['/system-hub/nonsense', '/system/skills'],
    ['/p/shop/agents/5?tab=docs', '/p/shop/system/agents/5?tab=docs'],
    ['/p/shop/system-hub/commands/3', '/p/shop/system/commands/3'],
  ])('%s lands on %s', (from, to) => {
    expect(landsOn(from)).toBe(to);
  });
});
