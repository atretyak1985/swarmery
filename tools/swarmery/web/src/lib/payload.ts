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

/** One-line "now: <last action>" text for a live session card. */
export function liveActionText(event: Event): string | null {
  if (event.type === 'user_prompt') {
    const text = pickString(event.payload, ['content']);
    return text !== null ? `prompt: ${text}` : 'prompt';
  }
  const arg = argSummary(event);
  if (event.toolName !== null) {
    return arg !== null ? `${event.toolName} ${arg}` : event.toolName;
  }
  return arg;
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

/**
 * Human task description from a subagent_start payload (real daemon rows
 * carry `description` next to `subagent_type`). Null when absent so callers
 * can fall back to the type.
 */
export function subagentDescription(event: Event): string | null {
  return pickString(event.payload, ['description']);
}

/**
 * Skill name from a skill_use payload. Real daemon rows nest it under
 * `input.skill` / `result.commandName`; flat `skill`/`name` kept as fallback.
 */
export function skillName(event: Event): string | null {
  if (!isRecord(event.payload)) return null;
  return (
    pickString(event.payload['input'], ['skill']) ??
    pickString(event.payload['result'], ['commandName']) ??
    pickString(event.payload, ['skill', 'name'])
  );
}

/**
 * Skill active when the tool call was issued (the skill→tool edge of the call
 * tree). The ingester stores the transcript's `attributionSkill` inside the
 * input map, so it sits at payload top level on open events / subagent_start
 * and under `input` once closeToolCall rebuilds the payload as {input, result}.
 */
export function attributedSkill(event: Event): string | null {
  if (!isRecord(event.payload)) return null;
  return (
    pickString(event.payload, ['attributionSkill']) ??
    pickString(event.payload['input'], ['attributionSkill'])
  );
}

/**
 * One-line argument of a tool call for the call-tree tooltip samples: reads
 * the tool input whether the payload is still open (flat input map) or closed
 * (rebuilt as {input, result}).
 */
export function toolArg(event: Event): string | null {
  if (!isRecord(event.payload)) return null;
  const input = isRecord(event.payload['input']) ? event.payload['input'] : event.payload;
  return pickString(input, [
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
  ]);
}

/** Pretty-printed payload for the expanded state. */
export function payloadJson(event: Event): string {
  try {
    return JSON.stringify(event.payload, null, 2) ?? 'null';
  } catch {
    return String(event.payload);
  }
}
