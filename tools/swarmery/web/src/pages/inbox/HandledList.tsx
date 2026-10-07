// The "handled by agent" tab: what the triage agent applied on its own in the
// last 7 days, newest first, each with an undo.

import { useState } from 'react';
import { undoTriageVerdict, type TriageVerdict } from '../../api/triage';
import { ageLabel, KIND_META, valueWording, type InboxKind } from './inboxModel';

function inboxKind(kind: string): InboxKind | null {
  return kind in KIND_META ? (kind as InboxKind) : null;
}

export function HandledList({
  verdicts,
  onChanged,
  now = Date.now(),
}: {
  verdicts: readonly TriageVerdict[];
  /** After a successful undo: reload everything the undone item touches (the lists AND the Inbox). */
  onChanged: () => void;
  now?: number;
}): JSX.Element {
  const [pending, setPending] = useState<number | null>(null);
  const [error, setError] = useState<string | null>(null);

  const undo = (v: TriageVerdict): void => {
    if (pending !== null) return;
    setPending(v.id);
    setError(null);
    undoTriageVerdict(v.id)
      .then(() => onChanged())
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setPending(null));
  };

  const rows = [...verdicts].sort((a, b) => b.createdAt.localeCompare(a.createdAt));

  return (
    <div className="h-full overflow-y-auto py-4">
      {error !== null && (
        <div role="alert" className="mb-3 flex items-center gap-3 font-mono text-[11px] text-red">
          <span>{error}</span>
          <button
            type="button"
            className="rounded-[6px] border border-line-strong px-2 py-[2px] text-ink-3 hover:text-ink"
            onClick={() => setError(null)}
          >
            dismiss
          </button>
        </div>
      )}
      {rows.length === 0 ? (
        <p className="py-6 text-[13px] text-ink-dim">The agent has not closed anything in the last 7 days.</p>
      ) : (
        <ul aria-label="closed by the agent" className="m-0 list-none divide-y divide-line p-0">
          {rows.map((v) => {
            const kind = inboxKind(v.kind);
            const meta = kind === null ? null : KIND_META[kind];
            const value = v.kind === 'classifier' ? v.value : valueWording(v.value);
            return (
              <li key={v.id} className="flex items-start gap-3 py-3">
                <span className={`mt-[5px] h-[7px] w-[7px] shrink-0 rounded-full ${meta?.dot ?? 'bg-ink-faint'}`} />
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-baseline gap-x-2 font-mono text-[10px] tracking-[0.12em] uppercase">
                    <span className={meta?.text ?? 'text-ink-faint'}>{meta?.label ?? v.kind}</span>
                    <span className="tracking-normal text-ink-faint normal-case">{ageLabel(v.createdAt, now)} ago</span>
                  </div>
                  <div className="mt-0.5 text-[12.5px] font-medium text-ink-2">{v.title}</div>
                  <div className="mt-0.5 text-[12px] text-ink-3">
                    <span className="font-medium">{value}</span>
                    {v.reason !== '' && <> · {v.reason}</>}
                  </div>
                </div>
                <button
                  type="button"
                  disabled={pending !== null}
                  aria-label={`undo ${v.title}`}
                  onClick={() => undo(v)}
                  className="shrink-0 rounded-[8px] border border-line-strong px-3 py-[5px] font-mono text-[11px] text-ink-3 hover:text-ink disabled:opacity-50"
                >
                  undo
                </button>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
