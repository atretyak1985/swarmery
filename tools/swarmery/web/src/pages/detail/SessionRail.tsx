// The context rail beside the open session (Swarmery v4): the handoff brief when
// there is one, then who is working, what this session belongs to, the account, what
// it touched and the lessons that apply there. Real data only (D-5): no PR row, and
// lessons are what applies to these areas, because nothing records which lesson a
// session actually received.
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
  accountWindow,
  isHeavy,
  isHotHandoff,
  linkedRows,
  mapLimited,
  MAX_TOUCHED_AREAS,
  swarmRows,
  touchedAreas,
} from '../../lib/sessionRail';
import type { RowState } from '../../lib/sessionsView';
import { useUsage } from '../../lib/usageData';
import { HandoffCard } from './HandoffCard';

const MAX_LESSONS = 3;
/** Lessons are asked for per area, every area, and a daemon answers on one DB line. */
const LESSON_REQUESTS = 4;

function Block({ label, count, children }: { label: string; count?: number; children: ReactNode }): JSX.Element {
  return (
    <div className="border-t border-line pt-2.5">
      <div className="flex items-baseline gap-1.5 font-mono text-[10px] leading-[normal] tracking-[0.12em] text-ink-faint uppercase">
        {label}
        {count !== undefined && <span className="tracking-normal">{count}</span>}
      </div>
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
  to,
}: {
  glyph?: ReactNode;
  text: string;
  sub?: string;
  right?: string | undefined;
  rightTone?: string;
  tone?: string;
  to?: string | undefined;
}): JSX.Element {
  const body = (
    <>
      {glyph}
      <div className="min-w-0">
        <div className={`truncate text-[12.5px] leading-[normal] ${tone}`}>{text}</div>
        {sub !== undefined && sub !== '' && (
          <div className="mt-px truncate font-mono text-[10.5px] leading-[normal] text-ink-faint">{sub}</div>
        )}
      </div>
      {right !== undefined && (
        <span className={`ml-auto font-mono text-[10.5px] leading-[normal] whitespace-nowrap ${rightTone}`}>{right}</span>
      )}
    </>
  );
  return to === undefined ? (
    <div className="flex items-start gap-2">{body}</div>
  ) : (
    <Link to={to} className="flex items-start gap-2 transition-opacity hover:opacity-80">
      {body}
    </Link>
  );
}

function PlanGlyph(): JSX.Element {
  return (
    <span aria-hidden="true" className="shrink-0 font-mono text-[11px] leading-[normal] text-brand">
      ⧉
    </span>
  );
}

function SwarmBlock({ detail, state, nowMs }: { detail: SessionDetail; state: RowState; nowMs: number }): JSX.Element {
  const { t } = useLingui();
  const swarm = swarmRows(detail, state, nowMs);
  return (
    <Block label={t`swarm`} count={swarm.total}>
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

function LinkedBlock({ detail }: { detail: SessionDetail }): JSX.Element {
  const { t } = useLingui();
  const { slug } = useParams<{ slug?: string }>();
  const rows = linkedRows(detail, slug ?? detail.projectSlug ?? '');
  return (
    <Block label={t`linked`}>
      {rows.length === 0 ? (
        <Row text={t`no plan`} sub={t`ad-hoc session`} tone="text-ink-faint" />
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
  const sub = [percent === null ? '' : t`plan ${percent} %`, reset].filter((part) => part !== '').join(' · ');
  return (
    <Block label={t`account`}>
      <Row text={key} sub={sub} right={isHeavy(costUsd) ? t`heavy` : undefined} rightTone="text-brand" />
    </Block>
  );
}

function TouchesBlock({ detail }: { detail: SessionDetail }): JSX.Element {
  const { t } = useLingui();
  const touched = touchedAreas(detail.fileChanges, detail.cwd);
  return (
    <Block label={t`touches`} count={touched.total}>
      {touched.areas.length === 0 ? (
        <Row text={t`nothing touched yet`} tone="text-ink-faint" />
      ) : (
        touched.areas.slice(0, MAX_TOUCHED_AREAS).map(({ area, files }) => (
          <Row
            key={area}
            glyph={
              <span aria-hidden="true" className="mt-[3px] shrink-0 font-mono text-[9px] leading-[normal] text-blue">
                ▪
              </span>
            }
            text={area}
            right={plural(files, { one: '# file', few: '# files', many: '# files', other: '# files' })}
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
    <Block label={t`lessons`}>
      {lessons === 'failed' && <Row text={t`lessons unavailable`} tone="text-ink-faint" />}
      {lessons !== 'failed' && lessons !== 'loading' && lessons.length === 0 && (
        <Row text={t`none matched`} tone="text-ink-faint" />
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
}: {
  detail: SessionDetail;
  state: RowState;
  nowMs: number;
  /** The session's cost as the numbers strip sums it. */
  costUsd: number | null;
}): JSX.Element {
  const { t } = useLingui();
  const { hash } = useLocation();
  const { rail: collapsed } = usePanels();
  const toggleLabel = collapsed ? t`expand context · \\` : t`collapse context · \\`;
  const hasHandoff = detail.handoff != null;

  // A link to the brief has to land on the brief, not on a collapsed strip.
  useEffect(() => {
    if (hash === '#handoff' && hasHandoff) setPanel('rail', false);
  }, [hash, hasHandoff]);

  return (
    <aside
      aria-label={t`session context`}
      className={`flex min-w-0 flex-[1_1_220px] flex-col gap-3.5 pt-2.5 desk:sticky desk:top-0 ${
        collapsed ? 'desk:flex-[0_0_28px]' : ''
      }`}
    >
      <button
        type="button"
        onClick={() => togglePanel('rail')}
        aria-label={toggleLabel}
        data-tip={toggleLabel}
        className="hidden h-5 w-[22px] self-end rounded-[5px] border border-transparent p-0 font-mono text-[11px] leading-[normal] text-ink-faint transition-colors hover:border-line-strong hover:text-ink desk:block"
      >
        {collapsed ? '◂' : '▸'}
      </button>
      <div className={collapsed ? 'contents desk:hidden' : 'contents'}>
        {detail.handoff != null && (
          <HandoffCard
            sessionId={detail.id}
            handoff={detail.handoff}
            hot={isHotHandoff(detail)}
            nowMs={nowMs}
          />
        )}
        <SwarmBlock detail={detail} state={state} nowMs={nowMs} />
        <LinkedBlock detail={detail} />
        <AccountBlock detail={detail} costUsd={costUsd} />
        <TouchesBlock detail={detail} />
        <LessonsBlock detail={detail} />
      </div>
      {collapsed && (
        // The whole strip expands, as in the design. The button above is the one a
        // keyboard and a screen reader get.
        <button
          type="button"
          tabIndex={-1}
          aria-hidden="true"
          onClick={() => togglePanel('rail')}
          className="hidden flex-col items-center gap-2.5 desk:flex"
        >
          {hasHandoff && (
            <span className={`font-mono text-[11px] leading-[normal] ${isHotHandoff(detail) ? 'text-red' : 'text-brand'}`}>⇢</span>
          )}
          <span className="rotate-180 font-mono text-[10px] leading-[normal] tracking-[0.12em] text-ink-faint uppercase [writing-mode:vertical-rl]">
            <Trans>context</Trans>
          </span>
        </button>
      )}
    </aside>
  );
}
