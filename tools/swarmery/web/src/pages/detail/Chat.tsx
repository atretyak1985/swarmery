// Chat tab: the session as a human-readable conversation (Claude-app style).
// User turns render as right-aligned tinted bubbles, assistant turns as
// markdown prose; each turn's tool activity collapses into a subtle one-liner
// ("Ran 5 commands, used 2 tools") that jumps to the Timeline tab. Long
// assistant texts clamp to ~20 lines with a show-more expander.

import { useMemo, useState } from 'react';
import type { Event, SessionDetail, Turn } from '../../api/types';
import { fmtTime } from '../../lib/format';
import { Markdown } from '../../lib/markdown';
import { pickString } from '../../lib/payload';
import { Empty } from '../../components/ui';

/* ----- tool activity one-liner ----- */

interface ToolCounts {
  commands: number;
  tools: number;
  agents: number;
  skills: number;
}

function countTools(events: readonly Event[]): ToolCounts {
  const c: ToolCounts = { commands: 0, tools: 0, agents: 0, skills: 0 };
  for (const e of events) {
    if (e.type === 'subagent_start') c.agents += 1;
    else if (e.type === 'skill_use') c.skills += 1;
    else if (
      e.type === 'tool_call' ||
      e.type === 'test_run' ||
      e.type === 'commit' ||
      e.type === 'file_change'
    ) {
      if (e.toolName === 'Bash') c.commands += 1;
      else c.tools += 1;
    }
  }
  return c;
}

function plural(n: number, word: string): string {
  return `${String(n)} ${word}${n === 1 ? '' : 's'}`;
}

function toolSummary(c: ToolCounts): string | null {
  const parts: string[] = [];
  if (c.commands > 0) parts.push(`ran ${plural(c.commands, 'command')}`);
  if (c.tools > 0) parts.push(`used ${plural(c.tools, 'tool')}`);
  if (c.agents > 0) parts.push(plural(c.agents, 'agent'));
  if (c.skills > 0) parts.push(plural(c.skills, 'skill'));
  if (parts.length === 0) return null;
  const s = parts.join(', ');
  return s.charAt(0).toUpperCase() + s.slice(1);
}

function ToolLine({ summary, onClick }: { summary: string; onClick: () => void }): JSX.Element {
  return (
    <button
      type="button"
      onClick={onClick}
      title="Show in Timeline"
      className="my-1.5 flex items-center gap-1.5 rounded-md px-1 py-0.5 font-mono text-[11px] text-ink-dim transition-colors hover:text-brand"
    >
      <span aria-hidden="true">⚙</span>
      <span>{summary}</span>
      <span aria-hidden="true">›</span>
    </button>
  );
}

/* ----- assistant prose with ~20-line clamp ----- */

const CLAMP_LINES = 20;
const CLAMP_CHARS = 1800; // long single-paragraph texts wrap into many lines too
// 20 lines × ~21px line height + block margins.
const CLAMP_MAX_H = 'max-h-[440px]';

function ClampedProse({ text }: { text: string }): JSX.Element {
  const [expanded, setExpanded] = useState(false);
  const long = text.split('\n').length > CLAMP_LINES || text.length > CLAMP_CHARS;
  return (
    <div>
      <div className={`relative ${long && !expanded ? `${CLAMP_MAX_H} overflow-hidden` : ''}`}>
        <Markdown text={text} />
        {long && !expanded && (
          <div
            className="pointer-events-none absolute inset-x-0 bottom-0 h-12 bg-gradient-to-t from-bg to-transparent"
            aria-hidden="true"
          />
        )}
      </div>
      {long && (
        <button
          type="button"
          onClick={() => setExpanded((v) => !v)}
          aria-expanded={expanded}
          className="mt-1 font-mono text-[11px] font-medium text-brand hover:text-ink"
        >
          {expanded ? 'show less' : 'show more'}
        </button>
      )}
    </div>
  );
}

/* ----- one turn ----- */

function turnText(turn: Turn, events: readonly Event[]): string | null {
  if (turn.text !== null && turn.text.trim() !== '') return turn.text;
  // Pre-0005 rows (text not yet backfilled): user prompts survive as the
  // user_prompt event payload (truncated) — better than an empty bubble.
  if (turn.role === 'user') {
    const prompt = events.find((e) => e.type === 'user_prompt');
    if (prompt !== undefined) {
      return pickString(prompt.payload, ['content', 'text', 'prompt', 'message']);
    }
  }
  return null;
}

function ChatTurn({
  turn,
  events,
  onShowTimeline,
}: {
  turn: Turn;
  events: readonly Event[];
  onShowTimeline: () => void;
}): JSX.Element | null {
  const text = turnText(turn, events);
  const summary = toolSummary(countTools(events));
  if (text === null && summary === null) return null;

  if (turn.role === 'user') {
    return (
      <div className="my-3 flex flex-col items-end">
        <div className="max-w-[88%] rounded-[14px] rounded-br-[4px] border border-line bg-surface2 px-3.5 py-2.5 text-[13px] leading-relaxed whitespace-pre-wrap text-ink">
          {text ?? '(empty prompt)'}
        </div>
        <span className="mt-1 pr-1 font-mono text-[10px] text-ink-dim">{fmtTime(turn.startedAt)}</span>
      </div>
    );
  }
  return (
    <div className="my-3 text-[13.5px] text-ink-2">
      {text !== null && <ClampedProse text={text} />}
      {summary !== null && <ToolLine summary={summary} onClick={onShowTimeline} />}
    </div>
  );
}

/* ----- the tab ----- */

export function Chat({
  detail,
  onShowTimeline,
}: {
  detail: SessionDetail;
  onShowTimeline: () => void;
}): JSX.Element {
  const turns = useMemo(() => detail.turns.slice().sort((a, b) => a.seq - b.seq), [detail.turns]);
  const eventsByTurn = useMemo(() => {
    const map = new Map<number, Event[]>();
    for (const e of detail.events) {
      if (e.turnId === null) continue;
      const list = map.get(e.turnId);
      if (list) list.push(e);
      else map.set(e.turnId, [e]);
    }
    return map;
  }, [detail.events]);

  if (turns.length === 0) {
    return <Empty>no conversation in this session yet</Empty>;
  }
  const assistantTurns = turns.filter((t) => t.role === 'assistant');
  const needsBackfill = assistantTurns.length > 0 && assistantTurns.every((t) => t.text === null);
  return (
    <div className="mt-3">
      {turns.map((turn) => (
        <ChatTurn
          key={turn.id}
          turn={turn}
          events={eventsByTurn.get(turn.id) ?? []}
          onShowTimeline={onShowTimeline}
        />
      ))}
      {needsBackfill && (
        <div className="my-4 rounded-[10px] border border-dashed border-line px-3 py-2 text-center font-mono text-[10.5px] text-ink-dim">
          some assistant prose is not ingested yet — run{' '}
          <code className="text-brand">swarmery backfill --rebuild-text</code>
        </div>
      )}
    </div>
  );
}
