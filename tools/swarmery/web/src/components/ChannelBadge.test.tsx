// @vitest-environment jsdom
//
// ChannelBadge + its placement on the Approvals PendingCard: "via mod" for a
// mod request, "via shim" for a shim request, nothing when the daemon sends no
// channel. web/tsconfig.json EXCLUDES *.test.tsx.

import { cleanup, render, screen } from '../test/render';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it } from 'vitest';
import type { PermissionRequest } from '../api/types';
import { PendingCard } from '../pages/Approvals';
import { ChannelBadge } from './ChannelBadge';

afterEach(cleanup);

function request(patch: Partial<PermissionRequest>): PermissionRequest {
  return {
    id: 1,
    sessionId: 1,
    toolName: 'Bash',
    requestJson: JSON.stringify({ tool_name: 'Bash', tool_input: { command: 'ls' } }),
    status: 'pending',
    requestedAt: new Date(Date.now() - 5_000).toISOString(),
    resolvedAt: null,
    resolvedVia: null,
    reason: null,
    expiresAt: new Date(Date.now() + 60_000).toISOString(),
    ...patch,
  };
}

function renderCard(patch: Partial<PermissionRequest>): void {
  render(
    <MemoryRouter>
      <PendingCard request={request(patch)} session={null} nowMs={Date.now()} busy={false} onResolve={() => undefined} />
    </MemoryRouter>,
  );
}

describe('ChannelBadge', () => {
  it('labels the mod channel and explains it in an accessible name', () => {
    render(<ChannelBadge channel="mod" />);
    const badge = screen.getByTestId('channel-badge');
    expect(badge.textContent).toBe('via mod');
    expect(badge.getAttribute('data-tip')).toContain('swarmery-link mod');
    expect(badge.getAttribute('aria-label')).toMatch(/^via mod — /);
    expect(badge.tabIndex).toBe(0);
  });

  it('labels the shim channel', () => {
    render(<ChannelBadge channel="shim" />);
    const badge = screen.getByTestId('channel-badge');
    expect(badge.textContent).toBe('via shim');
    expect(badge.getAttribute('data-tip')).toContain('hook process');
  });

  it('renders nothing without a channel', () => {
    render(<ChannelBadge channel={undefined} />);
    expect(screen.queryByTestId('channel-badge')).toBeNull();
  });
});

describe('Approvals PendingCard channel badge', () => {
  it('shows via mod for a mod request', () => {
    renderCard({ channel: 'mod' });
    expect(screen.getByTestId('channel-badge').textContent).toBe('via mod');
  });

  it('shows via shim for a shim request', () => {
    renderCard({ channel: 'shim' });
    expect(screen.getByTestId('channel-badge').textContent).toBe('via shim');
  });

  it('shows no badge for an old daemon row', () => {
    renderCard({});
    expect(screen.queryByTestId('channel-badge')).toBeNull();
  });
});
