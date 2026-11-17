// What the rewriter said about WHERE a change belongs (harness² phase 3): the
// form-ladder rung, the hunk lens, a KEEP_BASE reason, follow-ups in forms the
// one-file rewriter cannot make, and non-blocking apply-gate warnings. Shared by
// the Retro proposal card and the agent hub's proposal list.
import type { MessageDescriptor } from '@lingui/core';
import { msg } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import type { AgentChangeProposal, ProposalForm, ProposalLens } from '../../api/types';

/** The model's KEEP_BASE answer — a terminal 'rejected' row that no human rejected. */
export function isKeptBase(p: Pick<AgentChangeProposal, 'outcome'>): boolean {
  return p.outcome === 'kept_base';
}

const FORM_LABEL: Record<Exclude<ProposalForm, ''>, MessageDescriptor> = {
  skill: msg`skill`,
  script: msg`script`,
  subagent: msg`subagent`,
  hook: msg`hook`,
  'always-on': msg`always-on text`,
  keep_base: msg`keep as is`,
};

const LENS_LABEL: Record<Exclude<ProposalLens, ''>, MessageDescriptor> = {
  procedure: msg`procedure`,
  knowledge: msg`knowledge`,
  mixed: msg`procedure + knowledge`,
};

const LENS_TIP: Record<Exclude<ProposalLens, ''>, MessageDescriptor> = {
  procedure: msg`The change is about how to act: steps, rules, checks.`,
  knowledge: msg`The change is about what is true: facts, paths, conventions.`,
  mixed: msg`Some hunks change how to act, others change what is true.`,
};

const chip = 'rounded-[7px] border px-1.5 py-[2px] font-mono text-[10px] font-medium';

/**
 * The "kept as is" chip: shown in place of the status chip on a KEEP_BASE row,
 * styled apart from a human "rejected" (dashed, brand-toned) so the two never
 * read as the same thing.
 */
export function KeptBaseChip(): JSX.Element {
  const { t } = useLingui();
  return (
    <span
      data-testid="proposal-kept-base"
      data-tip={t`The rewriter found the file already does the job and proposed no change. Nobody rejected it.`}
      className={`${chip} border-dashed border-brand/50 text-brand`}
    >
      <Trans>kept as is</Trans>
    </span>
  );
}

/** Form + lens chips, inline beside a proposal's title. Renders nothing for an older row. */
export function ProposalFormLensChips({ p }: { p: AgentChangeProposal }): JSX.Element | null {
  const { i18n, t } = useLingui();
  const form = p.form ?? '';
  const lens = p.lens ?? '';
  if (form === '' && lens === '') return null;
  return (
    <>
      {form !== '' && form !== 'keep_base' && (
        <span
          data-testid="proposal-form"
          data-tip={t`Where the rewriter put this change on the form ladder (skill, script, subagent, hook, always-on text last).`}
          className={`${chip} border-line-strong text-ink-dim`}
        >
          {i18n._(FORM_LABEL[form])}
        </span>
      )}
      {lens !== '' && (
        <span
          data-testid="proposal-lens"
          data-tip={i18n._(LENS_TIP[lens])}
          className={`${chip} border-line text-ink-faint`}
        >
          {i18n._(LENS_LABEL[lens])}
        </span>
      )}
    </>
  );
}

/** The KEEP_BASE reason, follow-ups and warnings — the detail half of the facets. */
export function ProposalFacets({ p }: { p: AgentChangeProposal }): JSX.Element | null {
  const { i18n, t } = useLingui();
  const followups = p.suggested_followups ?? [];
  const warnings = p.warnings ?? [];
  const kept = isKeptBase(p);
  if (!kept && followups.length === 0 && warnings.length === 0) return null;
  return (
    <div className="mt-2 grid gap-2">
      {kept && (
        <div data-testid="proposal-keep-base-reason">
          <div className="font-mono text-[10px] text-brand">
            <Trans>Why the file stays as it is</Trans>
          </div>
          <p className="mt-0.5 font-mono text-[10.5px] leading-relaxed whitespace-pre-wrap text-ink-3">
            {p.rationale}
          </p>
        </div>
      )}
      {warnings.length > 0 && (
        <ul aria-label={t`warnings`} className="grid gap-0.5">
          {warnings.map((w) => (
            <li key={w} className="font-mono text-[10.5px] leading-relaxed text-amber">
              ⚠ {w}
            </li>
          ))}
        </ul>
      )}
      {followups.length > 0 && (
        <div>
          <div className="font-mono text-[10px] text-ink-faint">
            <Trans>Follow-ups the rewriter could not make itself</Trans>
          </div>
          <ul aria-label={t`follow-ups`} className="mt-0.5 grid gap-0.5">
            {followups.map((f, i) => (
              <li key={`${i}:${f.text}`} className="text-[11px] text-ink-dim">
                {f.form !== '' && (
                  <span className="font-mono text-[10px] text-ink">{i18n._(FORM_LABEL[f.form])}</span>
                )}
                {f.form !== '' && ' — '}
                {f.text}
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}
