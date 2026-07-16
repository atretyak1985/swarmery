// Header popover: browser-notification preferences (localStorage-backed —
// lib/notifications.ts). The first toggle-on runs the
// Notification.requestPermission() flow; browsers require a user gesture,
// which the click provides. Same hairline visual language as the other
// header controls.

import { useEffect, useRef, useState } from 'react';
import {
  notificationsSupported,
  permissionState,
  requestPermission,
  savePrefs,
  type NotifyPrefs,
} from '../lib/notifications';

const TOGGLES: { key: keyof NotifyPrefs; label: string; hint: string }[] = [
  { key: 'approvalRequested', label: 'approval requested', hint: 'a request needs a decision' },
  { key: 'sessionFinished', label: 'session finished', hint: 'an active session ended' },
  { key: 'sessionError', label: 'session error', hint: 'an error event appeared' },
];

export function NotifySettings({
  prefs,
  onChange,
}: {
  prefs: NotifyPrefs;
  onChange: (next: NotifyPrefs) => void;
}): JSX.Element {
  const [open, setOpen] = useState(false);
  const [denied, setDenied] = useState(permissionState() === 'denied');
  const ref = useRef<HTMLDivElement>(null);

  // Close on outside click.
  useEffect(() => {
    if (!open) return undefined;
    const onDown = (e: MouseEvent): void => {
      if (ref.current !== null && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', onDown);
    return () => document.removeEventListener('mousedown', onDown);
  }, [open]);

  const toggle = (key: keyof NotifyPrefs): void => {
    void (async () => {
      const enabling = !prefs[key];
      if (enabling && !(await requestPermission())) {
        setDenied(permissionState() === 'denied');
        return;
      }
      const next = { ...prefs, [key]: enabling };
      savePrefs(next);
      onChange(next);
    })();
  };

  const anyOn = Object.values(prefs).some(Boolean);

  return (
    <div className="relative" ref={ref}>
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        aria-haspopup="dialog"
        className="rounded-lg border border-line bg-surface px-2.5 py-1 font-mono text-[11px] font-semibold text-ink-2 transition-colors hover:bg-surface2"
      >
        alerts{anyOn ? ' ·' : ''}
      </button>
      {open && (
        <div
          role="dialog"
          aria-label="notification preferences"
          className="absolute right-0 z-30 mt-2 w-[290px] rounded-xl border border-line bg-surface p-3"
        >
          <div className="font-mono text-[10px] tracking-[0.14em] text-ink-faint uppercase">
            browser notifications
          </div>
          {!notificationsSupported() && (
            <div className="mt-2 text-[11.5px] text-ink-dim">
              this browser does not support the Notifications API
            </div>
          )}
          {denied && (
            <div className="mt-2 text-[11.5px] text-red">
              notifications are blocked — allow them in the browser site settings, then re-toggle
            </div>
          )}
          <div className="mt-2 flex flex-col gap-1">
            {TOGGLES.map((tgl) => (
              <label
                key={tgl.key}
                className="flex min-h-9 cursor-pointer items-baseline gap-2 rounded-[7px] px-1.5 py-1 transition-colors hover:bg-surface2"
              >
                <input
                  type="checkbox"
                  checked={prefs[tgl.key]}
                  disabled={!notificationsSupported()}
                  onChange={() => toggle(tgl.key)}
                  className="translate-y-px accent-green focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand"
                />
                <span className="font-mono text-[11.5px] whitespace-nowrap text-ink">
                  {tgl.label}
                </span>
                <span className="min-w-0 flex-1 text-right text-[10.5px] text-ink-faint">
                  {tgl.hint}
                </span>
              </label>
            ))}
          </div>
          <div className="mt-2 text-[10.5px] leading-snug text-ink-faint">
            fires only while this tab is in the background; click a notification to jump to the
            approval or session
          </div>
        </div>
      )}
    </div>
  );
}
