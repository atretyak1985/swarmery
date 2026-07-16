// Browser notification preferences + dispatch (control-plane v2). Prefs live
// in localStorage; notifications fire ONLY while the tab is hidden (a visible
// dashboard already shows the change live) and only after the user granted
// the Web Notifications permission from the header popover. Clicking a
// notification focuses the tab and navigates to the approval / session.

import { useCallback, useRef } from 'react';
import { useNavigate } from 'react-router-dom';
import type { SessionStatus, WSMessage } from '../api/types';
import { useLiveUpdates } from './ws';

export interface NotifyPrefs {
  approvalRequested: boolean;
  sessionFinished: boolean;
  sessionError: boolean;
}

export const DEFAULT_PREFS: NotifyPrefs = {
  approvalRequested: false,
  sessionFinished: false,
  sessionError: false,
};

const PREFS_KEY = 'swarmery.notify-prefs';

export function loadPrefs(): NotifyPrefs {
  try {
    const raw = localStorage.getItem(PREFS_KEY);
    if (raw === null) return DEFAULT_PREFS;
    return { ...DEFAULT_PREFS, ...(JSON.parse(raw) as Partial<NotifyPrefs>) };
  } catch {
    return DEFAULT_PREFS;
  }
}

export function savePrefs(prefs: NotifyPrefs): void {
  try {
    localStorage.setItem(PREFS_KEY, JSON.stringify(prefs));
  } catch {
    // storage blocked/full — prefs stay in-memory for this tab
  }
}

export function notificationsSupported(): boolean {
  return 'Notification' in window;
}

/** Current permission, 'unsupported' when the API is absent. */
export function permissionState(): NotificationPermission | 'unsupported' {
  return notificationsSupported() ? Notification.permission : 'unsupported';
}

/** Ask for permission — must be called from a user gesture (the toggle). */
export async function requestPermission(): Promise<boolean> {
  if (!notificationsSupported()) return false;
  if (Notification.permission === 'granted') return true;
  return (await Notification.requestPermission()) === 'granted';
}

function fire(title: string, body: string, tag: string, onClick: () => void): void {
  if (!notificationsSupported() || Notification.permission !== 'granted') return;
  if (!document.hidden) return; // never notify a visible tab
  const n = new Notification(title, { body, tag });
  n.onclick = () => {
    window.focus();
    onClick();
    n.close();
  };
}

/**
 * Subscribe to the shared WS (one socket app-wide — lib/ws.ts) and fire
 * browser notifications per prefs:
 *  - permission_requested                       → "Approval needed" → /approvals
 *  - session_updated, active → completed|killed → "Session finished" → /sessions/:id
 *  - event_appended with event.type === 'error' → "Session error"   → /sessions/:id
 */
export function useBrowserNotifications(prefs: NotifyPrefs): void {
  const navigate = useNavigate();
  const prefsRef = useRef(prefs);
  prefsRef.current = prefs;
  // Last-seen status per session id — transition detection: notify only on
  // active → completed/killed, never when a stale idle session ages out.
  const statusesRef = useRef(new Map<number, SessionStatus>());

  const onMessage = useCallback(
    (msg: WSMessage): void => {
      const p = prefsRef.current;
      if (msg.type === 'permission_requested') {
        if (p.approvalRequested) {
          fire(
            `Approval needed: ${msg.payload.toolName}`,
            `request #${String(msg.payload.id)} is waiting on you`,
            `approval-${String(msg.payload.id)}`,
            () => navigate('/approvals'),
          );
        }
        return;
      }
      if (msg.type === 'session_started' || msg.type === 'session_updated') {
        const s = msg.payload;
        if (!s.id) return; // defensive — malformed frame
        const prev = statusesRef.current.get(s.id);
        statusesRef.current.set(s.id, s.status);
        const finished = s.status === 'completed' || s.status === 'killed';
        if (msg.type === 'session_updated' && prev === 'active' && finished && p.sessionFinished) {
          fire(
            s.status === 'killed' ? 'Session killed' : 'Session finished',
            `${s.projectName ?? s.projectSlug}${s.title !== null ? ` — ${s.title}` : ''}`,
            `session-${String(s.id)}`,
            () => navigate(`/sessions/${String(s.id)}`),
          );
        }
        return;
      }
      if (msg.type === 'event_appended' && msg.payload.event.type === 'error' && p.sessionError) {
        fire(
          'Session error',
          `an error event in session #${String(msg.payload.sessionId)}`,
          `session-error-${String(msg.payload.sessionId)}`,
          () => navigate(`/sessions/${String(msg.payload.sessionId)}`),
        );
      }
    },
    [navigate],
  );
  useLiveUpdates(
    onMessage,
    useCallback(() => {
      // Reconnect: nothing to refetch — missed notifications are not replayed
      // by design (the badge/REST resync already reflects reality).
    }, []),
  );
}
