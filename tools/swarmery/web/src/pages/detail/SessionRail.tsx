// The context rail beside the open session (Swarmery v4): one card, the handoff brief
// first when there is one, then who is working, what this session belongs to, the
// account, what it touched and the lessons that apply there. Each block links to where
// its detail lives. Real data only (D-5): no PR row, no pushed state, and lessons are
// what applies to these areas, because nothing records which lesson a session received.
//
// It collapses to a narrow strip with `\` or the button, from `desk` up only: below
// it the rail wraps under the conversation, whatever the stored state says (D-3). So
// the blocks stay mounted behind `desk:hidden`, as the list does.

import { plural } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { Link, useLocation, useParams } from 'react-router-dom';
import { fetchLessons, type Lesson } from '../../api/lessons';
import type { SessionDetail } from '../../api/types';
import { StateGlyph } from '../../components/place/StateGlyph';
import { fmtResetAt } from '../../components/usage/format';
import { setPanel, togglePanel, usePanels } from '../../lib/panels';
import { accountKey } from '../../lib/sessionAccount';
import {
  type AreaRisk,
  accountWindow,
  isHeavy,
  isHotHandoff,
  linkedRows,
  mapLimited,
  MAX_TOUCHED_AREAS,
  swarmRows,
  touchedAreas,
} from '../../lib/sessionRail';
import { fmtCost } from '../../lib/format';
import { plansTaskHref } from '../plans/plansUrl';
import type { RowState } from '../../lib/sessionsView';
import { useUsage } from '../../lib/usageData';
import { HandoffCard } from './HandoffCard';

const MAX_LESSONS = 3;
/** Lessons are asked for per area, every area, and a daemon answers on one DB line. */
const LESSON_REQUESTS = 4;

const BLOCK_LINK = 'ml-auto bg-transparent p-0 tracking-normal normal-case text-ink-faint transition-colors hover:text-brand';

/** Where a block's detail lives: a route, or a tab of the session itself. */
type BlockLink = { label: string; to: string } | { label: string; onClick: () => void };

function Block({
  label,
  count,
  link,
  barPercent,
  children,
}: {
  label: string;
  count?: number;
  link?: BlockLink | undefined;
  barPercent?: number | null | undefined;
  children: ReactNode;
}): JSX.Element {
  return (
    <div className="border-b border-line px-3.5 pt-2.5 pb-3">
      <div className="flex items-baseline gap-1.5 font-mono text-[10px] leading-[normal] tracking-[0.12em] text-ink-3 uppercase">
        {label}
        {count !== undefined && <span className="tracking-normal">{count}</span>}
        {link !== undefined &&
          ('to' in link ? (
            <Link to={link.to} className={BLOCK_LINK}>
              {link.label}
            </Link>
          ) : (
            <button type="button" onClick={link.onClick} className={BLOCK_LINK}>
              {link.label}
            </button>
          ))}
      </div>
      {barPercent !== undefined && barPercent !== null && (
        <div aria-hidden="true" className="mt-[7px] h-1 overflow-hidden rounded-[2px] bg-line">
          <div className="h-full bg-brand" style={{ width: `${String(Math.min(100, Math.max(0, barPercent)))}%` }} />
        </div>
      )}
      <div className="mt-1.5 flex flex-col gap-[5px]">{children}</div>
    </div>
  );
}

function Row({
  glyph,
  text,
  sub,
  right,
  rightTone = 'text-ink-faint',
  tone = 'text-ink-2',
  subTone = 'text-ink-faint',
  to,
  onClick,
}: {
  glyph?: ReactNode;
  text: string;
  sub?: string;
  right?: string | undefined;
  rightTone?: string;
  tone?: string;
  subTone?: string;
  to?: string | undefined;
  onClick?: (() => void) | undefined;
}): JSX.Element {
  const body = (
    <>
      {glyph}
      <div className="min-w-0 flex-1">
        <div className={`truncate text-[12.5px] leading-[normal] ${tone}`}>{text}</div>
        {sub !== undefined && sub !== '' && (
          <div className={`mt-px truncate font-mono text-[10.5px] leading-[normal] ${subTone}`}>{sub}</div>
        )}
      </div>
      {right !== undefined && (
        <span className={`ml-auto font-mono text-[10.5px] leading-[normal] whitespace-nowrap ${rightTone}`}>{right}</span>
      )}
    </>
  );
  if (to !== undefined) {
    return (
      <Link to={to} className="flex items-start gap-2 transition-opacity hover:opacity-80">
        {body}
      </Link>
    );
  }
  if (onClick !== undefined) {
    return (
      <button
        type="button"
        onClick={onClick}
        className="flex w-full items-start gap-2 bg-transparent p-0 text-left transition-opacity hover:opacity-80"
      >
        {body}
      </button>
    );
  }
  return <div className="flex items-start gap-2">{body}</div>;
}

function PlanGlyph(): JSX.Element {
  return (
    <span aria-hidden="true" className="shrink-0 font-mono text-[11px] leading-[normal] text-brand">
      ⧉
    </span>
  );
}

function SwarmBlock({
  detail,
  state,
  nowMs,
  onShowSteps,
}: {
  detail: SessionDetail;
  state: RowState;
  nowMs: number;
  onShowSteps: () => void;
}): JSX.Element {
  const { t } = useLingui();
  const { slug } = useParams<{ slug?: string }>();
  const swarm = swarmRows(detail, state, nowMs);
  const plan = detail.planGroup;
  const phase = plan?.role === 'phase' ? plan.phaseSeq : null;
  const link: BlockLink =
    plan != null && phase !== null
      ? { label: t`phase →`, to: plansTaskHref(slug ?? detail.projectSlug ?? '', plan.taskId, { phase }) }
      : { label: t`steps →`, onClick: onShowSteps };
  return (
    <Block label={t`swarm`} count={swarm.total} link={link}>
      {swarm.rows.map((row) => (
        <Row
          key={row.key}
          glyph={<StateGlyph state={row.state} size={11} className="mt-px shrink-0" />}
          text={row.name}
          sub={row.note}
        />
      ))}
      {swarm.hidden > 0 && <Row text={t`+${swarm.hidden} earlier`} tone="text-ink-faint" />}
    </Block>
  );
}

function LinkedBlock({ detail, onShowDiff }: { detail: SessionDetail; onShowDiff: () => void }): JSX.Element {
  const { t } = useLingui();
  const { slug } = useParams<{ slug?: string }>();
  const rows = linkedRows(detail, slug ?? detail.projectSlug ?? '');
  const files = touchedAreas(detail.fileChanges, detail.cwd).total;
  return (
    <Block label={t`linked`}>
      {detail.gitBranch != null && (
        <Row
          glyph={
            <span aria-hidden="true" className="shrink-0 font-mono text-[11px] leading-[normal] text-ink-3">
              ⎇
            </span>
          }
          text={detail.gitBranch}
          sub={
            files === 0
              ? t`no changes yet`
              : plural(files, { one: '# file', few: '# files', many: '# files', other: '# files' })
          }
          onClick={onShowDiff}
        />
      )}
      {rows.length === 0 ? (
        <Row text={t`no plan · ad-hoc session`} tone="text-ink-faint" />
      ) : (
        rows.map((row) => <Row key={row.key} glyph={<PlanGlyph />} text={row.text} sub={row.sub} to={row.to} />)
      )}
    </Block>
  );
}

function AccountBlock({ detail, costUsd }: { detail: SessionDetail; costUsd: number | null }): JSX.Element {
  const { t } = useLingui();
  const { accounts, providers } = useUsage();
  const key = accountKey(detail);
  const plan = accountWindow(accounts, providers, key);
  const percent = plan === null ? null : Math.round(plan.percentUsed);
  const at = plan?.resetAt === undefined ? '' : fmtResetAt(plan.resetAt, Date.now());
  const reset = at === '' ? (plan?.resetText ?? '') : t`resets ${at}`;
  const spent = costUsd === null ? '' : fmtCost(costUsd);
  const sub = [percent === null ? '' : t`plan ${percent} %`, reset, spent === '' ? '' : t`this session ${spent}`]
    .filter((part) => part !== '')
    .join(' · ');
  return (
    <Block label={t`account`} barPercent={percent} link={{ label: t`accounts →`, to: '/settings?tab=accounts' }}>
      <Row text={key} sub={sub} right={isHeavy(costUsd) ? t`heavy` : undefined} rightTone="text-brand" />
    </Block>
  );
}

const RISK_TONE: Record<AreaRisk, string> = {
  deletes: 'text-red',
  sensitive: 'text-brand',
  new: 'text-green',
  edits: 'text-ink-faint',
};

function TouchesBlock({ detail, onShowDiff }: { detail: SessionDetail; onShowDiff: () => void }): JSX.Element {
  const { t } = useLingui();
  const touched = touchedAreas(detail.fileChanges, detail.cwd);
  const riskLabel: Record<AreaRisk, string> = {
    deletes: t`deletes`,
    sensitive: t`sensitive`,
    new: t`new files`,
    edits: t`edits`,
  };
  return (
    <Block
      label={t`touches`}
      count={touched.total}
      link={touched.total === 0 ? undefined : { label: t`diff →`, onClick: onShowDiff }}
    >
      {touched.areas.length === 0 ? (
        <Row text={t`nothing touched yet`} tone="text-ink-faint" />
      ) : (
        touched.areas.slice(0, MAX_TOUCHED_AREAS).map(({ area, files, risk }) => (
          <Row
            key={area}
            glyph={
              <span
                aria-hidden="true"
                className={`mt-[3px] shrink-0 font-mono text-[9px] leading-[normal] ${RISK_TONE[risk]}`}
              >
                ▪
              </span>
            }
            text={area}
            sub={`${plural(files, { one: '# file', few: '# files', many: '# files', other: '# files' })} · ${riskLabel[risk]}`}
            subTone={RISK_TONE[risk]}
            onClick={onShowDiff}
          />
        ))
      )}
    </Block>
  );
}

type LessonsState = readonly Lesson[] | 'loading' | 'failed';

/** The active lessons whose globs overlap the areas this session touched: the
 * injection matcher, one request per area. No area, no lessons: a session we cannot
 * place gets nothing rather than everything. */
function useAreaLessons(areas: readonly string[]): LessonsState {
  const [lessons, setLessons] = useState<LessonsState>('loading');
  const key = areas.join('\n');
  useEffect(() => {
    if (key === '') {
      setLessons([]);
      return undefined;
    }
    let live = true;
    mapLimited(key.split('\n'), LESSON_REQUESTS, (area) => fetchLessons('active', area))
      .then((found) => {
        if (!live) return;
        const byId = new Map(found.flat().map((lesson) => [lesson.id, lesson]));
        setLessons([...byId.values()]);
      })
      .catch(() => {
        if (live) setLessons('failed');
      });
    return () => {
      live = false;
    };
  }, [key]);
  return lessons;
}

function LessonsBlock({ detail }: { detail: SessionDetail }): JSX.Element {
  const { t } = useLingui();
  const { slug } = useParams<{ slug?: string }>();
  const areas = useMemo(
    () => touchedAreas(detail.fileChanges, detail.cwd).areas.map(({ area }) => area),
    [detail.fileChanges, detail.cwd],
  );
  const lessons = useAreaLessons(areas);
  const learning = slug === undefined ? '/learning' : `/p/${slug}/learning`;
  return (
    <Block label={t`lessons`} link={{ label: t`learning →`, to: learning }}>
      {lessons === 'failed' && <Row text={t`lessons unavailable`} tone="text-ink-faint" />}
      {lessons !== 'failed' && lessons !== 'loading' && lessons.length === 0 && (
        <Row text={t`none matched this session`} tone="text-ink-faint" />
      )}
      {lessons !== 'failed' &&
        lessons !== 'loading' &&
        lessons.slice(0, MAX_LESSONS).map((lesson) => (
          <Row
            key={lesson.id}
            glyph={
              <span aria-hidden="true" className="mt-1 shrink-0 font-mono text-[8px] leading-[normal] text-purple">
                ●
              </span>
            }
            text={lesson.title}
            sub={t`applies here`}
            to={`${learning}?tab=lessons&item=${encodeURIComponent(`lesson:${String(lesson.id)}`)}`}
          />
        ))}
    </Block>
  );
}

export function SessionRail({
  detail,
  state,
  nowMs,
  costUsd,
  onShowSteps,
  onShowDiff,
}: {
  detail: SessionDetail;
  state: RowState;
  nowMs: number;
  /** The session's cost as the numbers strip sums it. */
  costUsd: number | null;
  onShowSteps: () => void;
  onShowDiff: () => void;
}): JSX.Element {
  const { t } = useLingui();
  const { hash } = useLocation();
  const { rail: collapsed } = usePanels();
  const hasHandoff = detail.handoff != null;

  // A link to the brief has to land on the brief, not on a collapsed strip.
  useEffect(() => {
    if (hash === '#handoff' && hasHandoff) setPanel('rail', false);
  }, [hash, hasHandoff]);

  return (
    <aside
      aria-label={t`session context`}
      className={`mt-2.5 flex min-w-0 flex-[1_1_220px] flex-col self-start overflow-hidden rounded-[10px] border border-line bg-bg desk:sticky desk:top-0 ${
        collapsed ? 'desk:flex-[0_0_28px]' : ''
      }`}
    >
      <div className={collapsed ? 'contents desk:hidden' : 'contents'}>
        <div className="flex items-center gap-2 border-b border-line bg-surface2 px-3.5 py-2 font-mono text-[10px] leading-[normal]">
          <span className="tracking-[0.12em] text-ink-2 uppercase">
            <Trans>context</Trans>
          </span>
          <span className="min-w-0 text-ink-faint">
            · <Trans>what surrounds this session</Trans>
          </span>
          <button
            type="button"
            onClick={() => togglePanel('rail')}
            aria-label={t`collapse context · \\`}
            data-tip={t`collapse context · \\`}
            className="ml-auto hidden bg-transparent p-0 text-[11px] leading-[normal] text-ink-faint transition-colors hover:text-brand desk:block"
          >
            »
          </button>
        </div>
        {detail.handoff != null && (
          <HandoffCard
            sessionId={detail.id}
            handoff={detail.handoff}
            hot={isHotHandoff(detail)}
            nowMs={nowMs}
          />
        )}
        <SwarmBlock detail={detail} state={state} nowMs={nowMs} onShowSteps={onShowSteps} />
        <LinkedBlock detail={detail} onShowDiff={onShowDiff} />
        <AccountBlock detail={detail} costUsd={costUsd} />
        <TouchesBlock detail={detail} onShowDiff={onShowDiff} />
        <LessonsBlock detail={detail} />
      </div>
      {collapsed && (
        // The whole strip expands, as in the design.
        <button
          type="button"
          onClick={() => togglePanel('rail')}
          aria-label={t`expand context · \\`}
          data-tip={t`expand context · \\`}
          className="hidden flex-col items-center gap-2.5 py-2.5 desk:flex"
        >
          {hasHandoff && (
            <span
              aria-hidden="true"
              className={`font-mono text-[11px] leading-[normal] ${isHotHandoff(detail) ? 'text-red' : 'text-brand'}`}
            >
              ⇢
            </span>
          )}
          <span
            aria-hidden="true"
            className="rotate-180 font-mono text-[10px] leading-[normal] tracking-[0.12em] text-ink-faint uppercase [writing-mode:vertical-rl]"
          >
            <Trans>context</Trans>
          </span>
        </button>
      )}
    </aside>
  );
}
