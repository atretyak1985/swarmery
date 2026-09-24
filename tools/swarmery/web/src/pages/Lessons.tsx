import { useCallback, useEffect, useState } from 'react';
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
  type LessonMatch,
  type LessonStatus,
  mergeLesson,
  promoteLesson,
  type RetirementProposal,
  retireLesson,
} from '../api/lessons';
import { CalibrationPanel } from './LessonsCalibration';

const BTN = 'rounded border border-line px-2 py-px font-mono text-[11px] text-ink-dim hover:text-ink';
const BTN_PRIMARY = 'rounded border border-brand px-2 py-px font-mono text-[11px] text-brand';

function effectivenessLabel(e: LessonEffectiveness): string {
  const drop =
    e.medianDrop === null
      ? `not enough data (${String(e.beforeN)}/${String(e.afterN)} of ${String(e.minRuns)} runs)`
      : `surprise ${(e.medianBefore ?? 0).toFixed(2)} → ${(e.medianAfter ?? 0).toFixed(2)}`;
  const relied =
    e.reliedRate === null
      ? 'never injected'
      : `relied on ${String(Math.round(e.reliedRate * 100))}% of ${String(e.uses)}`;
  return `${drop} · ${relied}`;
}

const REASON_LABEL: Record<RetirementProposal['reason'], string> = {
  ineffective: 'ineffective',
  stale: 'stale',
  unused_60d: 'unused 60 days',
  superseded: 'superseded',
};

/** The retirement queue (phase 16.2): proposals wait for the operator; an
 *  unanswered one is retired by the daemon on its auto-retire date. */
function RetirementQueue(): JSX.Element | null {
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
    <section className="mt-4 max-w-3xl" aria-labelledby="retirement-heading">
      <h2 id="retirement-heading" className="text-sm text-ink">
        Proposed retirements
      </h2>
      {err !== null && (
        <div role="alert" className="mt-2 text-[12px] text-red">
          {err}
        </div>
      )}
      <ul className="mt-2 grid gap-2">
        {(items ?? []).map((p) => (
          <li key={p.id} className="rounded border border-line p-2 text-[12px]">
            <div className="flex items-baseline justify-between gap-3">
              <span className="text-ink">
                L-{String(p.lessonId)} {p.title}
              </span>
              <span className="shrink-0 font-mono text-[10px] text-ink-dim">
                {REASON_LABEL[p.reason]}
                {p.autoRetireAt !== null && ` · auto-retires ${p.autoRetireAt.slice(0, 10)}`}
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
                retire
              </button>
              <button
                type="button"
                disabled={busy}
                className={BTN}
                onClick={() => decide(() => keepLesson(p.id))}
              >
                keep
              </button>
            </div>
          </li>
        ))}
      </ul>
    </section>
  );
}

const FILTERS: { value: LessonStatus | undefined; label: string }[] = [
  { value: 'candidate', label: 'candidates' },
  { value: 'active', label: 'active' },
  { value: undefined, label: 'all' },
];

function matchLabel(m: LessonMatch): string {
  const where = m.kind === 'retro' ? `retro ×${String(m.count)}` : `lesson #${String(m.lessonId ?? 0)}`;
  return `${m.exact ? '= ' : '≈ '}${m.title} (${where})`;
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
        <span className="text-ink-dim">title</span>
        <input
          className="rounded border border-line bg-transparent px-2 py-1 text-ink"
          value={title}
          onChange={(e) => setTitle(e.target.value)}
        />
      </label>
      <label className="grid gap-0.5">
        <span className="text-ink-dim">guidance (one sentence)</span>
        <input
          className="rounded border border-line bg-transparent px-2 py-1 text-ink"
          value={guidance}
          onChange={(e) => setGuidance(e.target.value)}
        />
      </label>
      <label className="grid gap-0.5">
        <span className="text-ink-dim">area globs (comma-separated)</span>
        <input
          className="rounded border border-line bg-transparent px-2 py-1 font-mono text-ink"
          value={areas}
          onChange={(e) => setAreas(e.target.value)}
        />
      </label>
      <div className="flex gap-2">
        <button type="submit" disabled={busy} className={BTN_PRIMARY}>
          save
        </button>
        <button type="button" onClick={onCancel} className={BTN}>
          cancel
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
  const [editing, setEditing] = useState(false);
  const isCandidate = lesson.status === 'candidate';
  return (
    <li className="rounded border border-line p-3">
      <div className="flex items-baseline justify-between gap-3">
        <div className="text-ink">{lesson.title}</div>
        <div className="shrink-0 font-mono text-[10px] text-ink-dim">
          {lesson.status}
          {lesson.surpriseIndex !== null && ` · surprise ${lesson.surpriseIndex.toFixed(2)}`}
          {lesson.recurrences > 1 && ` · seen ${String(lesson.recurrences)}×`}
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
          <span className="text-ink">cause:</span> {lesson.cause}
        </p>
      )}
      <div className="mt-1 font-mono text-[10px] text-ink-dim">
        evidence: {lesson.evidence.join(' · ')}
      </div>
      <div className="mt-1 font-mono text-[10px] text-ink-dim">
        {lesson.planId} · {lesson.phaseName}
        {lesson.linkedNormTitle !== '' && ` · linked to "${lesson.linkedNormTitle}"`}
      </div>
      {lesson.status === 'active' &&
        lesson.effectiveness !== undefined &&
        lesson.effectiveness !== null && (
          <div className="mt-1 font-mono text-[10px] text-ink-dim">
            effectiveness: {effectivenessLabel(lesson.effectiveness)}
          </div>
        )}
      {lesson.promotedBranch !== '' && (
        <div className="mt-1 font-mono text-[10px] text-ink-dim">
          L-{String(lesson.id)} promoted to CLAUDE.md on branch{' '}
          <span className="text-ink">{lesson.promotedBranch}</span>. Review it there.
        </div>
      )}
      {lesson.sourceParagraph !== '' && (
        <details className="mt-1 text-[11px] text-ink-dim">
          <summary className="cursor-pointer">where reality diverged</summary>
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
              accept
            </button>
          )}
          {(isCandidate || lesson.status === 'active') && (
            <button type="button" disabled={busy} className={BTN} onClick={() => setEditing(true)}>
              edit
            </button>
          )}
          {isCandidate &&
            lesson.matches.map((m) => (
              <button
                key={`${m.kind}:${m.normTitle}:${String(m.lessonId ?? 0)}`}
                type="button"
                disabled={busy}
                className={BTN}
                title="merge this candidate into the existing lesson"
                onClick={() =>
                  act(() =>
                    mergeLesson(
                      lesson.id,
                      m.kind === 'retro' ? { normTitle: m.normTitle } : { lessonId: m.lessonId ?? 0 },
                    ),
                  )
                }
              >
                merge into {matchLabel(m)}
              </button>
            ))}
          {isCandidate && (
            <button
              type="button"
              disabled={busy}
              className={BTN}
              onClick={() => act(() => dismissLesson(lesson.id, 'dismissed by operator'))}
            >
              dismiss
            </button>
          )}
          {lesson.status === 'active' && (
            <button
              type="button"
              disabled={busy}
              className={BTN}
              onClick={() => act(() => retireLesson(lesson.id, 'retired by operator'))}
            >
              retire
            </button>
          )}
          {lesson.status === 'active' && lesson.promotedBranch === '' && (
            <button
              type="button"
              disabled={busy}
              className={BTN}
              title="Write this lesson into the project's CLAUDE.md for its area, committed on a new branch for you to review"
              onClick={() => act(() => promoteLesson(lesson.id))}
            >
              promote to CLAUDE.md
            </button>
          )}
        </div>
      )}
    </li>
  );
}

/** Lessons — the review queue for lessons learned from surprising runs
 *  (learning-loop phase 14). Nothing becomes active without an accept here. */
export function Lessons(): JSX.Element {
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
        .then(() => load(filter))
        .catch((e: unknown) => setErr(String(e)))
        .finally(() => setBusy(false));
    },
    [filter, load],
  );

  return (
    <div className="p-6">
      <h1 className="text-lg text-ink">Lessons</h1>
      <p className="mt-1 max-w-2xl text-[12px] text-ink-dim">
        When a phase run lands far from its forecast and its report explains why, a cheap model
        proposes up to two lessons, each citing evidence from that run. A candidate reaches future
        runs only after you accept it here. Merge it into an existing lesson when it repeats one.
        Active lessons are re-measured against their area&apos;s surprise; one that stops earning
        its place is proposed for retirement below, and retired on its own after 14 days without
        an answer.
      </p>
      <RetirementQueue />
      <fieldset className="mt-3 inline-flex gap-1" aria-label="filter lessons by status">
        {FILTERS.map((f) => (
          <button
            key={f.label}
            type="button"
            aria-pressed={filter === f.value}
            onClick={() => setFilter(f.value)}
            className={`rounded border px-1.5 py-px font-mono text-[10px] ${
              filter === f.value ? 'border-brand text-brand' : 'border-line text-ink-dim hover:text-ink'
            }`}
          >
            {f.label}
          </button>
        ))}
      </fieldset>
      {err !== null && (
        <div role="alert" className="mt-3 text-[12px] text-red">
          {err}
        </div>
      )}
      {items === null && err === null && <div className="mt-4 text-ink-dim">loading…</div>}
      {items !== null && items.length === 0 && (
        <div className="mt-4 text-[12px] text-ink-dim">No lessons here.</div>
      )}
      {items !== null && items.length > 0 && (
        <ul className="mt-4 grid max-w-3xl gap-3">
          {items.map((l) => (
            <LessonCard key={l.id} lesson={l} busy={busy} act={act} />
          ))}
        </ul>
      )}
      <CalibrationPanel />
    </div>
  );
}
