import type { MessageDescriptor } from '@lingui/core';
// `tr` is the module-level macro (helpers outside components); components use useLingui's `t`.
import { msg, t as tr } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { PageDetail, type PageAction } from '../components/place/PageDetail';
import { useCallback, useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import {
  acceptLesson,
  confirmRetirement,
  dismissLesson,
  editLesson,
  fetchLessons,
  fetchRetirements,
  keepLesson,
  type Lesson,
  type LessonEffectiveness,
  type LessonFinding,
  type LessonMatch,
  type LessonStatus,
  mergeLesson,
  promoteLesson,
  type RetirementProposal,
  retireLesson,
} from '../api/lessons';
import { CalibrationPanel } from './LessonsCalibration';
import { plansPath, plansTaskHref } from './plans/plansUrl';

const BTN = 'rounded border border-line px-2 py-px font-mono text-[11px] text-ink-dim hover:text-ink';
const BTN_PRIMARY = 'rounded border border-brand px-2 py-px font-mono text-[11px] text-brand';

function effectivenessLabel(e: LessonEffectiveness): string {
  const beforeN = String(e.beforeN);
  const afterN = String(e.afterN);
  const minRuns = String(e.minRuns);
  const before = (e.medianBefore ?? 0).toFixed(2);
  const after = (e.medianAfter ?? 0).toFixed(2);
  const drop =
    e.medianDrop === null
      ? tr`not enough data (${beforeN}/${afterN} of ${minRuns} runs)`
      : tr`surprise ${before} → ${after}`;
  const reliedPct = e.reliedRate === null ? '' : String(Math.round(e.reliedRate * 100));
  const uses = String(e.uses);
  const relied = e.reliedRate === null ? tr`never injected` : tr`relied on ${reliedPct}% of ${uses}`;
  return `${drop} · ${relied}`;
}

const REASON_LABEL: Record<RetirementProposal['reason'], MessageDescriptor> = {
  ineffective: msg`ineffective`,
  stale: msg`stale`,
  unused_60d: msg`unused 60 days`,
  superseded: msg`superseded`,
};

/** The retirement queue (phase 16.2): proposals wait for the operator; an
 *  unanswered one is retired by the daemon on its auto-retire date. */
function RetirementQueue(): JSX.Element | null {
  const { i18n } = useLingui();
  const [items, setItems] = useState<RetirementProposal[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(() => {
    fetchRetirements()
      .then((ps) => {
        setItems(ps);
        setErr(null);
      })
      .catch((e: unknown) => setErr(String(e)));
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const decide = (run: () => Promise<RetirementProposal>): void => {
    setBusy(true);
    run()
      .then(load)
      .catch((e: unknown) => setErr(String(e)))
      .finally(() => setBusy(false));
  };

  if (err === null && (items === null || items.length === 0)) return null;
  return (
    <section className="mt-4" aria-labelledby="retirement-heading">
      <h2 id="retirement-heading" className="text-sm text-ink">
        <Trans>Proposed retirements</Trans>
      </h2>
      {err !== null && (
        <div role="alert" className="mt-2 text-[12px] text-red">
          {err}
        </div>
      )}
      <ul className="mt-2 grid gap-2">
        {(items ?? []).map((p) => {
          const autoRetires = p.autoRetireAt?.slice(0, 10) ?? null;
          return (
            <li key={p.id} className="rounded border border-line p-2 text-[12px]">
              <div className="flex items-baseline justify-between gap-3">
                <span className="text-ink">
                  L-{String(p.lessonId)} {p.title}
                </span>
                <span className="shrink-0 font-mono text-[10px] text-ink-dim">
                  {i18n._(REASON_LABEL[p.reason])}
                  {autoRetires !== null && <Trans> · auto-retires {autoRetires}</Trans>}
                </span>
              </div>
              <p className="mt-1 text-[11px] text-ink-dim">{p.detail}</p>
              <div className="mt-2 flex gap-2">
                <button
                  type="button"
                  disabled={busy}
                  className={BTN_PRIMARY}
                  onClick={() => decide(() => confirmRetirement(p.id))}
                >
                  <Trans>retire</Trans>
                </button>
                <button
                  type="button"
                  disabled={busy}
                  className={BTN}
                  onClick={() => decide(() => keepLesson(p.id))}
                >
                  <Trans>keep</Trans>
                </button>
              </div>
            </li>
          );
        })}
      </ul>
    </section>
  );
}

const FILTERS: { value: LessonStatus | undefined; label: MessageDescriptor }[] = [
  { value: 'candidate', label: msg`candidates` },
  { value: 'active', label: msg`active` },
  { value: undefined, label: msg`all` },
];

function matchLabel(m: LessonMatch): string {
  const count = String(m.count);
  const lessonId = String(m.lessonId ?? 0);
  const where = m.kind === 'retro' ? tr`retro ×${count}` : tr`lesson #${lessonId}`;
  return `${m.exact ? '= ' : '≈ '}${m.title} (${where})`;
}

/** Where a cited review finding is read: its phase's Review tab when the plan and
 *  phase are known, else the plan; null when the plan is gone. */
export function findingHref(f: LessonFinding): string | null {
  if (f.projectSlug === '') return null;
  if (f.planId !== '' && f.phaseSeq > 0) {
    return plansPath(f.projectSlug, { plan: f.planId, detail: { kind: 'phase', seq: f.phaseSeq, tab: 'review' } });
  }
  return f.taskId > 0 ? plansTaskHref(f.projectSlug, f.taskId) : null;
}

/** The evidence ids, each `finding:<id>` the API resolved shown as a link to it. */
function EvidenceList({ lesson }: { lesson: Lesson }): JSX.Element {
  const byRef = new Map(lesson.findings.map((f) => [`finding:${String(f.id)}`, f]));
  return (
    <div className="mt-1 flex flex-wrap items-baseline gap-x-1 font-mono text-[10px] text-ink-dim">
      <Trans>evidence:</Trans>
      {lesson.evidence.map((e, i) => {
        const f = byRef.get(e);
        const href = f === undefined ? null : findingHref(f);
        const sep = i > 0 ? '· ' : '';
        if (f === undefined || href === null) {
          return (
            <span key={e}>
              {sep}
              {e}
            </span>
          );
        }
        const where = f.line > 0 ? `${f.file}:${String(f.line)}` : f.file;
        return (
          <span key={e}>
            {sep}
            <Link to={href} title={`${f.planId} · ${f.phaseName} · ${where}`} className="text-brand hover:underline">
              {f.findingId} · {f.phaseName}
            </Link>
          </span>
        );
      })}
    </div>
  );
}

function EditForm({
  lesson,
  busy,
  onSave,
  onCancel,
}: {
  lesson: Lesson;
  busy: boolean;
  onSave: (title: string, guidance: string, areas: string[]) => void;
  onCancel: () => void;
}): JSX.Element {
  const [title, setTitle] = useState(lesson.title);
  const [guidance, setGuidance] = useState(lesson.guidance);
  const [areas, setAreas] = useState(lesson.areaGlobs.join(', '));
  return (
    <form
      className="mt-2 grid gap-2 text-[12px]"
      onSubmit={(e) => {
        e.preventDefault();
        onSave(
          title,
          guidance,
          areas
            .split(',')
            .map((a) => a.trim())
            .filter((a) => a !== ''),
        );
      }}
    >
      <label className="grid gap-0.5">
        <span className="text-ink-dim">
          <Trans>title</Trans>
        </span>
        <input
          className="rounded border border-line bg-transparent px-2 py-1 text-ink"
          value={title}
          onChange={(e) => setTitle(e.target.value)}
        />
      </label>
      <label className="grid gap-0.5">
        <span className="text-ink-dim">
          <Trans>guidance (one sentence)</Trans>
        </span>
        <input
          className="rounded border border-line bg-transparent px-2 py-1 text-ink"
          value={guidance}
          onChange={(e) => setGuidance(e.target.value)}
        />
      </label>
      <label className="grid gap-0.5">
        <span className="text-ink-dim">
          <Trans>area globs (comma-separated)</Trans>
        </span>
        <input
          className="rounded border border-line bg-transparent px-2 py-1 font-mono text-ink"
          value={areas}
          onChange={(e) => setAreas(e.target.value)}
        />
      </label>
      <div className="flex gap-2">
        <button type="submit" disabled={busy} className={BTN_PRIMARY}>
          <Trans>save</Trans>
        </button>
        <button type="button" onClick={onCancel} className={BTN}>
          <Trans>cancel</Trans>
        </button>
      </div>
    </form>
  );
}

function LessonCard({
  lesson,
  busy,
  act,
}: {
  lesson: Lesson;
  busy: boolean;
  act: (run: () => Promise<Lesson>) => void;
}): JSX.Element {
  const { t } = useLingui();
  const [editing, setEditing] = useState(false);
  const isCandidate = lesson.status === 'candidate';
  const surprise = lesson.surpriseIndex?.toFixed(2) ?? null;
  const recurrences = String(lesson.recurrences);
  const linkedTitle = lesson.linkedNormTitle;
  const lessonId = String(lesson.id);
  const branch = lesson.promotedBranch;
  return (
    <li className="rounded border border-line p-3">
      <div className="flex items-baseline justify-between gap-3">
        <div className="text-ink">{lesson.title}</div>
        <div className="shrink-0 font-mono text-[10px] text-ink-dim">
          {lesson.source === 'review' && (
            <span
              className="mr-1 rounded border border-brand px-1 text-brand"
              title={t`proposed because reviewers raised the same finding type in this area in several phases`}
            >
              <Trans>review</Trans>
            </span>
          )}
          {lesson.status}
          {surprise !== null && t` · surprise ${surprise}`}
          {lesson.recurrences > 1 && t` · seen ${recurrences}×`}
        </div>
      </div>
      <p className="mt-1 text-[12px] text-ink">{lesson.guidance}</p>
      <div className="mt-1 flex flex-wrap gap-1 font-mono text-[10px] text-ink-dim">
        {lesson.areaGlobs.map((g) => (
          <span key={g} className="rounded border border-line px-1">
            {g}
          </span>
        ))}
      </div>
      {lesson.cause !== '' && (
        <p className="mt-1 text-[11px] text-ink-dim">
          <span className="text-ink">
            <Trans>cause:</Trans>
          </span>{' '}
          {lesson.cause}
        </p>
      )}
      <EvidenceList lesson={lesson} />
      <div className="mt-1 font-mono text-[10px] text-ink-dim">
        {lesson.planId} · {lesson.phaseName}
        {linkedTitle !== '' && t` · linked to "${linkedTitle}"`}
      </div>
      {lesson.status === 'active' &&
        lesson.effectiveness !== undefined &&
        lesson.effectiveness !== null && (
          <div className="mt-1 font-mono text-[10px] text-ink-dim">
            <Trans>effectiveness:</Trans> {effectivenessLabel(lesson.effectiveness)}
          </div>
        )}
      {lesson.promotedBranch !== '' && (
        <div className="mt-1 font-mono text-[10px] text-ink-dim">
          <Trans>
            L-{lessonId} promoted to CLAUDE.md on branch <span className="text-ink">{branch}</span>. Review it
            there.
          </Trans>
        </div>
      )}
      {lesson.sourceParagraph !== '' && (
        <details className="mt-1 text-[11px] text-ink-dim">
          <summary className="cursor-pointer">
            <Trans>where reality diverged</Trans>
          </summary>
          <p className="mt-1 whitespace-pre-wrap">{lesson.sourceParagraph}</p>
        </details>
      )}
      {editing ? (
        <EditForm
          lesson={lesson}
          busy={busy}
          onCancel={() => setEditing(false)}
          onSave={(title, guidance, areaGlobs) => {
            setEditing(false);
            act(() => editLesson(lesson.id, { title, guidance, areaGlobs }));
          }}
        />
      ) : (
        <div className="mt-2 flex flex-wrap items-center gap-2">
          {isCandidate && (
            <button
              type="button"
              disabled={busy}
              className={BTN_PRIMARY}
              onClick={() => act(() => acceptLesson(lesson.id))}
            >
              <Trans>accept</Trans>
            </button>
          )}
          {(isCandidate || lesson.status === 'active') && (
            <button type="button" disabled={busy} className={BTN} onClick={() => setEditing(true)}>
              <Trans>edit</Trans>
            </button>
          )}
          {isCandidate &&
            lesson.matches.map((m) => {
              const match = matchLabel(m);
              return (
                <button
                  key={`${m.kind}:${m.normTitle}:${String(m.lessonId ?? 0)}`}
                  type="button"
                  disabled={busy}
                  className={BTN}
                  title={t`merge this candidate into the existing lesson`}
                  onClick={() =>
                    act(() =>
                      mergeLesson(
                        lesson.id,
                        m.kind === 'retro' ? { normTitle: m.normTitle } : { lessonId: m.lessonId ?? 0 },
                      ),
                    )
                  }
                >
                  <Trans>merge into {match}</Trans>
                </button>
              );
            })}
          {isCandidate && (
            <button
              type="button"
              disabled={busy}
              className={BTN}
              // i18n-ignore — the reason is stored server-side, not shown here
              onClick={() => act(() => dismissLesson(lesson.id, 'dismissed by operator'))}
            >
              <Trans>dismiss</Trans>
            </button>
          )}
          {lesson.status === 'active' && (
            <button
              type="button"
              disabled={busy}
              className={BTN}
              // i18n-ignore — the reason is stored server-side, not shown here
              onClick={() => act(() => retireLesson(lesson.id, 'retired by operator'))}
            >
              <Trans>retire</Trans>
            </button>
          )}
          {lesson.status === 'active' && lesson.promotedBranch === '' && (
            <button
              type="button"
              disabled={busy}
              className={BTN}
              title={t`Write this lesson into the project's CLAUDE.md for its area, committed on a new branch for you to review`}
              onClick={() => act(() => promoteLesson(lesson.id))}
            >
              <Trans>promote to CLAUDE.md</Trans>
            </button>
          )}
        </div>
      )}
    </li>
  );
}

/** "in force · 12 d" or "candidate": how a live lesson stands, for its row and page. */
export function lessonStanding(lesson: Lesson): string {
  if (lesson.status !== 'active') return tr`candidate`;
  const since = Date.parse(lesson.activatedAt ?? lesson.createdAt);
  const days = String(Number.isNaN(since) ? 0 : Math.max(0, Math.floor((Date.now() - since) / 86_400_000)));
  return tr`in force · ${days} d`;
}

/** One lesson as a detail page (handoff §9): what it reached and what that did to
 *  its area, then everything the review card shows, so nothing depends on opening
 *  the queue. `act` runs a lesson action and reloads the list. */
export function LessonDetail({
  lesson,
  busy,
  act,
}: {
  lesson: Lesson;
  busy: boolean;
  act: (run: () => Promise<Lesson>) => void;
}): JSX.Element {
  const { t } = useLingui();
  const [editing, setEditing] = useState(false);
  const eff = lesson.effectiveness ?? null;
  const uses = eff?.uses ?? 0;
  const before = eff?.medianBefore?.toFixed(2) ?? null;
  const after = eff?.medianAfter?.toFixed(2) ?? null;
  const measured = before !== null && after !== null;
  const dropped = (eff?.medianDrop ?? 0) > 0;
  const isCandidate = lesson.status === 'candidate';
  const isActive = lesson.status === 'active';
  const usesText = String(uses);
  const minRuns = String(eff?.minRuns ?? 0);
  const surprise = lesson.surpriseIndex?.toFixed(2) ?? null;
  const planLine = `${lesson.planId} · ${lesson.phaseName}`;
  const linkedTitle = lesson.linkedNormTitle;
  const lessonId = String(lesson.id);
  const branch = lesson.promotedBranch;

  const lead = isCandidate
    ? t`A candidate: it reaches no run until you accept it.`
    : measured
      ? t`Injected into ${usesText} runs. Median surprise in its area: ${before} → ${after}.`
      : uses > 0
        ? t`Reached ${usesText} runs so far: too few to judge. Proof needs ${minRuns}.`
        : t`Not injected into any run yet.`;

  const kv = [
    { k: t`injected when`, v: lesson.areaGlobs.length > 0 ? lesson.areaGlobs.join(' · ') : t`every run` },
    { k: t`origin`, v: planLine },
  ];
  if (lesson.source === 'review') kv.push({ k: t`source`, v: t`review` });
  if (surprise !== null) kv.push({ k: t`surprise`, v: surprise });
  if (lesson.recurrences > 1) kv.push({ k: t`seen`, v: `${String(lesson.recurrences)}×` });
  if (linkedTitle !== '') kv.push({ k: t`linked to`, v: linkedTitle });

  const actions: PageAction[] = [];
  if (isCandidate) {
    actions.push({ label: t`accept`, tone: 'brand', disabled: busy, onClick: () => act(() => acceptLesson(lesson.id)) });
  }
  if (isCandidate || isActive) {
    actions.push({ label: t`edit wording`, disabled: busy, onClick: () => setEditing(true) });
  }
  if (isCandidate) {
    for (const m of lesson.matches) {
      const match = matchLabel(m);
      actions.push({
        key: `${m.kind}:${m.normTitle}:${String(m.lessonId ?? 0)}`,
        label: t`merge into ${match}`,
        tip: t`merge this candidate into the existing lesson`,
        disabled: busy,
        onClick: () =>
          act(() =>
            mergeLesson(lesson.id, m.kind === 'retro' ? { normTitle: m.normTitle } : { lessonId: m.lessonId ?? 0 }),
          ),
      });
    }
    actions.push({
      label: t`dismiss`,
      disabled: busy,
      // i18n-ignore — the reason is stored server-side, not shown here
      onClick: () => act(() => dismissLesson(lesson.id, 'dismissed by operator')),
    });
  }
  if (isActive) {
    actions.push({
      label: t`retire`,
      disabled: busy,
      // i18n-ignore — the reason is stored server-side, not shown here
      onClick: () => act(() => retireLesson(lesson.id, 'retired by operator')),
    });
    if (lesson.promotedBranch === '') {
      actions.push({
        label: t`promote to CLAUDE.md`,
        tip: t`Write this lesson into the project's CLAUDE.md for its area, committed on a new branch for you to review`,
        disabled: busy,
        onClick: () => act(() => promoteLesson(lesson.id)),
      });
    }
  }

  return (
    <PageDetail
      kind={t`lesson`}
      kindTone="purple"
      meta={`${lesson.planId} · ${lessonStanding(lesson)}`}
      title={lesson.title}
      lead={lead}
      {...(eff !== null
        ? {
            stats: [
              { label: t`runs reached`, value: usesText, ...(measured ? {} : { tone: 'faint' as const }) },
              ...(measured
                ? [
                    { label: t`surprise before`, value: before, tone: 'faint' as const },
                    { label: t`after`, value: after, tone: dropped ? ('green' as const) : ('red' as const) },
                  ]
                : []),
            ],
          }
        : {})}
      kv={kv}
      paras={lesson.cause !== '' ? [lesson.guidance, t`cause: ${lesson.cause}`] : [lesson.guidance]}
      actions={editing ? [] : actions}
    >
      {editing && (
        <EditForm
          lesson={lesson}
          busy={busy}
          onCancel={() => setEditing(false)}
          onSave={(title, guidance, areaGlobs) => {
            setEditing(false);
            act(() => editLesson(lesson.id, { title, guidance, areaGlobs }));
          }}
        />
      )}
      <div className="mt-[18px]">
        {lesson.evidence.length > 0 && <EvidenceList lesson={lesson} />}
        {isActive && eff !== null && (
          <div className="mt-1 font-mono text-[10px] text-ink-dim">
            <Trans>effectiveness:</Trans> {effectivenessLabel(eff)}
          </div>
        )}
        {lesson.promotedBranch !== '' && (
          <div className="mt-1 font-mono text-[10px] text-ink-dim">
            <Trans>
              L-{lessonId} promoted to CLAUDE.md on branch <span className="text-ink">{branch}</span>. Review it
              there.
            </Trans>
          </div>
        )}
        {lesson.sourceParagraph !== '' && (
          <details className="mt-1 text-[11px] text-ink-dim">
            <summary className="cursor-pointer">
              <Trans>where reality diverged</Trans>
            </summary>
            <p className="mt-1 whitespace-pre-wrap">{lesson.sourceParagraph}</p>
          </details>
        )}
      </div>
    </PageDetail>
  );
}

/** Lessons — the review queue for lessons learned from surprising runs
 *  (learning-loop phase 14). Nothing becomes active without an accept here.
 *  `embedded` renders it as Learning's Lessons tab: no page heading or padding,
 *  and no calibration panel (that is its own "Forecast honesty" tab). */
export function Lessons({
  embedded = false,
  onChanged,
}: {
  embedded?: boolean;
  /** After a lesson action: the host's own list is stale too. */
  onChanged?: () => void;
} = {}): JSX.Element {
  const { i18n, t } = useLingui();
  const [filter, setFilter] = useState<LessonStatus | undefined>('candidate');
  const [items, setItems] = useState<Lesson[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback((f: LessonStatus | undefined) => {
    fetchLessons(f)
      .then((ls) => {
        setItems(ls);
        setErr(null);
      })
      .catch((e: unknown) => setErr(String(e)));
  }, []);

  useEffect(() => {
    load(filter);
  }, [filter, load]);

  const act = useCallback(
    (run: () => Promise<Lesson>) => {
      setBusy(true);
      run()
        .then(() => {
          load(filter);
          onChanged?.();
        })
        .catch((e: unknown) => setErr(String(e)))
        .finally(() => setBusy(false));
    },
    [filter, load, onChanged],
  );

  return (
    <div className={embedded ? '' : 'p-6'}>
      {!embedded && (
        <h1 className="text-lg text-ink">
          <Trans>Lessons</Trans>
        </h1>
      )}
      <p className={`${embedded ? '' : 'mt-1 '}max-w-2xl text-[12px] text-ink-dim`}>
        <Trans>
          When a phase run lands far from its forecast and its report explains why, a cheap model
          proposes up to two lessons, each citing evidence from that run. A candidate reaches future
          runs only after you accept it here. Merge it into an existing lesson when it repeats one.
          Active lessons are re-measured against their area&apos;s surprise; one that stops earning
          its place is proposed for retirement below, and retired on its own after 14 days without
          an answer.
        </Trans>{' '}
        <Trans>
          A review finding type that repeats in one area across two or more phases is proposed here
          too, marked review and citing its findings; dismissing it mutes that area for 30 days.
        </Trans>
      </p>
      <RetirementQueue />
      <fieldset className="mt-3 inline-flex gap-1" aria-label={t`filter lessons by status`}>
        {FILTERS.map((f) => (
          <button
            key={f.value ?? 'all'}
            type="button"
            aria-pressed={filter === f.value}
            onClick={() => setFilter(f.value)}
            className={`rounded border px-1.5 py-px font-mono text-[10px] ${
              filter === f.value ? 'border-brand text-brand' : 'border-line text-ink-dim hover:text-ink'
            }`}
          >
            {i18n._(f.label)}
          </button>
        ))}
      </fieldset>
      {err !== null && (
        <div role="alert" className="mt-3 text-[12px] text-red">
          {err}
        </div>
      )}
      {items === null && err === null && (
        <div className="mt-4 text-ink-dim">
          <Trans>loading…</Trans>
        </div>
      )}
      {items !== null && items.length === 0 && (
        <div className="mt-4 text-[12px] text-ink-dim">
          <Trans>No lessons here.</Trans>
        </div>
      )}
      {items !== null && items.length > 0 && (
        <ul className="mt-4 grid gap-3">
          {items.map((l) => (
            <LessonCard key={l.id} lesson={l} busy={busy} act={act} />
          ))}
        </ul>
      )}
      {!embedded && <CalibrationPanel />}
    </div>
  );
}
