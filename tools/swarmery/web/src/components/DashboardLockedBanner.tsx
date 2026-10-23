// Global "dashboard locked" banner. The daemon takes a write from this page,
// and opens its live and terminal sockets, only with a dashboard session; when
// it refuses one (401 `dashboard_locked`, or a socket refused with no session
// to offer — lib/dashboardSession.ts), this says why and how to unlock: open
// the dashboard through `swarmery open` or the notch menu, which hands the
// page a one-shot unlock ticket.
//
// Mounted under the header in both shells, like AccountReadyBanner. Renders
// nothing until the first refusal; dismissable, and the next refusal shows it
// again. The shown/dismissed state lives in lib/dashboardSession.ts, so it
// survives a switch between the shells.

import { Trans, useLingui } from '@lingui/react/macro';
import { useSyncExternalStore } from 'react';
import { dismissLockedNotice, isLockedNoticeShown, onDashboardLocked } from '../lib/dashboardSession';

export function DashboardLockedBanner(): JSX.Element | null {
  const { t } = useLingui();
  const locked = useSyncExternalStore(onDashboardLocked, isLockedNoticeShown);

  if (!locked) return null;
  return (
    <div
      role="alert"
      className="border-b border-amber/25 bg-amber/5 px-4 py-2.5 desk:px-6"
      data-testid="dashboard-locked-banner"
    >
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 font-mono text-[11px]">
        <span className="font-semibold text-amber">
          <Trans>dashboard locked</Trans>
        </span>
        <span className="text-ink-2">
          <Trans>
            Changes and the terminal need an unlocked dashboard. Run{' '}
            <code className="text-ink">swarmery open</code> in a terminal, or choose{' '}
            <span className="text-ink">Open dashboard</span> in the notch menu.
          </Trans>
        </span>
        <button
          type="button"
          onClick={dismissLockedNotice}
          aria-label={t`Dismiss the dashboard locked notice`}
          className="ml-auto rounded-[6px] border border-line px-2 py-0.5 font-mono text-[10px] text-ink-dim transition-colors hover:bg-surface2 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-brand"
        >
          <Trans>dismiss</Trans>
        </button>
      </div>
    </div>
  );
}
