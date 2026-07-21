// Call-tree card for the desktop detail rail (below FILES CHANGED): who called
// what — skills (amber, ◈), subagents (blue, ⬡, recursive), tools aggregated
// per name inside each container. Derived client-side from the already-loaded
// events (rail convention — no extra API calls); WS event_appended rebuilds it
// through the parent's useMemo. Replaces the old flat agents/skills chips card:
// same names and System deep-links (↗), plus structure. Rows stay minimal —
// name plus live status (running / errors); durations, tokens, agent type and
// the rest live in the hover tooltip (ChartTooltip visual language).

import { useState } from 'react';
import { Link } from 'react-router-dom';
import {
  countAgentRuns,
  countToolCalls,
  type AgentGroupNode,
  type AgentNode,
  type CallNode,
  type SkillNode,
  type ToolNode,
} from '../../lib/calltree';
import { fmtDurationMs, fmtTime, fmtTokens } from '../../lib/format';
import { toolGlyph } from '../../lib/glyphs';
import { useHoverTip } from '../../components/HoverTip';

// ── hover tooltip content ────────────────────────────────────────────────────

function Tip({
  glyph,
  title,
  tone,
  children,
}: {
  glyph: string;
  title: string;
  tone: string;
  children: React.ReactNode;
}): JSX.Element {
  return (
    <div className="rounded-lg border border-line-strong bg-bg/95 px-3 py-2.5 shadow-lg backdrop-blur-sm">
      <div className={`mb-1.5 font-mono text-[11px] font-semibold break-words ${tone}`}>
        <span className="text-ink-dim" aria-hidden="true">
          {glyph}{' '}
        </span>
        {title}
      </div>
      {children}
    </div>
  );
}

function TipStat({
  label,
  value,
  tone = 'text-ink',
}: {
  label: string;
  value: string;
  tone?: string;
}): JSX.Element {
  return (
    <div className="flex items-baseline justify-between gap-3 py-px font-mono text-[10.5px]">
      <span className="shrink-0 text-ink-dim">{label}</span>
      <span className={`min-w-0 truncate text-right ${tone}`}>{value}</span>
    </div>
  );
}

/** Dimmed sample/run line under a divider (args, commands, run descriptions). */
function TipNotes({ lines, more = 0 }: { lines: string[]; more?: number }): JSX.Element | null {
  if (lines.length === 0) return null;
  return (
    <div className="mt-1.5 border-t border-line pt-1.5">
      {lines.map((line) => (
        <div key={line} className="truncate py-px font-mono text-[10px] text-ink-faint">
          {line}
        </div>
      ))}
      {more > 0 && (
        <div className="py-px font-mono text-[10px] text-ink-dim">{`+${String(more)} more`}</div>
      )}
    </div>
  );
}

function ToolTip({ node }: { node: ToolNode }): JSX.Element {
  return (
    <Tip glyph={toolGlyph(node.name)} title={node.name} tone="text-ink">
      <TipStat label="calls" value={`×${String(node.count)}`} />
      {node.errors > 0 && <TipStat label="errors" value={String(node.errors)} tone="text-red" />}
      {node.totalMs > 0 && <TipStat label="total time" value={fmtDurationMs(node.totalMs)} />}
      {node.totalMs > 0 && node.count > 1 && (
        <TipStat label="avg" value={fmtDurationMs(Math.round(node.totalMs / node.count))} />
      )}
      <TipNotes lines={node.samples} />
    </Tip>
  );
}

function SkillTip({ node }: { node: SkillNode }): JSX.Element {
  const tools = countToolCalls(node.children);
  const agents = countAgentRuns(node.children);
  return (
    <Tip glyph="◈" title={node.name} tone="text-amber">
      <TipStat label="invocations" value={`×${String(node.count)}`} />
      {tools > 0 && <TipStat label="tool calls" value={String(tools)} />}
      {agents > 0 && <TipStat label="subagents" value={String(agents)} />}
      <TipNotes lines={node.args !== null ? [node.args] : []} />
    </Tip>
  );
}

function AgentTip({ node }: { node: AgentNode }): JSX.Element {
  const tools = countToolCalls(node.children);
  const agents = countAgentRuns(node.children);
  const status = node.running ? 'running' : node.failed ? 'failed' : 'ok';
  const statusTone = node.running ? 'text-green' : node.failed ? 'text-red' : 'text-ink';
  return (
    <Tip
      glyph="⬡"
      title={node.description ?? node.type}
      tone={node.failed ? 'text-red' : 'text-blue'}
    >
      <TipStat label="agent" value={node.type} />
      <TipStat label="status" value={status} tone={statusTone} />
      <TipStat label="started" value={fmtTime(node.startedAt)} />
      {node.durationMs !== null && !node.running && (
        <TipStat label="duration" value={fmtDurationMs(node.durationMs)} />
      )}
      {node.tokens !== null && node.tokens > 0 && (
        <TipStat label="tokens" value={fmtTokens(node.tokens)} />
      )}
      {tools > 0 && <TipStat label="tool calls" value={String(tools)} />}
      {agents > 0 && <TipStat label="nested agents" value={String(agents)} />}
    </Tip>
  );
}

const GROUP_TIP_RUNS = 6;

function AgentGroupTip({ node }: { node: AgentGroupNode }): JSX.Element {
  const shown = node.runs.slice(0, GROUP_TIP_RUNS);
  return (
    <Tip glyph="⬡" title={`${node.type} ×${String(node.count)}`} tone="text-blue">
      {node.running > 0 && <TipStat label="running" value={String(node.running)} tone="text-green" />}
      {node.totalMs > 0 && <TipStat label="total time" value={fmtDurationMs(node.totalMs)} />}
      {node.tokens > 0 && <TipStat label="Σ tokens" value={fmtTokens(node.tokens)} />}
      <TipNotes
        lines={shown.map((run) => {
          const label = run.description ?? run.type;
          return run.running
            ? `${label} — running`
            : run.durationMs !== null
              ? `${label} — ${fmtDurationMs(run.durationMs)}`
              : label;
        })}
        more={node.runs.length - shown.length}
      />
    </Tip>
  );
}

// ── rows ─────────────────────────────────────────────────────────────────────

/** Chevron + label row that toggles its children; ↗ deep-links into System. */
function BranchRow({
  label,
  labelTone,
  glyph,
  meta,
  systemLink,
  nodes,
  tip,
}: {
  label: string;
  labelTone: string;
  glyph: string;
  meta: JSX.Element[];
  systemLink: string | null;
  nodes: CallNode[];
  tip: JSX.Element;
}): JSX.Element {
  const [open, setOpen] = useState(false);
  const { handlers, portal } = useHoverTip(tip);
  const hasKids = nodes.length > 0;
  return (
    <div>
      <div className="flex items-center gap-1.5 py-[3px]" {...handlers}>
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          aria-expanded={open}
          disabled={!hasKids}
          className="flex min-w-0 flex-1 items-center gap-1.5 text-left font-mono text-[11px]"
        >
          <span
            className={`w-2 shrink-0 text-[9px] text-ink-dim transition-transform ${open ? 'rotate-90' : ''} ${hasKids ? '' : 'opacity-0'}`}
            aria-hidden="true"
          >
            ▶
          </span>
          <span className={`min-w-0 truncate font-medium ${labelTone}`}>
            <span aria-hidden="true">{glyph} </span>
            {label}
          </span>
          <span className="ml-auto flex shrink-0 items-center gap-1.5">{meta}</span>
        </button>
        {systemLink !== null && (
          <Link
            to={systemLink}
            title="open in System"
            className="shrink-0 font-mono text-[10px] text-ink-faint transition-colors hover:text-ink"
          >
            ↗
          </Link>
        )}
      </div>
      {portal}
      {open && hasKids && (
        <div className="mb-1 ml-[3px] border-l border-line-soft pl-[13px]">
          <Nodes nodes={nodes} />
        </div>
      )}
    </div>
  );
}

function ToolRow({ node }: { node: ToolNode }): JSX.Element {
  const { handlers, portal } = useHoverTip(<ToolTip node={node} />);
  return (
    <>
      <div className="flex items-center gap-1.5 py-[3px] font-mono text-[11px]" {...handlers}>
        <span className="w-2 shrink-0" aria-hidden="true" />
        <span className="min-w-0 truncate text-ink-3">
          <span className="text-ink-dim" aria-hidden="true">
            {toolGlyph(node.name)}{' '}
          </span>
          {node.name}
          {node.count > 1 ? ` ×${String(node.count)}` : ''}
        </span>
        <span className="ml-auto flex shrink-0 items-center gap-1.5">
          {node.errors > 0 && (
            <span className="shrink-0 font-mono text-[10px] text-red">
              {String(node.errors)} err
            </span>
          )}
        </span>
      </div>
      {portal}
    </>
  );
}

function SkillRow({ node }: { node: SkillNode }): JSX.Element {
  return (
    <BranchRow
      label={node.name + (node.count > 1 ? ` ×${String(node.count)}` : '')}
      labelTone="text-amber"
      glyph="◈"
      meta={[]}
      systemLink={`/system?tab=skills&find=${encodeURIComponent(node.name)}`}
      nodes={node.children}
      tip={<SkillTip node={node} />}
    />
  );
}

function AgentRow({ node }: { node: AgentNode }): JSX.Element {
  const meta: JSX.Element[] = [];
  if (node.running) {
    meta.push(
      <span key="run" className="shrink-0 font-mono text-[10px] text-green">
        running
      </span>,
    );
  } else if (node.failed) {
    meta.push(
      <span key="fail" className="shrink-0 font-mono text-[10px] text-red">
        failed
      </span>,
    );
  }
  return (
    <BranchRow
      label={node.description ?? node.type}
      labelTone={node.failed ? 'text-red' : 'text-blue'}
      glyph="⬡"
      meta={meta}
      systemLink={`/system?tab=agents&find=${encodeURIComponent(node.type)}`}
      nodes={node.children}
      tip={<AgentTip node={node} />}
    />
  );
}

function AgentGroupRow({ node }: { node: AgentGroupNode }): JSX.Element {
  const meta: JSX.Element[] = [];
  if (node.running > 0) {
    meta.push(
      <span key="run" className="shrink-0 font-mono text-[10px] text-green">
        {`${String(node.running)} running`}
      </span>,
    );
  }
  return (
    <BranchRow
      label={`${node.type} ×${String(node.count)}`}
      labelTone="text-blue"
      glyph="⬡"
      meta={meta}
      systemLink={`/system?tab=agents&find=${encodeURIComponent(node.type)}`}
      nodes={node.runs}
      tip={<AgentGroupTip node={node} />}
    />
  );
}

function Nodes({ nodes }: { nodes: CallNode[] }): JSX.Element {
  return (
    <>
      {nodes.map((node, i) => {
        switch (node.kind) {
          case 'tool':
            return <ToolRow key={`t-${node.name}`} node={node} />;
          case 'skill':
            return <SkillRow key={`s-${node.name}`} node={node} />;
          case 'agent':
            return <AgentRow key={`a-${String(node.id)}`} node={node} />;
          case 'agent-group':
            return <AgentGroupRow key={`g-${node.type}-${String(i)}`} node={node} />;
        }
      })}
    </>
  );
}

export function CallTreeCard({
  nodes,
  className = '',
}: {
  nodes: CallNode[];
  className?: string;
}): JSX.Element | null {
  if (nodes.length === 0) return null;
  return (
    <div className={`rounded-xl border border-line bg-surface px-4 py-3.5 ${className}`}>
      <div className="mb-1 font-mono text-[10.5px] tracking-[0.08em] text-ink-dim uppercase">
        call tree
      </div>
      <Nodes nodes={nodes} />
    </div>
  );
}
