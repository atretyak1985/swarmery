import { describe, expect, it } from 'vitest';
import type { Recommendation } from '../../api/types';
import { proofVerdict } from './proofVerdict';

const rec = (status: string, note?: string): Pick<Recommendation, 'status' | 'evidence'> =>
  ({ status, evidence: note === undefined ? null : { note } }) as Pick<Recommendation, 'status' | 'evidence'>;

describe('proofVerdict', () => {
  it('keeps gathering while the daemon is still measuring', () => {
    expect(proofVerdict(rec('adopted', 'no measurable improvement yet'))).toBe('gathering');
    expect(proofVerdict(rec('adopted', 'insufficient post-adoption traffic'))).toBe('gathering');
    expect(proofVerdict(rec('accepted'))).toBe('gathering');
  });

  it('says no effect only for a finished measurement', () => {
    expect(proofVerdict(rec('adopted', 'no measurable improvement'))).toBe('none');
  });

  it('a verified rec is proven, whatever an older note said', () => {
    expect(proofVerdict(rec('verified', 'no measurable improvement yet'))).toBe('proven');
  });
});
