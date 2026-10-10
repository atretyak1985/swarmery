// Sidebar-bottom daemon health line (Redesign): "● daemon healthy · v0.2.0+41157a8"
// from GET /api/health, polled every 60s; red + "daemon unreachable" when the
// fetch fails.

import { Trans } from '@lingui/react/macro';
import { useEffect, useState } from 'react';
import type { HealthResponse } from '../api/types';
import { fetchHealth } from '../api';
import { versionLabel, versionTitle } from '../lib/health';
import { PluginDriftBadge } from './PluginDriftBadge';

const POLL_MS = 60_000;

export function HealthFooter(): JSX.Element | null {
  const [health, setHealth] = useState<HealthResponse | null>(null);
  const [unreachable, setUnreachable] = useState(false);

  useEffect(() => {
    let disposed = false;
    const poll = (): void => {
      fetchHealth()
        .then((h) => {
          if (disposed) return;
          setHealth(h);
          setUnreachable(false);
        })
        .catch(() => {
          if (!disposed) setUnreachable(true);
        });
    };
    poll();
    const timer = setInterval(poll, POLL_MS);
    return () => {
      disposed = true;
      clearInterval(timer);
    };
  }, []);

  if (health === null && !unreachable) return null;

  return (
    <div className="mt-auto flex items-center gap-1.5 px-3 py-2 font-mono text-[10.5px] text-ink-dim">
      <span
        className={`h-1.5 w-1.5 shrink-0 rounded-full ${unreachable ? 'bg-red' : 'bg-green'}`}
        aria-hidden="true"
      />
      {unreachable ? (
        <Trans>daemon unreachable</Trans>
      ) : (
        <>
          <Trans>daemon healthy</Trans>
          {health !== null && <span title={versionTitle(health)}> · {versionLabel(health)}</span>}
        </>
      )}
      <PluginDriftBadge health={health} />
    </div>
  );
}
