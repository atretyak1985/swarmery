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

/* ----- AskUserQuestion answer form (hooks-protocol amendment 1, E12) -----
 * questionsOf parses the request into a typed form model; the Answer* helpers
 * below are pure so the form's enable/submit logic is unit-testable. */

export interface QuestionOption {
  readonly label: string;
  readonly description: string;
}

export interface ParsedQuestion {
  readonly question: string;
  readonly header: string;
  readonly options: readonly QuestionOption[];
  readonly multiSelect: boolean;
}

function parseOption(v: unknown): QuestionOption | null {
  if (!isRecord(v)) return null;
  const label = v['label'];
  if (typeof label !== 'string' || label === '') return null;
  const description = typeof v['description'] === 'string' ? v['description'] : '';
  return { label, description };
}

function parseQuestion(v: unknown): ParsedQuestion | null {
  if (!isRecord(v)) return null;
  const question = v['question'];
  if (typeof question !== 'string' || question === '') return null;
  const header = typeof v['header'] === 'string' ? v['header'] : '';
  const multiSelect = v['multiSelect'] === true;
  const rawOptions = v['options'] ?? [];
  if (!Array.isArray(rawOptions)) return null;
  const options: QuestionOption[] = [];
  for (const raw of rawOptions) {
    const option = parseOption(raw);
    if (option === null) return null;
    options.push(option);
  }
  return { question, header, options, multiSelect };
}

/**
 * Typed `tool_input.questions` of an AskUserQuestion request, or null when
 * anything about the shape is off — the card then falls back to the generic
 * raw-JSON rendering. Never throws (same posture as the helpers above).
 */
export function questionsOf(r: PermissionRequest): ParsedQuestion[] | null {
  const input = requestToolInput(r);
  if (!isRecord(input)) return null;
  const raw = input['questions'];
  if (!Array.isArray(raw) || raw.length === 0) return null;
  const questions: ParsedQuestion[] = [];
  for (const q of raw) {
    const parsed = parseQuestion(q);
    if (parsed === null) return null;
    questions.push(parsed);
  }
  return questions;
}

/** Per-question draft state of the answer form. */
export interface AnswerDraft {
  readonly selected: readonly string[];
  readonly freeText: string;
}

export const EMPTY_DRAFT: AnswerDraft = { selected: [], freeText: '' };

/** POST {action:"answer"} body: string, or an array of labels for multiSelect (E12c). */
export type AnswerMap = Record<string, string | string[]>;

/**
 * One question's wire value from its draft, or null while unanswered.
 * Free text (trimmed) is first-class: on single-select it wins over a
 * selection; on multiSelect it joins the selected labels.
 */
export function answerValueOf(q: ParsedQuestion, draft: AnswerDraft): string | string[] | null {
  const freeText = draft.freeText.trim();
  if (q.multiSelect) {
    const values = freeText === '' ? [...draft.selected] : [...draft.selected, freeText];
    return values.length > 0 ? values : null;
  }
  if (freeText !== '') return freeText;
  return draft.selected[0] ?? null;
}

/**
 * The full `answers` object for POST {action:"answer"}, or null while any
 * question still lacks an answer (null drives the submit-disabled state).
 */
export function buildAnswers(
  questions: readonly ParsedQuestion[],
  drafts: readonly AnswerDraft[],
): AnswerMap | null {
  const answers: AnswerMap = {};
  for (const [i, q] of questions.entries()) {
    const value = answerValueOf(q, drafts[i] ?? EMPTY_DRAFT);
    if (value === null) return null;
    answers[q.question] = value;
  }
  return answers;
}

/**
 * Pre-fill for the "always allow…" shortcut: Bash narrows to the command's
 * first word as a prefix rule (`Bash(git *)` — NOTE: prefix matching also
 * covers chained `&& …` commands); every other tool suggests the bare tool
 * name (any input).
 */
export function suggestRulePattern(request: PermissionRequest): string {
  if (request.toolName !== 'Bash') return request.toolName;
  try {
    const parsed = JSON.parse(request.requestJson) as { tool_input?: { command?: string } };
    const first = parsed.tool_input?.command?.trim().split(/\s+/)[0];
    return first !== undefined && first !== '' ? `Bash(${first} *)` : 'Bash';
  } catch {
    return 'Bash';
  }
}
