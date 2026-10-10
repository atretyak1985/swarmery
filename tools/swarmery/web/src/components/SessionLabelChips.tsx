import { useLingui } from '@lingui/react/macro';
import { useEffect, useState } from 'react';
import { fetchSessionLabels, type SessionLabel } from '../api/decisions';

/** D2 labels (local classifier, advisory) as small chips; renders nothing
 *  when the session is unlabelled or every label is `unknown`. */
export function SessionLabelChips({ uuid }: { uuid: string }): JSX.Element | null {
  const { t } = useLingui();
  const [labels, setLabels] = useState<SessionLabel | null>(null);

  useEffect(() => {
    let live = true;
    fetchSessionLabels(uuid)
      .then((l) => {
        if (live) setLabels(l);
      })
      .catch(() => {
        if (live) setLabels(null);
      });
    return () => {
      live = false;
    };
  }, [uuid]);

  if (labels === null) return null;
  const chips = [
    ['type', t`type`, labels.taskType],
    ['outcome', t`outcome`, labels.outcome],
    ['cause', t`cause`, labels.failureCause],
  ].filter(([, , v]) => v !== '' && v !== 'unknown' && v !== 'none');
  if (chips.length === 0) return null;
  return (
    <span className="inline-flex flex-wrap gap-1" data-tip={t`labels from the local classifier (advisory)`}>
      {chips.map(([k, name, v]) => (
        <span
          key={k}
          className="rounded border border-line px-1.5 py-px font-mono text-[10px] text-ink-dim"
        >
          {name} {v}
        </span>
      ))}
    </span>
  );
}
