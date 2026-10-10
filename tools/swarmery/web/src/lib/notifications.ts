// Browser notification preferences + dispatch (control-plane v2). Prefs live
// in localStorage; notifications fire ONLY while the tab is hidden (a visible
// dashboard already shows the change live) and only after the user granted
// the Web Notifications permission from the header popover. Clicking a
// notification focuses the tab and navigates to the approval / session.

import { t } from '@lingui/core/macro';
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

function fire(title: string, body: string, tag: string, onClick: () => void): Notification | null {
  if (!notificationsSupported() || Notification.permission !== 'granted') return null;
  if (!document.hidden) return null; // never notify a visible tab
  const n = new Notification(title, { body, tag });
  n.onclick = () => {
    window.focus();
    onClick();
    n.close();
  };
  return n;
}

// Open "Approval needed" notifications by tag, module-wide — closed early when
// the matching permission_resolved frame arrives (a rule racing the broadcast,
// a terminal/mobile resolve) so a stale toast never outlives its request.
const openApprovalNotifications = new Map<string, Notification>();

/**
 * Subscribe to the shared WS (one socket app-wide — lib/ws.ts) and fire
 * browser notifications per prefs:
 *  - permission_requested                       → "Approval needed" → /approvals
 *  - session_updated, active → completed|killed → "Session finished" → /sessions/:id
 *  - event_appended, event.status === 'error'   → "Session error"   → /sessions/:id
 *    (status, not type: matches the webhook's session_error semantics —
 *    failed tool calls count, not just api_error records)
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
        // The daemon publishes permission_requested BEFORE rule evaluation and
        // the WS frame hydrates from the DB at broadcast time, so a
        // rule-auto-approved request usually arrives already 'approved' —
        // nothing is waiting on the user, so no notification.
        if (p.approvalRequested && msg.payload.status === 'pending') {
          const tag = `approval-${String(msg.payload.id)}`;
          const { toolName, id } = msg.payload;
          const n = fire(
            t`Approval needed: ${toolName}`,
            t`request #${id} is waiting on you`,
            tag,
            () => navigate('/approvals'),
          );
          if (n !== null) {
            openApprovalNotifications.set(tag, n);
            n.onclose = () => openApprovalNotifications.delete(tag);
          }
        }
        return;
      }
      if (msg.type === 'permission_resolved') {
        // Belt-and-braces for the hydration race above: if this request's
        // notification is still up, retract it — it is no longer actionable.
        const tag = `approval-${String(msg.payload.id)}`;
        const open = openApprovalNotifications.get(tag);
        if (open !== undefined) {
          openApprovalNotifications.delete(tag);
          open.close();
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
            s.status === 'killed' ? t`Session killed` : t`Session finished`,
            `${s.projectName ?? s.projectSlug}${s.title !== null ? ` — ${s.title}` : ''}`,
            `session-${String(s.id)}`,
            () => navigate(`/sessions/${String(s.id)}`),
          );
        }
        return;
      }
      if (msg.type === 'event_appended' && msg.payload.event.status === 'error' && p.sessionError) {
        const sessionId = msg.payload.sessionId;
        fire(
          t`Session error`,
          t`an error event in session #${sessionId}`,
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
