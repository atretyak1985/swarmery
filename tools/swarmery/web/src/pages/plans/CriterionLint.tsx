// The Criteria tab's lint for criterion classes (phase-run outcomes plan,
// phase 3, D3). A criterion that reads like landing work (push, a PR, a merge)
// or like a hand check (production, a console, "manually") but carries no
// `[LAND]` / `[MANUAL]` marker is one a phase run cannot close — the run
// settles `noop` / `partial` around it. Plans written before the markers
// existed are full of them, so the tab offers the marker in one click.
//
// Non-blocking: a hint under the criterion, never a gate. The button calls
// PATCH /api/epics/{taskId}/docs {line, class}; the daemon writes the marker
// (`- [ ] push` → `- [ ] [LAND] push`) and answers with the fresh doc.

import { useState } from 'react';
import { type CriterionClass, markPlanCriterion } from '../../api';
import type { PlanDoc } from '../../api/types';

/** A class marker at the very start of a criterion's label — the daemon's
 * `criterionClassRe` (internal/wsingest/classes.go). */
const CLASS_MARKER_RE = /^\s*\[(LAND|MANUAL)\]\s+/;

/**
 * The landing / hand-check heuristic. `\b` is ASCII-only in JS, so the word
 * edges are Unicode-aware lookarounds (`u` flag) — the Cyrillic words would never
 * match between `\b`s. Case-insensitive throughout, like the plan's `(?i)`.
 */
const LANDING_OR_MANUAL_RE =
  /(?<![\p{L}\p{N}_])(push|pull request|pr|merge|gh pr|прод|production|console|вручну|manually|по руках)(?![\p{L}\p{N}_])/iu;

/** The class marker a criterion label already carries, or null. */
export function criterionClassOf(label: string): CriterionClass | null {
  const m = CLASS_MARKER_RE.exec(label);
  return m !== null ? (m[1] as CriterionClass) : null;
}

/** True when an UNMARKED label reads like a [LAND] or [MANUAL] criterion. */
export function looksLikeLandOrManual(label: string): boolean {
  return criterionClassOf(label) === null && LANDING_OR_MANUAL_RE.test(label);
}

export interface CriterionLintProps {
  taskId: number;
  /** The phase doc's path relative to plan/ (what the docs endpoint accepts). */
  path: string;
  /** The criterion's 0-based source line — the PATCH address. */
  line: number;
  /** The criterion's label (the text after `- [ ] `). */
  text: string;
  done: boolean;
  /** The fresh doc after a successful mark. */
  onMarked: (doc: PlanDoc) => void;
}

const BTN =
  'rounded border px-1.5 py-px font-mono text-[9.5px] transition-colors disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-brand';

/** The hint and its two buttons; renders nothing for a ticked, already marked
 * or ordinary criterion. */
export function CriterionLint({ taskId, path, line, text, done, onMarked }: CriterionLintProps): JSX.Element | null {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  if (done || !looksLikeLandOrManual(text)) return null;

  const mark = (cls: CriterionClass): void => {
    setBusy(true);
    setErr(null);
    markPlanCriterion(taskId, path, line, cls)
      .then(onMarked)
      .catch((e: unknown) => setErr(e instanceof Error ? e.message : String(e)))
      .finally(() => setBusy(false));
  };

  return (
    <div
      data-testid="criterion-lint"
      className="mt-0.5 ml-6 flex flex-wrap items-center gap-1.5 font-mono text-[10px] text-ink-faint"
    >
      <span>looks like a landing/manual criterion — a run cannot close it</span>
      <button
        type="button"
        disabled={busy}
        onClick={() => mark('LAND')}
        data-tip="closed by push / PR / merge — ticked when the change request merges"
        className={`${BTN} border-brand/40 text-brand hover:bg-brand/10`}
      >
        Mark [LAND]
      </button>
      <button
        type="button"
        disabled={busy}
        onClick={() => mark('MANUAL')}
        data-tip="only a human can close it — you tick it after checking by hand"
        className={`${BTN} border-amber/40 text-amber hover:bg-amber/10`}
      >
        Mark [MANUAL]
      </button>
      {err !== null && (
        <span role="alert" className="text-red">
          {err}
        </span>
      )}
    </div>
  );
}
