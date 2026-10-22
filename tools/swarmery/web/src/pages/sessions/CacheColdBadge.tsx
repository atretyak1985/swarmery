// «Cache cold» chip + «continue from handoff» action (migration 0127,
// internal/cachecold). The daemon flags a live fat session whose prompt cache
// expired while it sat idle: its next message re-writes the whole context at
// the cache-write rate. Continuing from the handoff brief in a fresh session
// costs a fraction of that, so the chip carries the action:
//
//   - a brief exists → open it (the session detail's Handoff section);
//   - none yet → ask the daemon for one (POST /api/sessions/{id}/handoff, the
//     existing candidate rules); the session_updated frame that announces the
//     brief turns the action into the link.
//
// The chip sits inside a clickable session row, so clicks and Enter on it never
// reach the row's navigate handler.

import { Trans, useLingui } from '@lingui/react/macro';
import { type KeyboardEvent, type MouseEvent, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { requestSessionHandoff } from '../../api';
import type { Session } from '../../api/types';
import { fmtTime } from '../../lib/format';
import { useSessionHref } from '../../lib/sessionHref';

/** Where the detail page shows the brief (HandoffCard opens on this hash). */
export const HANDOFF_HASH = '#handoff';

function stop(e: MouseEvent | KeyboardEvent): void {
  e.stopPropagation();
}

export function CacheColdBadge({ session }: { session: Session }): JSX.Element | null {
  const { t } = useLingui();
  const navigate = useNavigate();
  const sessionHref = useSessionHref();
  const [pending, setPending] = useState(false);
  const [generating, setGenerating] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const cold = session.cacheCold;
  if (cold == null) return null;

  const k = Math.round(cold.footprint / 1000);
  const at = fmtTime(cold.since);
  const channel = cold.kind === 'api' ? t`API-key` : t`subscription`;
  const hasBrief = session.handoff != null;
  const openBrief = (): void => {
    navigate(`${sessionHref(session.id)}${HANDOFF_HASH}`);
  };

  const onAction = (e: MouseEvent): void => {
    e.stopPropagation();
    if (hasBrief) {
      openBrief();
      return;
    }
    setPending(true);
    setError(null);
    requestSessionHandoff(session.id)
      .then((r) => {
        if (r.status === 'ready') openBrief();
        else setGenerating(true);
      })
      .catch((err: unknown) => setError(err instanceof Error ? err.message : t`handoff request failed`))
      .finally(() => setPending(false));
  };

  const busy = !hasBrief && (pending || generating);
  const label = busy ? t`writing handoff…` : t`continue from handoff`;

  return (
    <span className="inline-flex shrink-0 items-center gap-1" onClick={stop} onKeyDown={stop}>
      <span
        data-tip={t`The ${channel} prompt cache of this ${k}k-token session expired at ${at}: the next message re-writes the whole context. Continue from the handoff brief in a fresh session instead.`}
        className="rounded-full border border-blue/40 bg-blue/10 px-[7px] py-0.5 font-mono text-[10px] whitespace-nowrap text-blue"
      >
        <Trans>cache cold</Trans>
      </span>
      <button
        type="button"
        onClick={onAction}
        disabled={busy}
        data-tip={
          hasBrief
            ? t`Open the handoff brief and its resume command`
            : t`Ask the daemon to write a handoff brief for this session`
        }
        className="rounded-full border border-blue/40 px-[7px] py-0.5 font-mono text-[10px] whitespace-nowrap text-blue transition-colors hover:bg-blue/15 focus-visible:outline-2 focus-visible:outline-brand disabled:opacity-60"
      >
        {label}
      </button>
      {error !== null && (
        <span role="alert" className="font-mono text-[10px] text-red">
          {error}
        </span>
      )}
    </span>
  );
}
