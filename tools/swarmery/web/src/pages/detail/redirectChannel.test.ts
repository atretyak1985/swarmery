import { describe, expect, it } from 'vitest';
import type { Session } from '../../api/types';
import { redirectChannel } from './redirectChannel';

type Input = Parameters<typeof redirectChannel>[0];

const session = (patch: Partial<Input>): Input => ({ procState: 'exited', resumeInFlight: false, link: null, ...patch });
const link = (controllable: boolean): Session['link'] => ({ controllable }) as Session['link'];

describe('redirectChannel', () => {
  it('is the composer while the composer accepts text', () => {
    expect(redirectChannel(session({}))).toBe('composer');
    expect(redirectChannel(session({ procState: null, link: link(true) }))).toBe('composer');
  });

  it('is nothing with the composer off and no link channel', () => {
    expect(redirectChannel(session({ procState: 'running' }))).toBeNull();
    expect(redirectChannel(session({ procState: 'orphaned', link: link(false) }))).toBeNull();
    expect(redirectChannel(session({ resumeInFlight: true }))).toBeNull();
  });

  it('is the link mod with the composer off and a mod polling', () => {
    expect(redirectChannel(session({ procState: 'running', link: link(true) }))).toBe('link');
  });
});
