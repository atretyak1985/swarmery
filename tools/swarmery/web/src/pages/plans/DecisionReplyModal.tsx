// The Review tab's in-place answer to an open guard decision (plan D24/D25):
// the same question, facts and options Needs You shows (guardDecisionView.ts),
// in an overlay over the tab instead of a trip to another page. Answers through
// the same endpoint (answerGuardDecision) a guard decision has always used —
// nothing here changes what gets sent, only where the operator can send it from.
//
// The overlay follows ConfirmDialog's contract (components/ui.tsx): focus moves
// onto the card on open and returns to the opener on close, Tab is contained,
// Esc cancels (never while busy), a backdrop click cancels and stops there.

import { useLingui } from '@lingui/react/macro';
import { useEffect, useRef, useState } from 'react';
import { answerGuardDecision } from '../../api/guard';
import type { NeedsYouDecision } from '../../api/types';
import { containTab, FOCUSABLE_SELECTOR } from '../../components/ui';
import { DecisionCard } from '../../components/place/DecisionCard';
import { ALLOW_OPTION, guardDecisionFacts, guardDecisionOptions, guardDecisionTitle, isTwoWayGuardDecision } from './guardDecisionView';

function FactsLine({ facts }: { facts: readonly { key: string; text: string }[] }): JSX.Element | null {
  if (facts.length === 0) return null;
  return (
    <>
      {facts.map((f, i) => (
        <span key={f.key}>
          {i > 0 && ' · '}
          {f.text}
        </span>
      ))}
    </>
  );
}

export interface DecisionReplyModalProps {
  open: boolean;
  decision: NeedsYouDecision;
  onAnswered: () => void;
  onClose: () => void;
}

export function DecisionReplyModal(props: DecisionReplyModalProps): JSX.Element | null {
  return props.open ? <DecisionReplyModalBody {...props} /> : null;
}

function DecisionReplyModalBody({ decision, onAnswered, onClose }: DecisionReplyModalProps): JSX.Element {
  const { t } = useLingui();
  const boxRef = useRef<HTMLDivElement | null>(null);
  const [pick, setPick] = useState<string | null>(null);
  const [note, setNote] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const closeHandler = useRef(onClose);
  const busyRef = useRef(busy);
  useEffect(() => {
    closeHandler.current = onClose;
    busyRef.current = busy;
  });

  useEffect(() => {
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    boxRef.current?.querySelector<HTMLElement>(FOCUSABLE_SELECTOR)?.focus();
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') {
        e.preventDefault();
        e.stopImmediatePropagation();
        if (!busyRef.current) closeHandler.current();
        return;
      }
      if (e.key !== 'Tab') return;
      const box = boxRef.current;
      if (box === null) return;
      e.stopImmediatePropagation();
      containTab(e, box);
    };
    window.addEventListener('keydown', onKey, true);
    return () => {
      window.removeEventListener('keydown', onKey, true);
      if (opener?.isConnected === true) opener.focus();
    };
  }, []);

  const twoWay = isTwoWayGuardDecision(decision);
  const facts = guardDecisionFacts(decision);

  const answer = (option: string): void => {
    setBusy(true);
    setError(null);
    answerGuardDecision(decision.taskId, decision.phaseId, decision.decisionId, option, note.trim())
      .then(() => onAnswered())
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setBusy(false));
  };

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-bg/70 p-4"
      role="dialog"
      aria-modal="true"
      aria-label={guardDecisionTitle(decision)}
      onClick={(e) => {
        e.stopPropagation();
        if (!busy) onClose();
      }}
    >
      <div ref={boxRef} className="w-full max-w-lg" onClick={(e) => e.stopPropagation()}>
        <DecisionCard
          gate
          kind={decision.decisionId}
          kindTone="red"
          title={guardDecisionTitle(decision)}
          {...(facts.length === 0 ? {} : { facts: <FactsLine facts={facts} /> })}
          {...(twoWay
            ? {}
            : {
                options: guardDecisionOptions(decision),
                picked: pick,
                onPick: setPick,
              })}
          note={{ value: note, onChange: setNote, placeholder: t`note — recorded with the answer (optional)` }}
          primary={
            twoWay
              ? { label: t`Allow · add to scope`, onClick: () => answer(ALLOW_OPTION), disabled: busy }
              : { label: t`answer`, onClick: () => pick !== null && answer(pick), disabled: busy || pick === null }
          }
          secondary={
            twoWay
              ? { label: t`Reject`, onClick: () => answer('reject'), disabled: busy }
              : { label: t`cancel`, onClick: onClose, disabled: busy }
          }
          secondaryKey={false}
        />
        {error !== null && (
          <div role="alert" className="mt-2 rounded-lg border border-red/25 bg-red/5 px-2.5 py-2 font-mono text-[11px] text-red">
            {error}
          </div>
        )}
      </div>
    </div>
  );
}
