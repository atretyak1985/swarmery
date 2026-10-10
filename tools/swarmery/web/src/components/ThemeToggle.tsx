// Quick light/dark theme toggle for the header (both the fleet/session shell
// and the project shell carry it, so the header chrome is identical across
// modes). The full theme controls (mode + palette) still live on /settings; this
// is just the one-click switch.

import { useLingui } from '@lingui/react/macro';
import { useTheme } from '../lib/theme';

export function ThemeToggle(): JSX.Element {
  const { theme, toggle } = useTheme();
  const { t } = useLingui();
  const isLight = theme === 'light';
  return (
    <button
      type="button"
      role="switch"
      aria-checked={isLight}
      aria-label={isLight ? t`switch to dark theme` : t`switch to light theme`}
      onClick={toggle}
      className="flex h-[26px] w-[26px] shrink-0 items-center justify-center rounded-lg border border-line bg-field text-[13px] leading-none text-ink-dim transition-colors hover:border-line-strong hover:text-ink"
    >
      <span aria-hidden="true">{isLight ? '☾' : '☀'}</span>
    </button>
  );
}
