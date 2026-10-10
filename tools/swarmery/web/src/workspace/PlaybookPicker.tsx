// Playbook picker (fusion phase 13): a <select> of the project's playbooks,
// shared by the board's task editors — the create modal (intake) and the task
// detail modal (edit), both pairing it with the description line below. The
// list is fetched once per project (built-ins overlaid by project overrides);
// the empty value means "no explicit choice" — the dispatcher auto-profiles one
// at admission and stamps it back onto the card, so the chip always names the
// recipe that actually ran. Fully keyboard-native (a plain <select>), labelled
// for WCAG.

import { Trans, useLingui } from '@lingui/react/macro';
import { useEffect, useState } from 'react';
import type { Playbook } from '../api/types';
import { fetchPlaybooks } from '../api';
import { ExplainPair } from '../components/Explain';

/** The recipe used when no playbook is selected — its selection maps to "". */
export const DEFAULT_PLAYBOOK = 'standard';

/** Shared fetch: the playbooks visible to a project, or [] on error. */
export function usePlaybooks(projectId: number | null): {
  playbooks: Playbook[];
  loading: boolean;
} {
  const [playbooks, setPlaybooks] = useState<Playbook[]>([]);
  const [loading, setLoading] = useState(true);
  useEffect(() => {
    if (projectId === null) {
      setPlaybooks([]);
      setLoading(false);
      return;
    }
    let disposed = false;
    setLoading(true);
    fetchPlaybooks(projectId)
      .then((ps) => {
        if (!disposed) setPlaybooks(ps);
      })
      .catch(() => {
        if (!disposed) setPlaybooks([]);
      })
      .finally(() => {
        if (!disposed) setLoading(false);
      });
    return () => {
      disposed = true;
    };
  }, [projectId]);
  return { playbooks, loading };
}

/**
 * A <select> bound to a playbook name. `value` is the selected name ("" = the
 * default recipe); `onChange` receives the new name ("" when the default is
 * picked).
 */
export function PlaybookSelect({
  playbooks,
  value,
  onChange,
  disabled = false,
  id,
}: {
  playbooks: Playbook[];
  value: string;
  onChange: (name: string) => void;
  disabled?: boolean;
  id?: string;
}): JSX.Element {
  // "No choice" is offered as the empty option; every other playbook is a named
  // option. If the current value is a name not in the list (e.g. a stored
  // project playbook the fetch has not returned yet) it still renders selected.
  const { t } = useLingui();
  const known = playbooks.some((p) => p.name === value);
  const base =
    'w-full rounded-[8px] border border-line bg-field px-2 py-1.5 font-mono text-[11px] text-ink';
  return (
    <select
      id={id}
      value={value}
      disabled={disabled}
      aria-label={t`playbook`}
      onClick={(e) => e.stopPropagation()}
      onChange={(e) => onChange(e.target.value)}
      className={`${base} outline-none transition-colors hover:border-line-strong focus:border-ink-dim disabled:opacity-50`}
    >
      <option value="">{t`Auto (chosen at dispatch)`}</option>
      {value !== '' && !known && <option value={value}>{value}</option>}
      {playbooks.map((p) => (
        <option key={p.name} value={p.name}>
          {p.name}
          {p.source === 'project' ? ' •' : ''}
        </option>
      ))}
    </select>
  );
}

/** The verify chip + one-line description for the selected playbook (drawer). */
export function PlaybookHint({
  playbooks,
  value,
}: {
  playbooks: Playbook[];
  value: string;
}): JSX.Element | null {
  const name = value === '' ? DEFAULT_PLAYBOOK : value;
  const pb = playbooks.find((p) => p.name === name);
  if (pb === undefined) return null;
  return (
    // The row stays items-start so the description keeps its top alignment
    // against a multi-line wrap; the pair re-centres the 15px trigger on the
    // py-px chip it explains rather than top-aligning the two.
    //
    // shrink-0 / min-w-0 are load-bearing: both of the pair's own children are
    // shrink-0, so when the long description made the row overflow, the pair
    // box was the one that gave — it collapsed to the chip's width and the "?"
    // trigger spilled out on top of the first words of the description. The
    // description is the part that should absorb the pressure, by wrapping.
    <div className="mt-1 flex items-start gap-1.5">
      <span className="shrink-0">
        <ExplainPair id="verify-knob">
          <VerifyChip verify={pb.verify} />
        </ExplainPair>
      </span>
      <span className="min-w-0 font-mono text-[10px] leading-snug text-ink-faint">
        {pb.description}
      </span>
    </div>
  );
}

const VERIFY_CHIP: Record<string, string> = {
  strict: 'border-amber/40 bg-amber/10 text-amber',
  normal: 'border-line text-ink-dim',
  off: 'border-line-strong text-ink-faint',
};

/** Small chip showing a playbook's verify strictness. */
export function VerifyChip({ verify }: { verify: string }): JSX.Element {
  const { t } = useLingui();
  const style = VERIFY_CHIP[verify] ?? 'border-line text-ink-faint';
  return (
    <span
      className={`shrink-0 rounded-full border px-1.5 py-[1px] font-mono text-[9px] uppercase ${style}`}
      data-tip={t`verification: ${verify}`}
    >
      <Trans>verify {verify}</Trans>
    </span>
  );
}
