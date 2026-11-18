import { describe, expect, it } from 'vitest';
import { classifyTermClose, TERM_CLOSE_MESSAGE, type TermCloseKind } from './closeReason';

describe('classifyTermClose', () => {
  it.each<[string, boolean, number, string, TermCloseKind]>([
    ['the shell exited', true, 1000, 'shell-exited', 'shell-exited'],
    ['the idle reaper closed it', true, 1000, 'idle', 'idle'],
    ['the daemon is stopping', true, 1001, 'daemon-stopping', 'daemon-stopping'],
    ['the terminal cap', true, 1013, 'too many terminals', 'too-many'],
    ['the shell could not start', true, 1011, 'cannot start terminal', 'start-failed'],
    ['the socket never opened', false, 1006, '', 'refused'],
    ['the daemon vanished mid-session', true, 1006, '', 'connection-lost'],
    ['an unknown normal close', true, 1000, '', 'connection-lost'],
  ])('%s → %s', (_label, opened, code, reason, want) => {
    expect(classifyTermClose(opened, code, reason)).toBe(want);
  });

  it('has a message for every kind', () => {
    const kinds: TermCloseKind[] = [
      'shell-exited',
      'idle',
      'daemon-stopping',
      'connection-lost',
      'refused',
      'too-many',
      'start-failed',
    ];
    for (const k of kinds) expect(TERM_CLOSE_MESSAGE[k]).toBeDefined();
  });
});
