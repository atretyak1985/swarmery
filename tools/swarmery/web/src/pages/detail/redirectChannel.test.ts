import { describe, expect, it } from 'vitest';
import type { Session } from '../../api/types';
import { hasLiveProcess, redirectChannel } from './redirectChannel';

type Input = Parameters<typeof redirectChannel>[0];

const session = (patch: Partial<Input>): Input => ({ procState: 'exited', resumeInFlight: false, link: null, ...patch });
const link = (controllable: boolean): Session['link'] => ({ controllable }) as Session['link'];

describe('redirectChannel', () => {
  it('is the composer while the composer accepts text', () => {
    expect(redirectChannel(session({}))).toBe('composer');
    expect(redirectChannel(session({ procState: null, link: link(true) }))).toBe('composer');
    // A headless reply in flight: the composer queues what is typed.
    expect(redirectChannel(session({ resumeInFlight: true }))).toBe('composer');
  });

  it('is nothing with the composer off and no link channel', () => {
    expect(redirectChannel(session({ procState: 'running' }))).toBeNull();
    expect(redirectChannel(session({ procState: 'orphaned', link: link(false) }))).toBeNull();
  });

  it('is the link mod with the composer off and a mod polling', () => {
    expect(redirectChannel(session({ procState: 'running', link: link(true) }))).toBe('link');
  });

  // Deliberate: a detached process's terminal is gone and nothing can `fg` it
  // back — the dashboard is the only channel left, so a detached session must
  // route through the composer exactly like idle, never read as "live".
  it('treats detached as composer-reachable, not a live process', () => {
    expect(hasLiveProcess('detached')).toBe(false);
    expect(redirectChannel(session({ procState: 'detached' }))).toBe('composer');
    expect(redirectChannel(session({ procState: 'detached', link: link(true) }))).toBe('composer');
  });
});
