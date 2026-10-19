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
//
// A second, button-less hint (mods-link follow-ups, phase 6) flags a criterion
// that names a Claude Code settings file while the phase doc cites no
// config-channel reference: mods-swarmery-link criterion 75 targeted
// `settings.local.json`, which Claude Code never reads for `pluginConfigs`
// (tools/swarmery/docs/claude-cli-config-channels.md).

import { Trans, useLingui } from '@lingui/react/macro';
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

/**
 * A Claude Code settings file or settings channel named in a criterion. Same
 * Unicode-aware edges as LANDING_OR_MANUAL_RE; the lookbehind also refuses a
 * `.` so `foo.settings.json` (some other file) does not count.
 */
const SETTINGS_FILE_RE =
  /(?<![\p{L}\p{N}_.])(settings\.json|settings\.local\.json|\.claude\.json|managed-settings\.json|pluginConfigs|--settings)(?![\p{L}\p{N}_])/iu;

/** The references that say where Claude Code actually reads its settings: the
 * workspace-plans resource and the daemon's own channel probe doc. */
const CONFIG_CHANNEL_CITATIONS = ['config-channels', 'claude-cli-config-channels'] as const;

/** The class marker a criterion label already carries, or null. */
export function criterionClassOf(label: string): CriterionClass | null {
  const m = CLASS_MARKER_RE.exec(label);
  return m !== null ? (m[1] as CriterionClass) : null;
}

/** True when an UNMARKED label reads like a [LAND] or [MANUAL] criterion. */
export function looksLikeLandOrManual(label: string): boolean {
  return criterionClassOf(label) === null && LANDING_OR_MANUAL_RE.test(label);
}

/** True when the label names a settings file and the phase doc cites no
 * config-channel reference. One citation anywhere in the doc silences it. */
export function looksLikeUncitedSettingsCriterion(label: string, docText: string): boolean {
  return SETTINGS_FILE_RE.test(label) && !CONFIG_CHANNEL_CITATIONS.some((ref) => docText.includes(ref));
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
  /** The whole phase doc's markdown — the settings hint looks for a citation. */
  docText: string;
  /** The fresh doc after a successful mark. */
  onMarked: (doc: PlanDoc) => void;
}

const BTN =
  'rounded border px-1.5 py-px font-mono text-[9.5px] transition-colors disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-brand';

/** The landing/manual hint with its two buttons, and the settings-file hint;
 * renders nothing for a ticked or ordinary criterion. */
export function CriterionLint({
  taskId,
  path,
  line,
  text,
  done,
  docText,
  onMarked,
}: CriterionLintProps): JSX.Element | null {
  const { t } = useLingui();
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  if (done) return null;
  const landOrManual = looksLikeLandOrManual(text);
  const uncitedSettings = looksLikeUncitedSettingsCriterion(text, docText);
  if (!landOrManual && !uncitedSettings) return null;

  const mark = (cls: CriterionClass): void => {
    setBusy(true);
    setErr(null);
    markPlanCriterion(taskId, path, line, cls)
      .then(onMarked)
      .catch((e: unknown) => setErr(e instanceof Error ? e.message : String(e)))
      .finally(() => setBusy(false));
  };

  return (
    <>
      {landOrManual && (
        <div
          data-testid="criterion-lint"
          className="mt-0.5 ml-6 flex flex-wrap items-center gap-1.5 font-mono text-[10px] text-ink-faint"
        >
          <span>
            <Trans>looks like a landing/manual criterion — a run cannot close it</Trans>
          </span>
          <button
            type="button"
            disabled={busy}
            onClick={() => mark('LAND')}
            data-tip={t`closed by push / PR / merge — ticked when the change request merges`}
            className={`${BTN} border-brand/40 text-brand hover:bg-brand/10`}
          >
            <Trans>Mark [LAND]</Trans>
          </button>
          <button
            type="button"
            disabled={busy}
            onClick={() => mark('MANUAL')}
            data-tip={t`only a human can close it — you tick it after checking by hand`}
            className={`${BTN} border-amber/40 text-amber hover:bg-amber/10`}
          >
            <Trans>Mark [MANUAL]</Trans>
          </button>
          {err !== null && (
            <span role="alert" className="text-red">
              {err}
            </span>
          )}
        </div>
      )}
      {uncitedSettings && (
        <div data-testid="criterion-lint-settings" className="mt-0.5 ml-6 font-mono text-[10px] text-ink-faint">
          <Trans>Names a settings file — cite where Claude Code reads it (config-channels)</Trans>
        </div>
      )}
    </>
  );
}
