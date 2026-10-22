// @vitest-environment jsdom
//
// The model-switch badge (verify-not-trust phase 14, SC-15) renders the
// daemon's modelSwitched answer and nothing else: on for a switched session,
// with the models in the tooltip; off for an unswitched one, for a payload from
// an older daemon without the fields, and for a flag without two models.

import { afterEach, describe, expect, it } from 'vitest';
import { cleanup, render, screen } from '../../test/render';
import { ModelSwitchBadge } from './ModelSwitchBadge';

afterEach(cleanup);

describe('ModelSwitchBadge', () => {
  it('shows the model count and lists the models in first-use order', () => {
    render(<ModelSwitchBadge session={{ modelSwitched: true, models: ['claude-fable-5-1', 'claude-opus-5-5'] }} />);
    const badge = screen.getByRole('img', { name: 'switched model mid-session: claude-fable-5-1 → claude-opus-5-5' });
    expect(badge.textContent).toBe('⇄ 2 models');
    expect(badge.getAttribute('data-tip')).toBe('switched model mid-session: claude-fable-5-1 → claude-opus-5-5');
  });

  it.each([
    ['not switched', { modelSwitched: false, models: ['claude-opus-5-5'] }],
    ['older daemon (fields absent)', {}],
    ['flag without two models', { modelSwitched: true, models: ['claude-opus-5-5'] }],
  ])('renders nothing when %s', (_name, session) => {
    render(<ModelSwitchBadge session={session} />);
    expect(screen.queryByTestId('model-switch-badge')).toBeNull();
  });
});
