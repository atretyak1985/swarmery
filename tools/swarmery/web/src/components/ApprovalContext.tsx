// Structured gated-action context (fusion phase 11 — DESIGN.md §4.7): renders a
// permission request's tool + arguments + cwd inline, replacing the raw-JSON
// summary. Dependency-light and prop-driven so the Phase 9 console can reuse it
// without importing the Approvals page. Every read is best-effort — a malformed
// request_json degrades to the raw string, never crashes a card.

import type { PermissionRequest } from '../api/types';
import { parsedRequest, requestToolInput } from '../lib/approvals';
import { pickString } from '../lib/payload';

/** The primary argument of a tool call, by tool convention (command, path, …). */
const PRIMARY_ARG_KEYS = [
  'command',
  'file_path',
  'filePath',
  'path',
  'pattern',
  'url',
  'query',
  'prompt',
] as const;

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

/** cwd from the hook stdin, when present. */
function cwdOf(request: PermissionRequest): string | null {
  const cwd = parsedRequest(request)?.['cwd'];
  return typeof cwd === 'string' && cwd !== '' ? cwd : null;
}

/** The primary argument string (command/path/url/…), or null. */
function primaryArgOf(request: PermissionRequest): string | null {
  return pickString(requestToolInput(request), PRIMARY_ARG_KEYS);
}

/**
 * Secondary tool_input fields worth showing as key/value chips, excluding the
 * primary arg (already rendered as the code block) and noisy/verbose keys.
 */
function secondaryArgsOf(request: PermissionRequest, primaryShown: boolean): [string, string][] {
  const input = requestToolInput(request);
  if (!isRecord(input)) return [];
  const skip = new Set<string>(['description']);
  if (primaryShown) for (const k of PRIMARY_ARG_KEYS) skip.add(k);
  const out: [string, string][] = [];
  for (const [k, v] of Object.entries(input)) {
    if (skip.has(k)) continue;
    let text: string;
    if (typeof v === 'string') text = v;
    else if (typeof v === 'number' || typeof v === 'boolean') text = String(v);
    else continue; // skip objects/arrays — keep the chip row scannable
    if (text.length > 120) text = text.slice(0, 117) + '…';
    out.push([k, text]);
  }
  return out;
}

/**
 * Inline structured context for a gated action. `sessionSlot` is an optional
 * caller-supplied node (e.g. a session link) rendered under the args.
 */
export function ApprovalContext({
  request,
  sessionSlot,
}: {
  request: PermissionRequest;
  sessionSlot?: React.ReactNode;
}): JSX.Element {
  const primary = primaryArgOf(request);
  const cwd = cwdOf(request);
  const secondary = secondaryArgsOf(request, primary !== null);

  return (
    <div className="mt-2.5 rounded-lg border border-line bg-bg px-3 py-2.5">
      <div className="flex flex-wrap items-center gap-2">
        <span className="rounded-md border border-brand/40 bg-brand/10 px-2 py-0.5 font-mono text-[10.5px] font-semibold text-brand">
          {request.toolName}
        </span>
        {cwd !== null && (
          <span className="min-w-0 font-mono text-[10px] text-ink-faint" title={cwd}>
            cwd <span className="text-ink-dim">{cwd}</span>
          </span>
        )}
      </div>

      {primary !== null && (
        <pre className="mt-2 max-h-40 overflow-y-auto rounded-md border border-line-soft bg-surface px-2.5 py-1.5 font-mono text-[11px] leading-relaxed break-all whitespace-pre-wrap text-ink-2">
          {primary}
        </pre>
      )}

      {secondary.length > 0 && (
        <div className="mt-1.5 flex flex-wrap gap-x-3 gap-y-1">
          {secondary.map(([k, v]) => (
            <span key={k} className="font-mono text-[10.5px] text-ink-dim">
              <span className="text-ink-faint">{k}:</span> {v}
            </span>
          ))}
        </div>
      )}

      {sessionSlot !== undefined && <div className="mt-2">{sessionSlot}</div>}
    </div>
  );
}
