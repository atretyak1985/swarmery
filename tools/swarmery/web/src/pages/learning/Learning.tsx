// Learning (v4, handoff §3, §9): one place replacing /lessons and /decisions, in
// the frame every place shares. The accordion groups are Lessons · Proof · The
// classifier, and honesty lives inside The classifier as its own row. `?tab=`
// keeps naming the old four tabs, so the retired routes still land on a group.
// Lessons, decisions and calibration take no project, so /p/:slug/learning shows
// the same fleet data; the Inbox links and the Proof rows follow the scope.

import { plural, t as tr } from '@lingui/core/macro';
import { useLingui } from '@lingui/react/macro';
import { useCallback, useEffect, useState } from 'react';
import { useParams } from 'react-router-dom';
import { fetchProjectRecommendations, fetchRecommendations } from '../../api';
import { type Lesson, fetchLessons } from '../../api/lessons';
import type { Recommendation } from '../../api/types';
import { PageDetail, PagePlace, type PageTone, type PlaceGroup, type PlaceItem, usePlaceParams } from '../../components/place/PageDetail';
import { fmtAgo } from '../../lib/format';
import { PLACES } from '../../lib/nav';
import { LessonDetail, Lessons, lessonStanding } from '../Lessons';
import { CalibrationPanel } from '../LessonsCalibration';
import { RecDetail } from '../health/HealthOverview';
import { Classifier } from './Classifier';
import { Proof } from './Proof';
import { proofVerdict, type ProofVerdict } from './proofVerdict';

export const LEARNING_TABS = ['lessons', 'classifier', 'honesty', 'proof'] as const;
export type LearningTab = (typeof LEARNING_TABS)[number];

/** Which recommendations are proof: the ones you took, by how they turned out. */
const PROOF_RECS = ['verified', 'accepted', 'adopted'] as const;

const VERDICT_TONE: Record<ProofVerdict, PageTone> = { proven: 'green', gathering: 'brand', none: 'red' };
const VERDICT_GLYPH: Record<ProofVerdict, string> = { proven: '✓', gathering: '✽', none: '✗' };

function verdictWord(verdict: ProofVerdict): string {
  if (verdict === 'proven') return tr`proven`;
  return verdict === 'gathering' ? tr`gathering` : tr`no effect`;
}

function lessonItem(lesson: Lesson): PlaceItem {
  const eff = lesson.effectiveness ?? null;
  const uses = eff?.uses ?? 0;
  const before = eff?.medianBefore?.toFixed(2) ?? null;
  const after = eff?.medianAfter?.toFixed(2) ?? null;
  const active = lesson.status === 'active';
  const status = lessonStanding(lesson);
  const reached = plural(uses, {
    one: 'reached # run',
    few: 'reached # runs',
    many: 'reached # runs',
    other: 'reached # runs',
  });
  return {
    key: `lesson:${String(lesson.id)}`,
    glyph: active ? '●' : '○',
    glyphSmall: true,
    glyphTone: active ? 'purple' : 'faint',
    title: lesson.title,
    ...(lesson.planId !== '' ? { tag: lesson.planId } : {}),
    summary: uses > 0 ? `${status} · ${reached}` : status,
    ...(before !== null && after !== null
      ? { right: `${before}→${after}`, rightTone: (eff?.medianDrop ?? 0) > 0 ? ('green' as const) : ('faint' as const) }
      : active && uses > 0
        ? { right: tr`gathering` }
        : {}),
  };
}

function proofItem(rec: Recommendation): PlaceItem {
  const verdict = proofVerdict(rec);
  const rule = rec.rule;
  const target = rec.target;
  return {
    key: `proof:${String(rec.id)}`,
    glyph: VERDICT_GLYPH[verdict],
    glyphTone: VERDICT_TONE[verdict],
    title: rec.title,
    summary: `${rule} · ${target}`,
    age: fmtAgo(rec.updated_at),
    right: verdictWord(verdict),
    rightTone: VERDICT_TONE[verdict],
  };
}

function groupOf(tab: LearningTab): string {
  return tab === 'honesty' ? 'classifier' : tab;
}

function inboxHref(slug: string | null): string {
  return PLACES.find((p) => p.id === 'inbox')?.href(slug) ?? '/inbox';
}

export function Learning(): JSX.Element {
  const { t } = useLingui();
  const { slug } = useParams<{ slug: string }>();
  const projectSlug = slug ?? null;
  const { tab, item, pick } = usePlaceParams<LearningTab>(LEARNING_TABS, 'lessons');
  const [lessons, setLessons] = useState<Lesson[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [lessonError, setLessonError] = useState<string | null>(null);
  const [recs, setRecs] = useState<Recommendation[] | null>(null);

  const loadLessons = useCallback((): void => {
    fetchLessons()
      .then(setLessons)
      .catch(() => setLessons(null));
  }, []);
  useEffect(loadLessons, [loadLessons]);

  useEffect(() => {
    const status = PROOF_RECS.join(',');
    (projectSlug === null ? fetchRecommendations(status) : fetchProjectRecommendations(projectSlug, status))
      .then((r) => setRecs(r.recommendations))
      .catch(() => setRecs(null));
  }, [projectSlug]);

  const act = useCallback(
    (run: () => Promise<Lesson>): void => {
      setBusy(true);
      setLessonError(null);
      run()
        .then(loadLessons)
        .catch((e: unknown) => setLessonError(String(e)))
        .finally(() => setBusy(false));
    },
    [loadLessons],
  );

  const inbox = inboxHref(projectSlug);
  const live = (lessons ?? []).filter((l) => l.status === 'active' || l.status === 'candidate');
  const ordered = [...live.filter((l) => l.status === 'active'), ...live.filter((l) => l.status === 'candidate')];
  const inForce = live.filter((l) => l.status === 'active').length;
  const candidates = live.length - inForce;
  const proof = (recs ?? []).filter((r) => (PROOF_RECS as readonly string[]).includes(r.status));
  const provingOut = proof.filter((r) => r.status !== 'verified').length;
  const lessonsInForce = plural(inForce, {
    one: '# lesson in force',
    few: '# lessons in force',
    many: '# lessons in force',
    other: '# lessons in force',
  });
  const changesProving = plural(provingOut, {
    one: '# change proving out',
    few: '# changes proving out',
    many: '# changes proving out',
    other: '# changes proving out',
  });

  const groups: PlaceGroup[] = [
    {
      key: 'lessons',
      label: t`Lessons`,
      tone: 'purple',
      count: ordered.length,
      items: [
        ...ordered.map(lessonItem),
        {
          key: 'queue',
          glyph: '▤',
          title: t`Review queue`,
          summary:
            candidates > 0
              ? plural(candidates, {
                  one: '# candidate · retirements · every status',
                  few: '# candidates · retirements · every status',
                  many: '# candidates · retirements · every status',
                  other: '# candidates · retirements · every status',
                })
              : t`retirements · every status`,
        },
      ],
    },
    {
      key: 'proof',
      label: t`Proof`,
      tone: 'green',
      count: proof.length,
      items: [
        ...proof.map(proofItem),
        { key: 'because', glyph: '◇', title: t`Because of you`, summary: t`what a proof row is and when it is verified` },
      ],
    },
    {
      key: 'classifier',
      label: t`Classifier`,
      tone: 'green',
      items: [
        { key: 'calibration', glyph: '◐', title: t`Calibration`, summary: t`what the model labels · how often you agree` },
        { key: 'honesty', glyph: '≈', title: t`Forecast honesty`, summary: t`how close forecasts land` },
      ],
    },
  ];

  const group = groupOf(tab);
  const open = groups.find((g) => g.key === group) ?? groups[0];
  const selected = tab === 'honesty' ? 'honesty' : (open?.items.find((i) => i.key === item)?.key ?? open?.items[0]?.key ?? null);

  const detail = (): JSX.Element => {
    if (group === 'lessons') {
      const lesson = ordered.find((l) => `lesson:${String(l.id)}` === selected);
      if (lesson !== undefined) {
        return (
          <>
            {lessonError !== null && (
              <div role="alert" className="px-7 pt-3 text-[12px] text-red">
                {lessonError}
              </div>
            )}
            <LessonDetail key={lesson.id} lesson={lesson} busy={busy} act={act} />
          </>
        );
      }
      return (
        <PageDetail kind={t`lessons`} kindTone="purple" title={t`Every lesson, and what waits for a decision.`}>
          <div className="mt-2">
            <Lessons embedded onChanged={loadLessons} />
          </div>
        </PageDetail>
      );
    }
    if (group === 'proof') {
      const rec = proof.find((r) => `proof:${String(r.id)}` === selected);
      if (rec !== undefined) {
        const verdict = proofVerdict(rec);
        const word = verdictWord(verdict);
        return <RecDetail rec={rec} kind={t`proof · ${word}`} kindTone={VERDICT_TONE[verdict]} inboxHref={inbox} />;
      }
      return <Proof inboxHref={inbox} />;
    }
    return selected === 'honesty' ? (
      <PageDetail kind={t`learning · forecast honesty`} title={t`Forecast honesty`}>
        <CalibrationPanel />
      </PageDetail>
    ) : (
      <PageDetail kind={t`learning · classifier`} kindTone="green" title={t`What the local model labels, and how it does.`}>
        <div className="mt-3">
          <Classifier inboxHref={`${inbox}?tab=classifier`} />
        </div>
      </PageDetail>
    );
  };

  return (
    <PagePlace
      tabbed
      title={t`Learning`}
      sub={`${lessonsInForce} · ${changesProving}`}
      searchHint={t`filter lessons`}
      groups={groups}
      openGroup={group}
      selected={selected}
      onOpenGroup={(key) => pick(key as LearningTab)}
      onSelect={(key, rowGroup) => {
        if (rowGroup === 'classifier') {
          pick(key === 'honesty' ? 'honesty' : 'classifier');
          return;
        }
        const first = groups.find((g) => g.key === rowGroup)?.items[0]?.key;
        pick(rowGroup as LearningTab, key === first ? null : key);
      }}
      detail={detail()}
    />
  );
}
