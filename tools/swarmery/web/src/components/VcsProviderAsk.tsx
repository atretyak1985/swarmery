// "Which service hosts <host>?" — asked once, by VcsAuthBanner, when the daemon
// found an origin whose host it could not classify (GET /api/projects/{id}/vcs
// → askProvider). The answer goes to PUT /api/projects/{id}/vcs/provider, which
// stores it in the project's .claude/settings.local.json; `onSaved` then
// re-asks the daemon, whose next answer carries the chosen provider's terms.
//
// The two choices are data — a value and its label — rendered by one map, so
// nothing here branches on which provider was picked (SC-11; enforced by
// web/scripts/check-no-provider-branching.sh).

import { useId, useState } from 'react';
import { putProjectVcsProvider } from '../api';
import type { VcsProviderAnswer } from '../api/types';

export interface VcsProviderAskProps {
  projectId: number;
  /** The origin's host; '' reads as "this repository". */
  host: string;
  /** Called after the answer is stored (the banner's reload). */
  onSaved: () => void;
}

const CHOICES: ReadonlyArray<{ readonly value: VcsProviderAnswer; readonly label: string }> = [
  { value: 'github', label: 'GitHub' },
  { value: 'gitlab', label: 'GitLab' },
];

const BTN =
  'rounded-md border border-line px-2 py-0.5 font-mono text-[10.5px] text-ink-2 transition-colors hover:bg-surface2 focus-visible:outline focus-visible:outline-2 focus-visible:outline-ink-dim disabled:cursor-not-allowed disabled:opacity-50';

export function VcsProviderAsk({ projectId, host, onSaved }: VcsProviderAskProps): JSX.Element {
  const [saving, setSaving] = useState<VcsProviderAnswer | null>(null);
  const [error, setError] = useState<string | null>(null);
  const questionId = useId();
  const errorId = useId();

  const answer = async (value: VcsProviderAnswer): Promise<void> => {
    setSaving(value);
    setError(null);
    try {
      await putProjectVcsProvider(projectId, value);
      onSaved();
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(null);
    }
  };

  return (
    <section
      aria-label="Code host"
      data-testid="vcs-provider-ask"
      className="border-b border-line bg-surface px-4 py-2 font-mono text-[11px] text-ink-2 desk:px-6"
    >
      <div
        role="group"
        aria-labelledby={questionId}
        aria-busy={saving !== null}
        aria-describedby={error !== null ? errorId : undefined}
        className="flex flex-wrap items-center gap-x-3 gap-y-1.5"
      >
        <span id={questionId} className="font-semibold">
          Which service hosts {host === '' ? 'this repository' : <code>{host}</code>}?
        </span>
        <span className="ml-auto flex items-center gap-2">
          {CHOICES.map((c) => (
            <button
              key={c.value}
              type="button"
              className={BTN}
              disabled={saving !== null}
              aria-busy={saving === c.value}
              onClick={() => {
                void answer(c.value);
              }}
            >
              {c.label}
            </button>
          ))}
        </span>
      </div>
      {error !== null && (
        <p id={errorId} role="alert" className="mt-1.5 text-red">
          Could not save the answer: {error}
        </p>
      )}
    </section>
  );
}
