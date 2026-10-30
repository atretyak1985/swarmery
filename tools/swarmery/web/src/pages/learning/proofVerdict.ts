// What one Proof row says about the number it was meant to move.

import type { Recommendation } from '../../api/types';

export type ProofVerdict = 'proven' | 'gathering' | 'none';

/** The note a verify pass leaves when nothing moved. */
// i18n-ignore — matched against the daemon's note, never rendered
const NO_EFFECT_NOTE = 'no measurable improvement';
/** …and how it says the measurement is still running: the daemon writes "no
 * measurable improvement yet", keeps the status and verifies again next pass. */
// i18n-ignore — matched against the daemon's note, never rendered
const NOT_FINAL = ' yet';

export function proofVerdict(rec: Pick<Recommendation, 'status' | 'evidence'>): ProofVerdict {
  if (rec.status === 'verified') return 'proven';
  const note = (rec.evidence as { note?: unknown } | null)?.note;
  const final = typeof note === 'string' && note.includes(NO_EFFECT_NOTE) && !note.endsWith(NOT_FINAL);
  return final ? 'none' : 'gathering';
}
