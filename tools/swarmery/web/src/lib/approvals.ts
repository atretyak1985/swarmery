// Defensive helpers around PermissionRequest.requestJson — the verbatim
// PermissionRequest hook stdin stored as a TEXT column (contract: a JSON
// string, but shape is upstream-undocumented — every read is best-effort
// and a malformed payload must render as raw text, never crash a card).

import type { PermissionRequest } from '../api/types';
import { pickString } from './payload';

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

/** Parsed hook stdin object, or null when requestJson isn't a JSON object. */
export function parsedRequest(r: PermissionRequest): Record<string, unknown> | null {
  try {
    const v = JSON.parse(r.requestJson) as unknown;
    return isRecord(v) ? v : null;
  } catch {
    return null;
  }
}

/** The `tool_input` of the hook stdin (unknown shape). */
export function requestToolInput(r: PermissionRequest): unknown {
  return parsedRequest(r)?.['tool_input'] ?? null;
}

/** tool_input essentials, by tool: Bash → command, Edit/Write → file_path, … */
const SUMMARY_KEYS = [
  'command',
  'file_path',
  'filePath',
  'path',
  'pattern',
  'url',
  'query',
  'prompt',
  'description',
] as const;

/** One-line "what does it want to do" summary for a request card. */
export function requestSummary(r: PermissionRequest): string {
  const input = requestToolInput(r);
  const essential = pickString(input, SUMMARY_KEYS);
  if (essential !== null) return essential;
  try {
    const compact = JSON.stringify(input ?? (JSON.parse(r.requestJson) as unknown));
    if (typeof compact === 'string' && compact !== 'null') return compact;
  } catch {
    // fall through to the raw string
  }
  return r.requestJson;
}

/** Pretty-printed full hook stdin for the expanded state (raw on parse failure). */
export function requestJsonPretty(r: PermissionRequest): string {
  const parsed = parsedRequest(r);
  if (parsed !== null) {
    try {
      return JSON.stringify(parsed, null, 2);
    } catch {
      // fall through to the raw string
    }
  }
  return r.requestJson;
}

/** Live clock text: 42 → "42 s", 83 → "1 m 23 s", 3700 → "1 h 01 m". */
export function fmtClock(totalSec: number): string {
  const sec = Math.max(0, Math.floor(totalSec));
  if (sec < 60) return `${String(sec)} s`;
  const min = Math.floor(sec / 60);
  if (min < 60) return `${String(min)} m ${String(sec % 60).padStart(2, '0')} s`;
  return `${String(Math.floor(min / 60))} h ${String(min % 60).padStart(2, '0')} m`;
}
