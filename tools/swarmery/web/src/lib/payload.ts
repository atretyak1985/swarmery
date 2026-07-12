// Defensive extraction from Event.payload (contract: raw JSON, decoded
// client-side — shape is not guaranteed, so every read is best-effort).

import type { Event } from '../api/types';

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

/** First non-empty string found under any of `keys` (top level only). */
export function pickString(payload: unknown, keys: readonly string[]): string | null {
  if (!isRecord(payload)) return null;
  for (const key of keys) {
    const v = payload[key];
    if (typeof v === 'string' && v.trim() !== '') return v;
  }
  return null;
}

/** First finite number found under any of `keys`. */
export function pickNumber(payload: unknown, keys: readonly string[]): number | null {
  if (!isRecord(payload)) return null;
  for (const key of keys) {
    const v = payload[key];
    if (typeof v === 'number' && Number.isFinite(v)) return v;
  }
  return null;
}

/** One-line argument summary for a tool-call row. */
export function argSummary(event: Event): string | null {
  return pickString(event.payload, [
    'command',
    'file_path',
    'filePath',
    'path',
    'pattern',
    'query',
    'url',
    'description',
    'prompt',
    'skill',
    'summary',
    'text',
    'message',
  ]);
}

/** Failure text shown inline (without expanding) for error/denied/timeout rows. */
export function errorText(event: Event): string | null {
  return (
    pickString(event.payload, ['error', 'stderr', 'message', 'result', 'reason', 'text']) ??
    (event.status !== null && event.status !== 'ok' ? event.status : null)
  );
}

/** Subagent display name from a subagent_start payload. */
export function subagentName(event: Event): string {
  return (
    pickString(event.payload, ['agentType', 'agent_type', 'name', 'subagent_type']) ?? 'subagent'
  );
}

/** Pretty-printed payload for the expanded state. */
export function payloadJson(event: Event): string {
  try {
    return JSON.stringify(event.payload, null, 2) ?? 'null';
  } catch {
    return String(event.payload);
  }
}
