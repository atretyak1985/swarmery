// Which model and effort the next per-phase run starts with, and the chips that say
// what a run used and what its doc asks for.

import { msg, plural, t } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import type { EpicPhase } from '../../api/types';

// ── Per-phase model selection ───────────────────────────────────────────────
// The picker's options ARE the closed set the daemon accepts (planning.Models,
// internal/planning/runner.go), so SC-5 — "the UI cannot send a value the API
// would reject" — is structural here rather than a second validation.
//
// `default` is NOT a model. It means SEND NO `model` KEY AT ALL, leaving the
// daemon's own SWARMERY_PHASERUN_MODEL (a hand-pinned plist value that may carry
// a `[1m]` context-window suffix the closed set does not contain) in charge. It
// is first, and it is the fallback, so a phase run started by someone who never
// touched this control behaves exactly as it did before the control existed.
//
// Scope: this is the PER-PHASE control only. The whole-plan run reads
// SWARMERY_PLANRUN_MODEL and is deliberately untouched, so it gets no picker —
// one there would silently do nothing.
//
// Since phase 3, `default` is also what lets a phase DOC's own `**Model:**`
// declaration apply: sending no key leaves the daemon's four-rung ladder in
// charge, and rung 2 of that ladder is the doc. Choosing a real model here is an
// OVERRIDE of every doc on the plan — which is why the label says so, and why
// each phase that declares one shows a `doc:` chip beside its Run button.
/** The concrete model a `default` phase run ends up on when no doc and no env
 * knob says otherwise — `planning.DefaultModel`, the last rung of
 * `phaserun.resolveModel`.
 *
 * Named here rather than left implicit because "default" used to be a lie of
 * omission: it meant NO --model flag, which is not a house default but the
 * ACCOUNT default (Fable, ~2× the Opus price). The rung nobody picked was the
 * most expensive one, and the picker said nothing about it. The daemon now pins
 * this rung, and the label says which model that is. */
const PHASE_RUN_DEFAULT_MODEL_LABEL = 'opus 5.5'; // i18n-ignore — a model name, interpolated into the message below

const PHASE_RUN_MODELS = [
  { value: 'default', label: msg`per-doc, else ${PHASE_RUN_DEFAULT_MODEL_LABEL} (no model sent)` },
  // Labels name the GENERATION each alias resolves to today and what choosing
  // it costs relative to opus 5.5, which is the rung `default` lands on. No
  // entry says "default" any more: two of them did, the picker's own default is
  // the first row, and a second thing calling itself the default is how an
  // operator ends up overriding every doc on the plan by picking what they read
  // as "leave it alone".
  { value: 'opus', label: msg`opus 5.5` },
  { value: 'sonnet', label: msg`sonnet 5 — faster, cheaper` },
  // ~2.5x, not ~2x: fable 5 is $10/$50 per MTok against opus 5.5's $4/$20
  // (tools/swarmery/config/pricing.json). The old figure was measured against
  // opus 5, which cost $5/$25 — opus 5.5 got cheaper and the comparison moved.
  { value: 'fable', label: msg`fable 5.1 — most capable, ~2.5× cost` },
] as const;
export type PhaseRunModel = (typeof PHASE_RUN_MODELS)[number]['value'];
const DEFAULT_PHASE_RUN_MODEL: PhaseRunModel = 'default';
/** Its OWN key — beside the planner's `swarmery.planning.model`, never shared:
 * the two choices are about different runs and different money. */
export const PHASE_RUN_MODEL_KEY = 'swarmery.phaserun.model';

/** The line under the ledger: what a run started now will use. */
export function phaseRunModelLine(value: PhaseRunModel): string {
  return value === 'default' ? t`per phase doc, else ${PHASE_RUN_DEFAULT_MODEL_LABEL}` : value;
}

// ── Per-phase effort selection ──────────────────────────────────────────────
// The second half of "what does this run cost": which brain, and how hard it
// thinks. Its options ARE the CLI's closed set (claudeflags.ValidEfforts), so —
// like the model picker — the UI cannot send a value the API would reject.
//
// `default` means SEND NO `effort` KEY, which hands the decision to the same
// ladder the model takes: the phase DOC's own `**Effort:**` header first, then
// SWARMERY_PHASERUN_EFFORT, then the engine's pinned default.
//
// What it does NOT mean is "cheap". A `claude -p` with no --effort runs at the
// CLI's own default, xhigh — the DEEPEST setting — so before the daemon pinned
// this rung, every un-picked phase run paid maximum reasoning tokens for up to
// four hours. The labels below say which end is which for that reason.
const PHASE_RUN_EFFORTS = [
  { value: 'default', label: msg`per-doc, else high (no effort sent)` },
  { value: 'low', label: msg`low — mechanical work` },
  { value: 'medium', label: msg`medium — scoped work` },
  { value: 'high', label: msg`high — implementation` },
  { value: 'xhigh', label: msg`xhigh — deepest, slowest` },
  { value: 'max', label: msg`max — no ceiling` },
] as const;
export type PhaseRunEffort = (typeof PHASE_RUN_EFFORTS)[number]['value'];
const DEFAULT_PHASE_RUN_EFFORT: PhaseRunEffort = 'default';
/** Its own key, beside the model's — the two are different decisions about the
 * same money and must not share storage. */
export const PHASE_RUN_EFFORT_KEY = 'swarmery.phaserun.effort';

/** The effort half of that line. */
export function phaseRunEffortLine(value: PhaseRunEffort): string {
  return value === 'default' ? t`per phase doc, else high` : value;
}

function isPhaseRunEffort(v: string | null): v is PhaseRunEffort {
  return PHASE_RUN_EFFORTS.some((e) => e.value === v);
}

/** Last-used choice; falls back to the default when storage is unavailable
 * (Safari private mode throws on access, not just on write). */
export function readStoredPhaseRunEffort(): PhaseRunEffort {
  try {
    const v = localStorage.getItem(PHASE_RUN_EFFORT_KEY);
    return isPhaseRunEffort(v) ? v : DEFAULT_PHASE_RUN_EFFORT;
  } catch {
    return DEFAULT_PHASE_RUN_EFFORT;
  }
}

function isPhaseRunModel(v: string | null): v is PhaseRunModel {
  return PHASE_RUN_MODELS.some((m) => m.value === v);
}

/** Last-used choice; falls back to the default when storage is unavailable
 * (Safari private mode throws on access, not just on write). */
export function readStoredPhaseRunModel(): PhaseRunModel {
  try {
    const v = localStorage.getItem(PHASE_RUN_MODEL_KEY);
    return isPhaseRunModel(v) ? v : DEFAULT_PHASE_RUN_MODEL;
  } catch {
    return DEFAULT_PHASE_RUN_MODEL;
  }
}

/** The short name behind a full model ID (`claude-opus-5-5` → `opus`). The `[1m]`
 * suffix on the operator's pinned env value is stripped for the LABEL only — the
 * full string stays in the tooltip, because that suffix is the whole difference
 * between a 200k run and a 1M one. An ID we do not know is shown verbatim. */
function phaseModelShortName(id: string): string {
  const base = id.replace(/\[[^\]]*\]$/, '');
  return MODEL_SHORT_NAMES[base] ?? id;
}
const MODEL_SHORT_NAMES: Record<string, string> = {
  'claude-opus-5-5': 'opus',
  // Kept past the 5.5 cutover: runs recorded before it still carry this id in
  // sessions.model, and a historical row must keep rendering as "opus".
  'claude-opus-5': 'opus',
  'claude-sonnet-5': 'sonnet',
  'claude-fable-5-1': 'fable',
};

/** Which model a finished run ACTUALLY used — read from the session the run
 * produced (`sessions.model`), never from what the UI asked for, so a run that
 * fell back to the daemon's default cannot be mislabelled as the operator's
 * choice. Renders nothing while the run is live: a label about a process still
 * in flight would be a claim, not a record. */
export function RunModelChip({ phase }: { phase: EpicPhase }): JSX.Element | null {
  const { t } = useLingui();
  if (phase.runModel === null || phase.runState === 'running') return null;
  // A run that FELL BACK gets its own chip instead of the plain one. `runModel`
  // is the session's first model, which for these runs is a true statement about
  // the first turn and a false one about the output: an Opus 5.5 safeguard
  // refusal moves the session onto an older model and the work continues there.
  // The tooltip lists every model with its turn count, because "42 turns on opus
  // 5.5, 3 on opus 4.1" says how much of the run the fallback actually touched.
  const lastUse = phase.runModels[phase.runModels.length - 1];
  if (phase.runModelFellBack && phase.runModels.length > 1 && lastUse !== undefined) {
    const first = phaseModelShortName(phase.runModel);
    const last = phaseModelShortName(lastUse.model);
    const trail = phase.runModels
      .map((use) => {
        const { model, turns } = use;
        return plural(turns, {
          one: `${model} (# turn)`,
          few: `${model} (# turns)`,
          many: `${model} (# turns)`,
          other: `${model} (# turns)`,
        });
      })
      .join(' → ');
    return (
      <span
        className="rounded border border-amber/40 bg-amber/10 px-1.5 py-px font-mono text-[9.5px] text-amber"
        data-tip={t`this run changed model mid-flight: ${trail}`}
      >
        <Trans>
          {first} → fell back to {last}
        </Trans>
      </span>
    );
  }
  const runModel = phase.runModel;
  return (
    <span
      className="rounded border border-line px-1.5 py-px font-mono text-[9.5px] text-ink-dim"
      data-tip={t`the last run used ${runModel} (from its session)`}
    >
      {phaseModelShortName(phase.runModel)}
    </span>
  );
}

/** The models the daemon actually knows (`planning.Models`), used to tell a
 * declaration it will accept from one it will refuse. Short names and full IDs
 * both resolve server-side, so both count as known here. */
const KNOWN_PHASE_MODELS = new Set<string>([
  ...PHASE_RUN_MODELS.filter((m) => m.value !== 'default').map((m) => m.value),
  ...Object.keys(MODEL_SHORT_NAMES),
]);

/** What the phase DOC asks for (`**Model:** opus` in its header) — rung 2 of the
 * daemon's ladder, and a different fact from `RunModelChip`'s: that one reports
 * what a past run USED, this one what the next run WILL use.
 *
 * Rendered per phase because the declaration is per phase, while the picker above
 * the column is one control for the whole plan (phase 2 made it plan-wide so the
 * row's Run, the detail panel's Retry and the diagnosis modal's Retry could not
 * disagree). Splitting it into N selects would reintroduce exactly that.
 *
 * Three states, and the distinction is the point — silently showing a declaration
 * that is not in effect is the bug this whole rung exists to avoid:
 *   - picker on `default`, value known  → IN EFFECT, normal weight;
 *   - picker overridden                 → struck through, the override named;
 *   - value the daemon does not know    → red, because Run will 409 on it. */
export function DocModelChip({
  phase,
  picked,
}: {
  phase: EpicPhase;
  picked: PhaseRunModel;
}): JSX.Element | null {
  const { t } = useLingui();
  if (phase.docModel === null) return null;
  const docModel = phase.docModel;
  const shortName = phaseModelShortName(docModel);
  const known = KNOWN_PHASE_MODELS.has(docModel);
  const overridden = picked !== 'default';
  const tip = !known
    ? t`the phase doc declares **Model:** ${docModel}, which is not a model this daemon knows (opus, sonnet, fable) — a run will be refused until the line is fixed`
    : overridden
      ? t`the phase doc asks for ${docModel}, but the picker above is set to ${picked} and overrides it for this run`
      : t`the phase doc asks for ${docModel} — this run will use it`;
  const cls = !known
    ? 'border-red/40 text-red'
    : overridden
      ? 'border-line text-ink-faint line-through'
      : 'border-line text-ink-dim';
  return (
    <span
      className={`rounded border px-1.5 py-px font-mono text-[9.5px] ${cls}`}
      data-tip={tip}
    >
      <Trans>doc: {shortName}</Trans>
    </span>
  );
}

/** The per-phase run model picker. Sits with the Run/Retry controls it governs
 * — the choice is made where the money is spent, not in a settings page. */
export function PhaseRunModelPicker({
  value,
  onChange,
  disabled,
}: {
  value: PhaseRunModel;
  onChange: (m: PhaseRunModel) => void;
  disabled: boolean;
}): JSX.Element {
  const { t, i18n } = useLingui();
  return (
    <label className="flex min-w-0 max-w-full flex-wrap items-center justify-end gap-1.5 font-mono text-[10px] text-ink-faint">
      <Trans>phase run model</Trans>
      <select
        value={value}
        disabled={disabled}
        onChange={(e) => {
          if (isPhaseRunModel(e.target.value)) onChange(e.target.value);
        }}
        aria-label={t`phase run model`}
        title={t`the model every Run phase / Retry run on this plan starts with. Leave it on the default to let each phase doc's own **Model:** line decide (and the daemon's knob where a doc declares none); choosing one here overrides every doc on the plan. The whole-plan run is not affected.`}
        className="min-w-0 max-w-full rounded-lg border border-line bg-field px-2 py-1 font-mono text-[10px] text-ink-dim outline-none transition-colors hover:text-ink focus:border-brand/50 disabled:opacity-50"
      >
        {PHASE_RUN_MODELS.map((m) => (
          <option key={m.value} value={m.value}>
            {i18n._(m.label)}
          </option>
        ))}
      </select>
    </label>
  );
}

/** The per-phase effort picker. Sits beside the model picker because the two
 * answer one question together — which brain, and how hard it thinks — and
 * neither is legible about cost without the other. */
export function PhaseRunEffortPicker({
  value,
  onChange,
  disabled,
}: {
  value: PhaseRunEffort;
  onChange: (e: PhaseRunEffort) => void;
  disabled: boolean;
}): JSX.Element {
  const { t, i18n } = useLingui();
  return (
    <label className="flex min-w-0 max-w-full flex-wrap items-center justify-end gap-1.5 font-mono text-[10px] text-ink-faint">
      <Trans>effort</Trans>
      <select
        value={value}
        disabled={disabled}
        onChange={(e) => {
          if (isPhaseRunEffort(e.target.value)) onChange(e.target.value);
        }}
        aria-label={t`phase run effort`}
        title={t`how hard every Run phase / Retry run on this plan thinks. Leave it on the default to let each phase doc's own **Effort:** line decide (and the daemon's knob where a doc declares none); choosing one here overrides every doc on the plan. Note that NO effort is not the cheap end — an unpinned claude run thinks at xhigh, the deepest setting. The whole-plan run is not affected.`}
        className="min-w-0 max-w-full rounded-lg border border-line bg-field px-2 py-1 font-mono text-[10px] text-ink-dim outline-none transition-colors hover:text-ink focus:border-brand/50 disabled:opacity-50"
      >
        {PHASE_RUN_EFFORTS.map((e) => (
          <option key={e.value} value={e.value}>
            {i18n._(e.label)}
          </option>
        ))}
      </select>
    </label>
  );
}
