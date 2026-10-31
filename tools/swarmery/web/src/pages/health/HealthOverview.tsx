// Health's detail pages (handoff §9): the one sentence about the fleet with its
// failed share by agent, the friction the operator can remove right now, and one
// page per friction, Advisor finding and agent. All of them are blocks of
// PageDetail; the boards the blocks cannot express stay on the group's first row.

import { plural, t } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { useCallback, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { createApprovalRule } from '../../api';
import type {
  HealthAutoMode,
  Recommendation,
  RetroAgentRow,
  RetroAgentsResp,
  RetroDeniedTool,
  RetroErrorGroup,
  RetroFrictionResp,
} from '../../api/types';
import { ImproveModal } from '../../components/ImproveModal';
import {
  PageDetail,
  type PageAction,
  type PageBar,
  type PageKv,
  type PageRow,
  type PageTone,
} from '../../components/place/PageDetail';
import type { NumberItem } from '../../components/place/NumbersStrip';
import { ErrorBox, Loading } from '../../components/ui';
import { fmtAgo, fmtDurationMs } from '../../lib/format';
import { HOT_FAILED_PCT, agentMove, agentsByFailure } from './healthPlace';
import {
  type AutoModeTone,
  autoModeRow,
  fmtPct,
  oneSentence,
  uncoveredDenied,
  untriagedErrors,
  windowCell,
} from './healthModel';

const AUTO_MODE_TONE: Record<AutoModeTone, string> = {
  quiet: 'text-ink-faint',
  seen: 'text-amber',
  alerting: 'text-red',
};

/**
 * Whether Claude Code's server-side auto mode permission check is answering — a
 * fact about the machine, not about the range or the project. The state is
 * carried by the words, the colour only repeats it.
 */
function AutoModeRow({ mode, inboxHref }: { mode: HealthAutoMode; inboxHref: string }): JSX.Element {
  const { t } = useLingui();
  const row = autoModeRow(mode, mode.lastAt !== null ? fmtAgo(mode.lastAt) : null);
  return (
    <div
      role="status"
      aria-label={t`Auto mode permission check`}
      className="mt-[18px] flex flex-wrap items-baseline gap-x-3 gap-y-1 border-b border-line py-2"
    >
      <span className="font-mono text-[10px] tracking-[0.12em] text-ink-faint uppercase">
        <Trans>Auto mode permission check</Trans>
      </span>
      <span className={`font-mono text-[11px] ${AUTO_MODE_TONE[row.tone]}`}>{row.text}</span>
      {mode.alerting && (
        <Link to={inboxHref} className="font-mono text-[11px] font-semibold text-red hover:underline">
          <Trans>alerting → Inbox</Trans>
        </Link>
      )}
    </div>
  );
}

function sameErrorLine(count: number): string {
  return t`Same error ${plural(count, { one: '# time', few: '# times', many: '# times', other: '# times' })} in this window`;
}

function lastSeenLine(ago: string, sessions: number): string {
  if (sessions === 0) return t`Last seen ${ago}.`;
  return t`Last seen ${ago} · in ${plural(sessions, { one: '# session', few: '# sessions', many: '# sessions', other: '# sessions' })}.`;
}

/** The design's title: "142 runs in 30 days, 11 % failed — down from 19 %." */
function fleetTitle(agents: RetroAgentsResp, days: number): string {
  const cell = windowCell(agents);
  const runs = cell.runs;
  const head = plural(runs, { one: '# run', few: '# runs', many: '# runs', other: '# runs' });
  const window = plural(days, { one: '# day', few: '# days', many: '# days', other: '# days' });
  const failed = fmtPct(cell.failedPct);
  const prev = cell.prevFailedPct;
  if (prev === null || Math.round(prev) === Math.round(cell.failedPct)) return t`${head} in ${window}, ${failed} failed.`;
  const was = fmtPct(prev);
  return cell.failedPct < prev
    ? t`${head} in ${window}, ${failed} failed — down from ${was}.`
    : t`${head} in ${window}, ${failed} failed — up from ${was}.`;
}

/** The one sentence: which way the rate moved and whose runs moved it. */
function fleetLead(agents: RetroAgentsResp): string {
  const hot = agents.agents.filter((row) => row.error_rate * 100 >= HOT_FAILED_PCT).length;
  const line = String(HOT_FAILED_PCT);
  const sentence = oneSentence(agents);
  return hot > 0
    ? `${sentence} ${plural(hot, {
        one: `# agent still sits above the ${line} % line.`,
        few: `# agents still sit above the ${line} % line.`,
        many: `# agents still sit above the ${line} % line.`,
        other: `# agents still sit above the ${line} % line.`,
      })}`
    : sentence;
}

function fleetStats(agents: RetroAgentsResp): NumberItem[] {
  const cell = windowCell(agents);
  const prev = cell.prevFailedPct;
  const tone = prev === null ? 'default' : cell.failedPct < prev ? 'green' : cell.failedPct > prev ? 'red' : 'default';
  return [
    { label: t`runs`, value: String(cell.runs) },
    { label: t`failed`, value: fmtPct(cell.failedPct), tone },
    ...(prev !== null ? [{ label: t`was`, value: fmtPct(prev), tone: 'faint' as const }] : []),
    { label: t`spent`, value: `$${String(Math.round(cell.costUsd))}`, tone: 'brand' },
  ];
}

function failedBars(agents: RetroAgentsResp): PageBar[] {
  return agentsByFailure(agents).map((row) => {
    const now = row.error_rate * 100;
    return {
      label: row.agent,
      percent: now,
      value: fmtPct(now),
      tone: now >= HOT_FAILED_PCT ? 'brand' : 'green',
    };
  });
}

export function FleetDetail({
  agents,
  error,
  onRetry,
  scopeLabel,
  days,
  decisions,
  autoMode,
  alertsHref,
}: {
  agents: RetroAgentsResp | null;
  error: string | null;
  onRetry: () => void;
  scopeLabel: string;
  days: number;
  decisions: readonly PageRow[];
  /** Absent while the daemon is unreachable or older than the field: no row. */
  autoMode: HealthAutoMode | undefined;
  alertsHref: string;
}): JSX.Element {
  const { t } = useLingui();
  if (error !== null) {
    return (
      <div className="px-7 py-5">
        <ErrorBox message={error} onRetry={onRetry} />
      </div>
    );
  }
  if (agents === null) return <Loading label={t`health…`} />;
  return (
    <PageDetail
      kind={t`health · fleet`}
      kindTone="brand"
      meta={`${scopeLabel} · ${String(days)} d`}
      title={fleetTitle(agents, days)}
      lead={fleetLead(agents)}
      stats={fleetStats(agents)}
      barsTitle={t`failed share by agent`}
      bars={failedBars(agents)}
      rowsTitle={t`decisions`}
      rows={decisions}
    >
      {autoMode !== undefined && <AutoModeRow mode={autoMode} inboxHref={alertsHref} />}
    </PageDetail>
  );
}

type AllowState = 'idle' | 'busy' | 'done';

/** "+ always allow": a fleet-wide rule for a tool no rule covers. */
function useAlwaysAllow(tool: string): { state: AllowState; failed: string | null; allow: () => void } {
  const [state, setState] = useState<AllowState>('idle');
  const [failed, setFailed] = useState<string | null>(null);
  const allow = useCallback((): void => {
    if (state !== 'idle') return;
    setState('busy');
    setFailed(null);
    // i18n-ignore: persisted rule note sent to the API, not UI copy
    createApprovalRule({ projectId: null, toolPattern: tool, note: 'created from Health overview' })
      .then(() => setState('done'))
      .catch((e: unknown) => {
        setFailed(String(e));
        setState('idle');
      });
  }, [state, tool]);
  return { state, failed, allow };
}

function allowAction(tool: string, state: AllowState, allow: () => void): PageAction {
  if (state === 'done') return { label: t`✓ always allowed`, tone: 'green', disabled: true };
  return {
    label: state === 'busy' ? '…' : t`always allow ${tool}`,
    tone: 'brand',
    onClick: allow,
    disabled: state === 'busy',
    tip: t`auto-approve every ${tool} request, in every project`,
  };
}

function deniedRow(denied: RetroDeniedTool, onOpen: (key: string) => void): PageRow {
  const tool = denied.tool;
  const deniedCount = String(denied.denied);
  return {
    key: `denied:${tool}`,
    glyph: '▲',
    glyphTone: 'brand',
    label: tool,
    sub: plural(denied.calls, {
      one: `denied ${deniedCount}× · of # call`,
      few: `denied ${deniedCount}× · of # calls`,
      many: `denied ${deniedCount}× · of # calls`,
      other: `denied ${deniedCount}× · of # calls`,
    }),
    right: t`+ always allow`,
    rightTone: 'brand',
    onClick: () => onOpen(`denied:${tool}`),
  };
}

function errorRow(group: RetroErrorGroup, onOpen: (key: string) => void): PageRow {
  return {
    key: `error:${group.key}`,
    glyph: '✗',
    glyphTone: 'red',
    label: group.example,
    sub: sameErrorLine(group.count),
    age: fmtAgo(group.last_ts),
    onClick: () => onOpen(`error:${group.key}`),
  };
}

export function RemovableDetail({
  friction,
  onOpen,
  onBoard,
}: {
  friction: RetroFrictionResp | null;
  /** Opens a friction row by key. */
  onOpen: (key: string) => void;
  onBoard: () => void;
}): JSX.Element {
  const { t } = useLingui();
  const deniedTools = useMemo(() => (friction !== null ? uncoveredDenied(friction) : []), [friction]);
  const errors = useMemo(() => (friction !== null ? untriagedErrors(friction) : []), [friction]);
  const count = deniedTools.length + errors.length;
  const top = deniedTools[0];
  const { state, failed, allow } = useAlwaysAllow(top?.tool ?? '');
  return (
    <PageDetail
      kind={t`health · friction`}
      kindTone="brand"
      meta={t`things you can remove today`}
      title={
        friction === null
          ? t`The friction board is unavailable right now.`
          : count === 0
            ? t`Nothing to remove: no tool was denied without a rule and no error repeated in this window.`
            : plural(count, {
                one: '# friction you can remove today.',
                few: '# frictions you can remove today.',
                many: '# frictions you can remove today.',
                other: '# frictions you can remove today.',
              })
      }
      {...(count > 0 ? { lead: t`Tools you deny every time, and errors that repeat across sessions.` } : {})}
      rowsTitle={t`friction`}
      rows={[...deniedTools.map((d) => deniedRow(d, onOpen)), ...errors.map((g) => errorRow(g, onOpen))]}
      actions={top !== undefined ? [allowAction(top.tool, state, allow)] : []}
      link={{ label: t`see friction`, onClick: onBoard }}
    >
      {failed !== null && <div className="mt-1.5 font-mono text-[10.5px] text-red">{failed}</div>}
    </PageDetail>
  );
}

export function DeniedDetail({ denied, onBoard }: { denied: RetroDeniedTool; onBoard: () => void }): JSX.Element {
  const { t } = useLingui();
  const tool = denied.tool;
  const deniedCount = String(denied.denied);
  const callCount = String(denied.calls);
  const { state, failed, allow } = useAlwaysAllow(tool);
  return (
    <PageDetail
      kind={t`health · friction`}
      kindTone="brand"
      title={plural(denied.calls, {
          one: `${tool} denied ${deniedCount} of # call`,
          few: `${tool} denied ${deniedCount} of # calls`,
          many: `${tool} denied ${deniedCount} of # calls`,
          other: `${tool} denied ${deniedCount} of # calls`,
        })}
      lead={t`No approval rule covers this tool yet. Allow it once and these runs stop asking.`}
      kv={[
        { k: t`denied`, v: deniedCount },
        { k: t`calls`, v: callCount },
      ]}
      actions={[allowAction(tool, state, allow)]}
      link={{ label: t`friction board →`, onClick: onBoard }}
    >
      {failed !== null && <div className="mt-1.5 font-mono text-[10.5px] text-red">{failed}</div>}
    </PageDetail>
  );
}

export function ErrorDetail({ group, onBoard }: { group: RetroErrorGroup; onBoard: () => void }): JSX.Element {
  const { t } = useLingui();
  return (
    <PageDetail
      kind={t`health · friction`}
      kindTone="red"
      meta={sameErrorLine(group.count)}
      title={group.example}
      lead={lastSeenLine(fmtAgo(group.last_ts), group.sessions.length)}
      kv={[
        { k: t`times`, v: String(group.count) },
        { k: t`sessions`, v: String(group.sessions.length) },
        { k: t`last seen`, v: fmtAgo(group.last_ts) },
      ]}
      link={{ label: t`friction board →`, onClick: onBoard }}
    />
  );
}

/** One recommendation as a page: Health's Advisor group and Learning's Proof. */
export function RecDetail({
  rec,
  kind,
  kindTone = 'blue',
  inboxHref,
}: {
  rec: Recommendation;
  kind: string;
  kindTone?: PageTone;
  inboxHref: string;
}): JSX.Element {
  const { t } = useLingui();
  const rule = rec.rule;
  const target = rec.target;
  const kv: PageKv[] = [
    { k: t`rule`, v: rule },
    { k: t`target`, v: target },
    { k: t`raised`, v: fmtAgo(rec.created_at) },
  ];
  if (rec.baseline !== null) kv.push({ k: rec.baseline.metric, v: String(rec.baseline.value) });
  return (
    <PageDetail
      kind={kind}
      kindTone={kindTone}
      meta={`${rule} · ${target}`}
      title={rec.title}
      lead={rec.detail}
      kv={kv}
      link={{ label: t`decide in Inbox →`, to: inboxHref }}
    />
  );
}

export function AgentDetail({
  row,
  systemHref,
  onAll,
}: {
  row: RetroAgentRow;
  systemHref: string;
  onAll: () => void;
}): JSX.Element {
  const { t } = useLingui();
  const [improving, setImproving] = useState(false);
  const { now, prev, drop } = agentMove(row);
  const hot = now >= HOT_FAILED_PCT;
  const agent = row.agent;
  const failed = fmtPct(now);
  const runs = String(row.runs);
  const was = prev !== null ? fmtPct(prev) : '';
  const tone: PageTone = hot ? 'brand' : 'green';
  const kv: PageKv[] = [
    { k: t`sessions`, v: String(row.sessions) },
    { k: t`errors`, v: String(row.errors) },
  ];
  if (row.avg_ms !== null) kv.push({ k: t`average run`, v: fmtDurationMs(row.avg_ms) });
  if (row.p95_ms !== null) kv.push({ k: t`p95 run`, v: fmtDurationMs(row.p95_ms) });
  if (row.success_rate !== null) kv.push({ k: t`judged success`, v: fmtPct(row.success_rate * 100) });
  if (row.re_dispatch_rate !== null) kv.push({ k: t`re-dispatched`, v: fmtPct(row.re_dispatch_rate * 100) });
  return (
    <>
      <PageDetail
        kind={t`health · agent`}
        kindTone={tone}
        title={
          drop !== null && drop >= 2
            ? plural(row.runs, {
              one: `${agent}: ${failed} of # run failed, down from ${was}.`,
              few: `${agent}: ${failed} of # runs failed, down from ${was}.`,
              many: `${agent}: ${failed} of # runs failed, down from ${was}.`,
              other: `${agent}: ${failed} of # runs failed, down from ${was}.`,
            })
            : plural(row.runs, {
              one: `${agent}: ${failed} of # run failed.`,
              few: `${agent}: ${failed} of # runs failed.`,
              many: `${agent}: ${failed} of # runs failed.`,
              other: `${agent}: ${failed} of # runs failed.`,
            })
        }
        stats={[
          { label: t`runs`, value: runs },
          { label: t`failed`, value: failed, tone },
          ...(prev !== null ? [{ label: t`before`, value: was, tone: 'faint' as const }] : []),
          { label: t`spent`, value: `$${String(Math.round(row.cost_usd))}`, tone: 'brand' },
        ]}
        kv={kv}
        actions={[
          { label: t`open in System`, to: systemHref },
          { label: t`propose a change`, tone: 'brand', onClick: () => setImproving(true) },
        ]}
        link={{ label: t`all agents →`, onClick: onAll }}
      />
      {improving && <ImproveModal row={row} onClose={() => setImproving(false)} onGenerated={() => undefined} />}
    </>
  );
}
