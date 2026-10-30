// The health reading shared by the agent rows and the agent detail. The split-pane
// hub that used to live here moved into the System place (pages/SystemShell.tsx).

import { i18n } from '@lingui/core';
import { msg } from '@lingui/core/macro';
import type { PageTone } from '../../components/place/PageDetail';

/** Failed-run share thresholds: <30% green, <60% amber, else red. */
export function healthTone(failedShare: number): { tone: PageTone; label: string } {
  if (failedShare < 0.3) return { tone: 'green', label: i18n._(msg`healthy`) };
  if (failedShare < 0.6) return { tone: 'amber', label: i18n._(msg`degraded`) };
  return { tone: 'red', label: i18n._(msg`unhealthy`) };
}
