// Unit tests for the /docs rail grouping rule. Pure logic, no DOM.

import { describe, expect, it } from 'vitest';
import { GROUP_LABEL, GROUP_ORDER, groupOf } from './docsRail';

describe('groupOf', () => {
  it('puts guide- files under Guides', () => {
    expect(groupOf('guide-getting-started.md')).toBe('Guides');
    expect(groupOf('guide-plans.md')).toBe('Guides');
  });

  it('puts plain docs under Reference, not Guides', () => {
    expect(groupOf('ONBOARDING.md')).toBe('Reference');
    expect(groupOf('concepts.md')).toBe('Reference');
    expect(groupOf('retro.md')).toBe('Reference');
  });

  it('does not treat a doc merely containing "guide" as a guide', () => {
    expect(groupOf('styleguide.md')).toBe('Reference');
  });

  it('routes wire docs to Formats and Protocols', () => {
    expect(groupOf('jsonl-format.md')).toBe('Formats');
    expect(groupOf('system-config-format.md')).toBe('Formats');
    expect(groupOf('api-project-config.md')).toBe('Formats');
    expect(groupOf('ws-protocol.md')).toBe('Protocols');
    expect(groupOf('hooks-protocol.md')).toBe('Protocols');
  });

  it('orders the rail Guides → Reference → Formats → Protocols', () => {
    expect(GROUP_ORDER).toEqual(['Guides', 'Reference', 'Formats', 'Protocols']);
  });

  it('labels every group with a translatable message', () => {
    expect(Object.keys(GROUP_LABEL)).toEqual([...GROUP_ORDER]);
  });
});
