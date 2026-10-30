// Steps tab (handoff §5): the live ledger of the session, one row per step: a
// glyph, the step, its detail, and how long it took. Failed steps stay in the
// ledger with their failure text visible without opening them. A row opens its
// raw payload; a subagent is a row that opens onto its own steps, nested.
//
// The gate itself is the card above the tabs, so a waiting step only says what it
// waits on and carries the level badge. Chat reuses `Nodes` for its tool groups.

import type { I18n, MessageDescriptor } from '@lingui/core';
import { msg } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { useMemo, useState } from 'react';
import type { Event, SessionDetail } from '../../api/types';
import { fmtSpan, fmtTime } from '../../lib/format';
import {
  argSummary,
  errorText,
  payloadJson,
  pickString,
  subagentDescription,
  subagentName,
} from '../../lib/payload';
import { compactSpan, stepDuration } from '../../lib/sessionsView';
import { buildTimeline, countEvents, type TimelineNode } from '../../lib/timeline';
import type { DecisionTier } from '../../components/place/DecisionCard';
import { StateGlyph, type GlyphState } from '../../components/place/StateGlyph';
import { Empty } from '../../components/ui';

const TYPE_LABELS: Partial<Record<Event['type'], MessageDescriptor>> = {
  permission_request: msg`Permission`,
  permission_resolved: msg`Permission`,
  error: msg`Error`,
  test_run: msg`Tests`,
  commit: msg`Commit`,
  skill_use: msg`Skill`,
  file_change: msg`File change`,
  session_end: msg`Session end`,
};

// i18n-ignore: Tailwind classes
const STEP = 'grid grid-cols-[18px_minmax(0,1fr)_auto] items-start gap-3 border-b border-line py-2';
const STEP_DURATION = 'font-mono text-[10.5px] leading-[18px] whitespace-nowrap text-ink-faint';
const STEP_DETAIL = 'mt-[3px] font-mono text-[11.5px] leading-[1.5] break-words text-ink-3';

/** Shape and colour of the level badge on a gated step (handoff §7). */
const TIER_BADGE: Record<DecisionTier, { label: MessageDescriptor; className: string }> = {
  destructive: { label: msg`▲ destructive`, className: 'rounded-[3px] border border-red text-red' },
  write: { label: msg`◆ write`, className: 'rounded-full border border-dashed border-blue text-blue' },
  question: { label: msg`? question`, className: 'rounded-full border border-amber text-amber' },
};

function labelFor(event: Event, i18n: I18n): string {
  const typeLabel = TYPE_LABELS[event.type];
  const base = event.toolName ?? (typeLabel !== undefined ? i18n._(typeLabel) : event.type);
  if (event.status === 'error') return i18n._(msg`${base} · error`);
  if (event.status === 'denied') return i18n._(msg`${base} · denied`);
  if (event.status === 'timeout') return i18n._(msg`${base} · timeout`);
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

/** The agent's own words for the step, when the tool call carried them; the tool
 * then steps back to the grey tag beside it (handoff §5). */
function stepWords(event: Event): string | null {
  return pickString(event.payload, ['description']);
}

function EventRow({ event, active }: { event: Event; active: boolean }): JSX.Element {
  const { i18n } = useLingui();
  const [open, setOpen] = useState(false);
  const failed = isErrorEvent(event);
  const words = stepWords(event);
  const arg = argSummary(event);
  const failure = failed ? errorText(event) : null;
  const state: GlyphState = failed ? 'failed' : active ? 'working' : 'done';

  return (
    <div className={STEP} data-tip={fmtTime(event.ts)}>
      <StateGlyph state={state} size={12} className="mt-px" />
      <div className="min-w-0">
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          aria-expanded={open}
          className="flex min-w-0 items-baseline gap-2 text-left"
        >
          <span className={`text-[13px] leading-[normal] font-medium ${failed ? 'text-red' : 'text-ink'}`}>
            {words ?? labelFor(event, i18n)}
          </span>
          {words !== null && <span className="font-mono text-[10.5px] text-ink-faint">{labelFor(event, i18n)}</span>}
        </button>
        {arg !== null && arg !== words && <div className={STEP_DETAIL}>{arg}</div>}
        {failure !== null && <div className={`${STEP_DETAIL} text-red`}>{failure}</div>}
        {open && (
          <pre className="mt-1.5 overflow-x-auto rounded-md border border-line bg-bg px-2.5 py-2 font-mono text-[10.5px] leading-relaxed text-ink-2">
            {payloadJson(event)}
          </pre>
        )}
      </div>
      <span className={STEP_DURATION}>{stepDuration(event.durationMs)}</span>
    </div>
  );
}

function SubagentBlock({
  node,
}: {
  node: Extract<TimelineNode, { kind: 'subagent' }>;
}): JSX.Element {
  const { t } = useLingui();
  const [open, setOpen] = useState(false);
  // WHO did WHAT: the description leads; the agent type stands in for a tool.
  const type = subagentName(node.start);
  const description = subagentDescription(node.start);
  const events = countEvents(node.children);
  const running = node.stop === null;
  const duration =
    node.stop !== null
      ? stepDuration(node.stop.durationMs ?? null) || fmtSpan(node.start.ts, node.stop.ts)
      : t`running…`;

  return (
    <div>
      <div className={STEP}>
        <StateGlyph state={running ? 'working' : 'done'} size={12} className="mt-px" />
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          aria-expanded={open}
          className="flex min-w-0 items-baseline gap-2 text-left"
        >
          <span className="min-w-0 truncate text-[13px] leading-[normal] font-medium text-ink">
            {description ?? type}
          </span>
          <span className="shrink-0 font-mono text-[10.5px] whitespace-nowrap text-ink-faint">
            {description !== null ? type : t`subagent`} · <Trans>{events} events</Trans>
          </span>
          <span aria-hidden="true" className="shrink-0 font-mono text-[10px] text-ink-faint">
            {open ? '▾' : '▸'}
          </span>
        </button>
        <span className={STEP_DURATION}>{duration}</span>
      </div>
      {open && (
        <div className="border-l border-line-strong pl-3">
          <Nodes nodes={node.children} />
        </div>
      )}
    </div>
  );
}

function Prompt({ event }: { event: Event }): JSX.Element {
  const { t } = useLingui();
  const text =
    pickString(event.payload, ['text', 'prompt', 'content', 'message']) ?? t`(empty prompt)`;
  return (
    <div className={STEP}>
      <span aria-hidden="true" className="w-[18px] text-center font-mono text-[12px] leading-[18px] text-ink-faint">
        ›
      </span>
      <div className="line-clamp-2 min-w-0 text-[13px] leading-[1.45] text-ink-2">{text}</div>
      <span className={STEP_DURATION}>{fmtTime(event.ts)}</span>
    </div>
  );
}

/** A step waiting on the gate card above: what it asks, and the level. */
function AwaitingStep({ event, tier }: { event: Event; tier: DecisionTier | undefined }): JSX.Element {
  const { t, i18n } = useLingui();
  const words = stepWords(event);
  const command = argSummary(event);
  const tool = event.toolName ?? t`Permission`;
  const waited = compactSpan(Date.now() - new Date(event.ts).getTime());
  const badge = tier === undefined ? undefined : TIER_BADGE[tier];
  return (
    <div className={STEP}>
      <StateGlyph state="needs" size={12} className="mt-px" />
      <div className="min-w-0">
        <div className="flex min-w-0 items-baseline gap-2">
          <span className="text-[13px] leading-[normal] font-medium text-ink">{words ?? t`${tool} · awaiting approval`}</span>
          {words !== null && <span className="font-mono text-[10.5px] text-ink-faint">{tool}</span>}
        </div>
        {command !== null && command !== words && <div className={STEP_DETAIL}>{command}</div>}
        {badge !== undefined && (
          <span className={`mt-1 inline-block px-1.5 font-mono text-[10px] ${badge.className}`}>
            {i18n._(badge.label)}
          </span>
        )}
      </div>
      <span className={STEP_DURATION}>{t`waiting ${waited}`}</span>
    </div>
  );
}

// Exported for the Chat tab's expandable tool groups — same rows, same nesting.
export function Nodes({
  nodes,
  activeEventId = null,
  gateTier,
}: {
  nodes: TimelineNode[];
  /** The step in progress on a live session, whose glyph is the working one. */
  activeEventId?: number | null;
  gateTier?: DecisionTier | undefined;
}): JSX.Element {
  return (
    <>
      {nodes.map((node) =>
        node.kind === 'subagent' ? (
          <SubagentBlock key={`sub-${node.start.id}`} node={node} />
        ) : node.event.type === 'user_prompt' ? (
          <Prompt key={node.event.id} event={node.event} />
        ) : isWaitingEvent(node.event) ? (
          <AwaitingStep key={node.event.id} event={node.event} tier={gateTier} />
        ) : (
          <EventRow key={node.event.id} event={node.event} active={node.event.id === activeEventId} />
        ),
      )}
    </>
  );
}

export function Timeline({
  detail,
  live = false,
  gateTier,
}: {
  detail: SessionDetail;
  /** A working session: its newest step is the one in progress. */
  live?: boolean;
  gateTier?: DecisionTier | undefined;
}): JSX.Element {
  const groups = useMemo(() => buildTimeline(detail), [detail]);
  const lastEvent = detail.events[detail.events.length - 1];

  if (groups.length === 0) {
    return (
      <Empty>
        <Trans>no events in this session yet</Trans>
      </Empty>
    );
  }
  return (
    <div className="mt-[14px] flex flex-col">
      {groups.map((group) => (
        <section key={group.turn !== null ? `turn-${group.turn.id}` : 'orphans'}>
          <Nodes nodes={group.nodes} activeEventId={live ? (lastEvent?.id ?? null) : null} gateTier={gateTier} />
        </section>
      ))}
    </div>
  );
}
