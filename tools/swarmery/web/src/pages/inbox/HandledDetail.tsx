// One thing the triage agent closed on its own, in the detail column of the Inbox's
// "handled by agent" tab: what it was, what the agent did and why, and the undo.
// The design's quiet `ok` has no action behind it, so only the undo is offered.

import { useLingui } from '@lingui/react/macro';
import type { TriageVerdict } from '../../api/triage';
import { DecisionCard } from '../../components/place/DecisionCard';
import { ageLabel, KIND_META, valueWording, type InboxKind } from './inboxModel';

function kindLabel(kind: string): string {
  return kind in KIND_META ? KIND_META[kind as InboxKind].label : kind;
}

/** The agent's call in words; a classifier label is already a word. */
export function verdictWording(v: TriageVerdict): string {
  return v.kind === 'classifier' ? v.value : valueWording(v.value);
}

export function HandledDetail({
  verdict,
  busy,
  error,
  onUndo,
  now = Date.now(),
}: {
  verdict: TriageVerdict;
  busy: boolean;
  error: string | null;
  onUndo: () => void;
  now?: number;
}): JSX.Element {
  const { t } = useLingui();
  const kind = kindLabel(verdict.kind);
  const age = ageLabel(verdict.createdAt, now);
  const value = verdictWording(verdict);
  return (
    <div className="max-w-[900px] px-4 pb-10 desk:px-7">
      {error !== null && (
        <div role="alert" className="py-3 font-mono text-[11px] text-red">
          {error}
        </div>
      )}
      <DecisionCard
        kind={t`handled by agent`}
        kindTone="default"
        meta={t`${kind} · ${age} ago`}
        title={verdict.title}
        body={verdict.reason === '' ? undefined : verdict.reason}
        facts={value}
        primary={{ label: t`undo`, onClick: onUndo, disabled: busy }}
      />
    </div>
  );
}
