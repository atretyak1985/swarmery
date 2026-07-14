// Timeline tab: turns chronologically; tool calls as compact expandable rows;
// subagent spans as nested collapsible blocks (blue track — the signature
// element); errors red with failure text visible WITHOUT expanding.

import { useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import type { Event, SessionDetail, Turn } from '../../api/types';
import { fmtAgo, fmtDurationMs, fmtSpan, fmtTime } from '../../lib/format';
import {
  argSummary,
  errorText,
  payloadJson,
  pickString,
  subagentDescription,
  subagentName,
} from '../../lib/payload';
import { buildTimeline, countEvents, type TimelineNode } from '../../lib/timeline';
import { Empty } from '../../components/ui';

const TOOL_GLYPHS: Record<string, string> = {
  Read: '▤',
  Grep: '⌕',
  Glob: '⌕',
  Edit: '✎',
  Write: '✎',
  Bash: '❯',
  Agent: '⬡',
  Task: '⬡',
  Skill: '◈',
  WebFetch: '↯',
  WebSearch: '↯',
};

const TYPE_LABELS: Partial<Record<Event['type'], string>> = {
  permission_request: 'Permission',
  permission_resolved: 'Permission',
  error: 'Error',
  test_run: 'Tests',
  commit: 'Commit',
  skill_use: 'Skill',
  file_change: 'File change',
  session_end: 'Session end',
};

function glyphFor(event: Event): string {
  if (event.toolName !== null && TOOL_GLYPHS[event.toolName] !== undefined) {
    return TOOL_GLYPHS[event.toolName] ?? '·';
  }
  if (event.type === 'skill_use') return '◈';
  if (event.type === 'commit') return '⎇';
  if (event.type === 'error') return '✕';
  return '·';
}

function labelFor(event: Event): string {
  const base = event.toolName ?? TYPE_LABELS[event.type] ?? event.type;
  if (event.status === 'error') return `${base} · error`;
  if (event.status === 'denied') return `${base} · denied`;
  if (event.status === 'timeout') return `${base} · timeout`;
  return base;
}

function isErrorEvent(event: Event): boolean {
  return (
    event.type === 'error' ||
    event.status === 'error' ||
    event.status === 'denied' ||
    event.status === 'timeout'
  );
}

function isWaitingEvent(event: Event): boolean {
  return event.type === 'permission_request' && event.status === null;
}

function EventRow({ event, inSubagent }: { event: Event; inSubagent: boolean }): JSX.Element {
  const [open, setOpen] = useState(false);
  const failed = isErrorEvent(event);
  // Waiting permission_request rows are rendered by AwaitingApprovalNode
  // instead — this component never receives one (see Nodes below).
  const arg = argSummary(event);
  const failure = failed ? errorText(event) : null;

  const rail = failed ? 'border-red/50' : inSubagent ? 'border-blue/35' : 'border-line';
  const dot = failed
    ? 'border-red bg-red/20'
    : event.status === 'ok'
      ? 'border-green bg-surface2'
      : 'border-ink-dim bg-surface2';
  const toolTone = failed ? 'text-red' : 'text-ink';

  return (
    <div className={`relative ml-[5px] border-l-2 py-1.5 pl-3 ${rail}`}>
      <span
        className={`absolute top-[15px] -left-[5px] h-2 w-2 rounded-full border-2 ${dot}`}
        aria-hidden="true"
      />
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        className="flex w-full items-start gap-2.5 text-left"
      >
        <span className="min-w-[42px] pt-0.5 font-mono text-[10.5px] text-ink-dim">
          {fmtTime(event.ts)}
        </span>
        <span className="min-w-0 flex-1">
          <span className={`block font-mono text-[12px] font-medium ${toolTone}`}>
            <span className="mr-1 text-ink-dim" aria-hidden="true">
              {glyphFor(event)}
            </span>
            {labelFor(event)}
          </span>
          {arg !== null && (
            <span className="block truncate font-mono text-[10.5px] text-ink-faint">{arg}</span>
          )}
        </span>
        <span className="shrink-0 pt-0.5 font-mono text-[10px] text-ink-faint">
          {fmtDurationMs(event.durationMs)}
        </span>
      </button>
      {failure !== null && (
        <div className="mt-[7px] rounded-[7px] border border-red/30 bg-red/6 px-2.5 py-1.5 font-mono text-[11px] text-red">
          {failure}
        </div>
      )}
      {open && (
        <pre className="mt-1.5 overflow-x-auto rounded-md border border-line bg-bg px-2.5 py-2 font-mono text-[10.5px] leading-relaxed text-ink-2">
          {payloadJson(event)}
        </pre>
      )}
    </div>
  );
}

function SubagentBlock({
  node,
}: {
  node: Extract<TimelineNode, { kind: 'subagent' }>;
}): JSX.Element {
  const [open, setOpen] = useState(true);
  // WHO did WHAT: description is the primary label; agent type is a dimmed
  // suffix (or the fallback label when the payload has no description).
  const type = subagentName(node.start);
  const description = subagentDescription(node.start);
  const events = countEvents(node.children);
  const duration =
    node.stop !== null
      ? fmtDurationMs(node.stop.durationMs ?? null) || fmtSpan(node.start.ts, node.stop.ts)
      : 'running…';

  return (
    <div className="my-1.5 ml-[5px] rounded-r-lg border-l-2 border-blue bg-blue/5 py-2 pr-2.5 pl-3">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        className="flex w-full items-center gap-2 text-left font-mono text-[11.5px]"
      >
        <span
          className={`text-ink-dim transition-transform ${open ? 'rotate-90' : ''}`}
          aria-hidden="true"
        >
          ▶
        </span>
        <span className="min-w-0 truncate font-bold text-blue">⬡ {description ?? type}</span>
        <span className="shrink-0 whitespace-nowrap text-ink-dim">
          {description !== null ? type : 'subagent'} · {events} events
        </span>
        <span className={`ml-auto shrink-0 ${node.stop === null ? 'text-green' : 'text-ink-dim'}`}>
          {duration}
        </span>
      </button>
      {open && (
        <div className="mt-1.5">
          <Nodes nodes={node.children} inSubagent />
        </div>
      )}
    </div>
  );
}

function Prompt({ event }: { event: Event }): JSX.Element {
  const text =
    pickString(event.payload, ['text', 'prompt', 'content', 'message']) ?? '(empty prompt)';
  return (
    <div className="mb-2 rounded-[10px] border border-line bg-surface2 px-3 py-2.5 text-[13px] leading-normal text-ink">
      {text}
    </div>
  );
}

/** Trailing "awaiting approval" node — Canvas's dedicated amber-ring card.
 * Approve/deny identity (permission_requests.id) is not on this event, so
 * the actions route to the Approvals screen rather than resolving inline. */
function AwaitingApprovalNode({ event }: { event: Event }): JSX.Element {
  const command = argSummary(event);
  return (
    <div className="relative py-0 pb-1 pl-6">
      <span
        className="absolute top-[3px] left-0 box-border h-3 w-3 animate-blink-dot rounded-full border-2 border-amber bg-amber/20"
        aria-hidden="true"
      />
      <div className="rounded-[10px] border border-amber/32 bg-amber/6 px-3.5 py-[11px]">
        <div className="flex items-center gap-2">
          <span className="font-mono text-[12px] font-bold text-amber">
            {(event.toolName ?? 'Permission') + ' · awaiting approval'}
          </span>
          <span className="ml-auto shrink-0 font-mono text-[10px] text-amber">
            {fmtAgo(event.ts)}
          </span>
        </div>
        {command !== null && (
          <div className="mt-1.5 font-mono text-[11.5px] text-ink-2">{command}</div>
        )}
        <div className="mt-1.5 text-[12.5px] leading-[1.5] text-ink-3">
          <span className="text-ink-faint">→ </span>
          respond in the terminal, or approve/deny from the Approvals screen.
        </div>
        <div className="mt-2.5 flex gap-[7px]">
          <Link
            to="/approvals"
            className="inline-flex min-h-9 items-center rounded-lg border border-green/40 bg-green/10 px-4 font-mono text-[11px] font-semibold text-green transition-colors hover:bg-green/20 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand"
          >
            approve
          </Link>
          <Link
            to="/approvals"
            className="inline-flex min-h-9 items-center rounded-lg border border-red/40 bg-transparent px-4 font-mono text-[11px] text-red transition-colors hover:bg-red/10 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand"
          >
            deny
          </Link>
        </div>
      </div>
    </div>
  );
}

function Nodes({
  nodes,
  inSubagent = false,
}: {
  nodes: TimelineNode[];
  inSubagent?: boolean;
}): JSX.Element {
  return (
    <>
      {nodes.map((node) =>
        node.kind === 'subagent' ? (
          <SubagentBlock key={`sub-${node.start.id}`} node={node} />
        ) : node.event.type === 'user_prompt' ? (
          <Prompt key={node.event.id} event={node.event} />
        ) : isWaitingEvent(node.event) ? (
          <AwaitingApprovalNode key={node.event.id} event={node.event} />
        ) : (
          <EventRow key={node.event.id} event={node.event} inSubagent={inSubagent} />
        ),
      )}
    </>
  );
}

function TurnHeader({ turn }: { turn: Turn | null }): JSX.Element {
  return (
    <div className="mt-4 mb-1.5 flex items-center gap-2 font-mono text-[10.5px] tracking-[0.1em] text-ink-dim uppercase">
      {turn !== null ? `turn ${turn.seq} · ${fmtTime(turn.startedAt)}` : 'unassigned events'}
      <span className="h-px flex-1 bg-line" aria-hidden="true" />
    </div>
  );
}

export function Timeline({ detail }: { detail: SessionDetail }): JSX.Element {
  const groups = useMemo(() => buildTimeline(detail), [detail]);

  if (groups.length === 0) {
    return <Empty>no events in this session yet</Empty>;
  }
  return (
    <div className="mt-[26px]">
      {groups.map((group) => (
        <section key={group.turn !== null ? `turn-${group.turn.id}` : 'orphans'}>
          <TurnHeader turn={group.turn} />
          <Nodes nodes={group.nodes} />
        </section>
      ))}
    </div>
  );
}
