// Pure helpers for the lesson effect verdict (honest learning phase 1; Go
// internal/lessons/confound.go). Kept out of the component file so the chip
// module only exports components.
import type { EffectVerdict, Lesson, LessonEffectiveness } from '../../api/lessons';

/** The stored verdict; a row from a daemon older than the verdict migration
 *  (no `verdict` field) reads as insufficient, like the Go default. */
export function effectVerdictOf(e: LessonEffectiveness | null | undefined): EffectVerdict {
  return e?.verdict ?? 'insufficient';
}

export interface ConfoundedShare {
  /** Active lessons with a measured drop (any verdict but insufficient). */
  measured: number;
  confounded: number;
  /** confounded / measured, 0 when nothing is measured. */
  share: number;
}

/** How many of the measured active lessons are confounded. */
export function confoundedShare(lessons: readonly Lesson[]): ConfoundedShare {
  let measured = 0;
  let confounded = 0;
  for (const l of lessons) {
    if (l.status !== 'active') continue;
    const v = effectVerdictOf(l.effectiveness);
    if (v === 'insufficient') continue;
    measured += 1;
    if (v === 'confounded') confounded += 1;
  }
  return { measured, confounded, share: measured === 0 ? 0 : confounded / measured };
}
