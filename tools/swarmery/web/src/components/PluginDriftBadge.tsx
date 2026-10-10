// Plugin-drift badge on the daemon health line: enabled plugins the daemon
// found are not actually loadable in some project. Silent at zero — the health
// line is otherwise pure noise — and rendered by every shell that shows health
// (App, WorkspaceShell), so the signal cannot be present on one screen and
// missing on the next.

import { useLingui } from '@lingui/react/macro';
import type { HealthResponse } from '../api/types';

export function PluginDriftBadge({
  health,
}: {
  health: HealthResponse | null;
}): JSX.Element | null {
  const { t } = useLingui();
  const drift = health?.pluginDrift;
  // Absent (older daemon) and zero both render nothing, but they are not the
  // same thing: a daemon that cannot scan reports itself as an error finding,
  // so a blind scanner still shows up here rather than reading as healthy.
  if (drift === undefined || drift.error + drift.warn === 0) return null;
  const { error, warn } = drift;
  return (
    <span
      data-tip={t`${error} error / ${warn} warn plugin findings — see System → Insights`}
      className={error > 0 ? 'text-red' : 'text-amber'}
    >
      {error > 0 ? t`· plugins ⚠ ${error}` : t`· plugins ${warn}`}
    </span>
  );
}
