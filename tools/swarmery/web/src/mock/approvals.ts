// Mock permission_requests store for VITE_MOCK=1 (phase 2 — approvals).
// The store is MUTABLE so approve/deny transitions a request from pending to
// history locally, exactly like the daemon would. The mock WS scenario:
//   - two pending fixtures of different ages on load (one close to expiry),
//   - a new permission_requested pushed ~3 s after load,
//   - an expiry sweep that resolves overdue pendings as `expired` live,
//   - permission_resolved emitted for every transition (incl. the client's
//     own approve/deny — mirrors the daemon's fan-out).

import type { PermissionRequest, WSMessage } from '../api/types';

const SEC = 1_000;
const MIN = 60_000;
const now = Date.now();
const iso = (msAgo: number): string => new Date(now - msAgo).toISOString();
/** Frozen default approval window (docs/hooks-protocol.md, Q-A). */
const APPROVAL_TIMEOUT_MS = 120 * SEC;

/** Verbatim PermissionRequest hook stdin (E1 fixture shape), as the daemon stores it. */
function stdin(
  sessionUuid: string,
  cwd: string,
  toolName: string,
  toolInput: Record<string, unknown>,
): string {
  return JSON.stringify({
    session_id: sessionUuid,
    transcript_path: `/Users/user/.claude/projects/${sessionUuid}.jsonl`,
    cwd,
    permission_mode: 'default',
    hook_event_name: 'PermissionRequest',
    tool_name: toolName,
    tool_input: toolInput,
  });
}

// Session uuids/cwds mirror mockSessions in ./data.ts (ids 1–5).
const store: PermissionRequest[] = [
  // --- pending (ages 42 s and 95 s — the second expires live ~25 s after load)
  {
    id: 41,
    sessionId: 3,
    toolName: 'Bash',
    requestJson: stdin('9c11d2e3-f4a5-4b6c-8d7e-90f1a2b3c4d5', '/Users/user/work/swarmery', 'Bash', {
      command: 'rm -rf node_modules && npm ci',
      description: 'Reinstall dependencies from scratch',
    }),
    status: 'pending',
    requestedAt: iso(42 * SEC),
    resolvedAt: null,
    resolvedVia: null,
    reason: null,
    expiresAt: iso(42 * SEC - APPROVAL_TIMEOUT_MS),
  },
  {
    id: 42,
    sessionId: 1,
    toolName: 'Write',
    requestJson: stdin('a3f2b8c1-4d5e-4f60-8a71-b2c3d4e5f601', '/Users/user/work/orders-api', 'Write', {
      file_path: '.github/workflows/deploy.yml',
      content: 'name: deploy\non:\n  push:\n    branches: [main]\n',
    }),
    status: 'pending',
    requestedAt: iso(95 * SEC),
    resolvedAt: null,
    resolvedVia: null,
    reason: null,
    expiresAt: iso(95 * SEC - APPROVAL_TIMEOUT_MS),
  },
  // --- history (one of each terminal status)
  {
    id: 38,
    sessionId: 1,
    toolName: 'Edit',
    requestJson: stdin('a3f2b8c1-4d5e-4f60-8a71-b2c3d4e5f601', '/Users/user/work/orders-api', 'Edit', {
      file_path: 'internal/mail/templates.go',
      old_string: 'legacy.CreateTemplate',
      new_string: 'v2.CreateEmailTemplate',
    }),
    status: 'approved',
    requestedAt: iso(13 * MIN),
    resolvedAt: iso(13 * MIN - 34 * SEC),
    resolvedVia: 'dashboard',
    reason: null,
    expiresAt: iso(13 * MIN - APPROVAL_TIMEOUT_MS),
  },
  {
    id: 37,
    sessionId: 4,
    toolName: 'Bash',
    requestJson: stdin('b4c5d6e7-f8a9-4b0c-8d1e-2f3a4b5c6d7e', '/Users/user/work/orders-api', 'Bash', {
      command: 'git push --force origin main',
      description: 'Force-push the rebased branch',
    }),
    status: 'denied',
    requestedAt: iso(28 * MIN),
    resolvedAt: iso(28 * MIN - 51 * SEC),
    resolvedVia: 'dashboard',
    reason: 'force-push to main is never ok — open a PR instead',
    expiresAt: iso(28 * MIN - APPROVAL_TIMEOUT_MS),
  },
  {
    id: 36,
    sessionId: 4,
    toolName: 'Bash',
    requestJson: stdin('b4c5d6e7-f8a9-4b0c-8d1e-2f3a4b5c6d7e', '/Users/user/work/orders-api', 'Bash', {
      command: 'curl -X POST https://api.example.com/webhooks/test',
      description: 'Fire the test webhook',
    }),
    status: 'expired',
    requestedAt: iso(64 * MIN),
    resolvedAt: iso(62 * MIN),
    resolvedVia: null,
    reason: null,
    expiresAt: iso(62 * MIN),
  },
  {
    id: 35,
    sessionId: 5,
    toolName: 'Bash',
    requestJson: stdin('c5d6e7f8-a9b0-4c1d-8e2f-3a4b5c6d7e8f', '/Users/user/work/orders-api', 'Bash', {
      command: 'gh release create v0.3.0 --generate-notes',
      description: 'Cut the release',
    }),
    status: 'resolved_elsewhere',
    requestedAt: iso(126 * MIN),
    resolvedAt: iso(125 * MIN),
    resolvedVia: 'terminal',
    reason: null,
    expiresAt: iso(124 * MIN),
  },
];

/* ----- WS emitter bridge — the single mock socket registers itself here so
 * store transitions (approve/deny/expiry/injection) reach every subscriber
 * of the shared connection, like the daemon's fan-out. ----- */

type PermissionEmitter = (msg: WSMessage) => void;
let emitter: PermissionEmitter | null = null;

export function setMockPermissionEmitter(fn: PermissionEmitter | null): void {
  emitter = fn;
}

function emit(msg: WSMessage): void {
  emitter?.(msg);
}

/* ----- REST surface (used by mockApi in ./data.ts) ----- */

/**
 * GET /api/approvals?status= — `pending` | exact status | `resolved`
 * (meta-filter: every terminal status); no param → all rows.
 */
export function mockApprovalsList(status?: string): PermissionRequest[] {
  return store
    .filter((r) => {
      if (status === undefined) return true;
      if (status === 'resolved') return r.status !== 'pending';
      return r.status === status;
    })
    .map((r) => ({ ...r }));
}

/** POST /api/approvals/{id} — resolves a pending row, emits permission_resolved. */
export function mockResolveApproval(
  id: number,
  action: 'approve' | 'deny',
  reason?: string,
): PermissionRequest {
  const row = store.find((r) => r.id === id);
  if (row === undefined) throw new Error(`mock: approval ${String(id)} not found (404)`);
  if (row.status !== 'pending') {
    throw new Error(`mock: approval ${String(id)} already ${row.status} (409)`);
  }
  row.status = action === 'approve' ? 'approved' : 'denied';
  row.resolvedAt = new Date().toISOString();
  row.resolvedVia = 'dashboard';
  row.reason = reason !== undefined && reason.trim() !== '' ? reason.trim() : null;
  const resolved = { ...row };
  emit({ type: 'permission_resolved', payload: resolved });
  return resolved;
}

/* ----- WS scenario hooks (used by the mock socket in ./ws.ts) ----- */

let injectionDone = false;

/** Push one new pending request (scheduled ~3 s after load, fires once per page load). */
export function injectMockPermissionRequest(): void {
  if (injectionDone) return;
  injectionDone = true;
  const req: PermissionRequest = {
    id: 43,
    sessionId: 2,
    toolName: 'Bash',
    requestJson: stdin('e1f2a3b4-c5d6-4e7f-8091-a2b3c4d5e6f7', '/Users/user/work/example-app', 'Bash', {
      command: 'gh pr merge 42 --squash --delete-branch',
      description: 'Merge the dependency-upgrade PR',
    }),
    status: 'pending',
    requestedAt: new Date().toISOString(),
    resolvedAt: null,
    resolvedVia: null,
    reason: null,
    expiresAt: new Date(Date.now() + APPROVAL_TIMEOUT_MS).toISOString(),
  };
  store.unshift(req);
  emit({ type: 'permission_requested', payload: { ...req } });
}

/** The daemon's expiry sweeper, mocked: overdue pendings resolve as `expired`. */
export function sweepMockExpiry(): void {
  for (const r of store) {
    if (r.status !== 'pending') continue;
    if (new Date(r.expiresAt).getTime() > Date.now()) continue;
    r.status = 'expired';
    r.resolvedAt = new Date().toISOString();
    emit({ type: 'permission_resolved', payload: { ...r } });
  }
}
