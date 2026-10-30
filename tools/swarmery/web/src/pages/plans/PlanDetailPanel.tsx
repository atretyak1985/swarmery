// The plan's details, opened inline in place of the phase list.

import type { MessageDescriptor } from '@lingui/core';
import { msg, plural, t } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { useEffect, useMemo, useState } from 'react';
import { fetchPlanDoc } from '../../api';
import type { Epic, PlanRevision } from '../../api/types';
import { CopyIdBadge } from '../../components/CopyIdBadge';
import { ErrorBox, Loading } from '../../components/ui';
import { fmtDateTime } from '../../lib/format';
import { Markdown } from '../../lib/markdown';
import { ORIGIN_LABEL, RevisionReview } from '../planning/RevisionReview';
import { DecisionTable } from './DecisionTable';
import { RailSection } from './ForecastVsActual';
import { PHASE_CHIP, phaseChip, ProgressBar } from './phaseChips';
import { PlainSummary } from './PlainSummary';
import { DetailShell, DetailTabs, DocEditor } from './planDetailShell';
import { computeResolvedSeqs, extractSection, phaseStatus, planComplete } from './planModel';
import { EPIC_STATUS_LABEL, PLAN_DETAIL_TAB_LABEL } from './plansTarget';
import type { PlanDetailTab } from './plansUrl';
import { usePlanDoc } from './usePlanDoc';

// Plan status badge — the theme has no `yellow` token; `amber` is the app's
// semantic waiting/approval color, so paused uses it.
const STATUS_BADGE: Record<Epic['status'], string> = {
  active: 'border-brand/40 text-brand',
  paused: 'border-amber/40 text-amber',
  done: 'border-green/40 text-green',
  archived: 'border-line text-ink-faint',
};


/** Plan details panel: a Plan tab (the plan README markdown), a Spec tab
 * (rendered spec.md + per-criterion coverage; only when the plan has one), an
 * Edit tab (raw README) and — only when every phase is done — a Summary tab
 * with the per-phase executed work. Rendered inline in place of the phase
 * list. */
export function PlanDetailPanel({
  epic,
  tab,
  onTab,
  tabHref,
  onDocChanged,
  revisions,
  revisionsErr,
  stagedCount,
  focusRevId = null,
  onRevisionsChanged,
}: {
  epic: Epic;
  tab: PlanDetailTab;
  onTab: (tab: PlanDetailTab) => void;
  /** Each tab's URL — makes the tabs real links. */
  tabHref?: ((tab: PlanDetailTab) => string) | undefined;
  onDocChanged: () => void;
  /** The plan's revisions, newest first (fetched by EpicDetail). */
  revisions: PlanRevision[] | null;
  revisionsErr: string | null;
  stagedCount: number;
  /** The revision a `/details/revisions/<revId>` link opened — expanded and
   * marked current on the Revisions tab. */
  focusRevId?: number | null;
  /** A revision was decided — refetch the list (and the docs, on apply). */
  onRevisionsChanged: () => void;
}): JSX.Element {
  const { t, i18n } = useLingui();
  const resolvedSeqs = useMemo(() => computeResolvedSeqs(epic.phases), [epic.phases]);
  const complete = planComplete(epic, resolvedSeqs);
  // The Summary tab exists only on complete plans, the Spec tab only on plans
  // with a spec.md — a stale selection (e.g. a rescan un-ticked a box, or
  // removed the spec) degrades to the Plan tab instead of a dead panel.
  const activeTab: PlanDetailTab =
    (tab === 'summary' && !complete) || (tab === 'spec' && !epic.hasSpec) ? 'plan' : tab;
  const staged = String(stagedCount);
  const tabs: { id: PlanDetailTab; label: string }[] = [
    { id: 'plan', label: i18n._(PLAN_DETAIL_TAB_LABEL.plan) },
    { id: 'plain', label: i18n._(PLAN_DETAIL_TAB_LABEL.plain) },
    ...(epic.hasSpec ? [{ id: 'spec' as const, label: i18n._(PLAN_DETAIL_TAB_LABEL.spec) }] : []),
    ...(complete ? [{ id: 'summary' as const, label: i18n._(PLAN_DETAIL_TAB_LABEL.summary) }] : []),
    { id: 'revisions', label: stagedCount > 0 ? t`Revisions (${staged})` : i18n._(PLAN_DETAIL_TAB_LABEL.revisions) },
    { id: 'edit', label: i18n._(PLAN_DETAIL_TAB_LABEL.edit) },
  ];
  const title = epic.title;

  return (
    <DetailShell
      ariaLabel={t`${title} — plan details`}
      header={
        <div className="min-w-0">
          <div className="text-[14px] font-semibold text-ink">{epic.title}</div>
          <div className="mt-1 flex flex-wrap items-center gap-1.5">
            <span
              className={`rounded border px-1.5 py-px font-mono text-[9.5px] ${STATUS_BADGE[epic.status]}`}
            >
              {i18n._(EPIC_STATUS_LABEL[epic.status])}
            </span>
            <CopyIdBadge id={epic.externalId} label={t`plan`} />
            <span className="font-mono text-[10px] text-ink-faint">
              {epic.rollup.done}/{epic.rollup.total} ({Math.round(epic.rollup.pct)}%)
            </span>
          </div>
        </div>
      }
      tabBar={
        <DetailTabs label={t`plan details tabs`} tabs={tabs} active={activeTab} onTab={onTab} hrefFor={tabHref} />
      }
    >
      {activeTab === 'edit' ? (
        <DocEditor taskId={epic.taskId} path="README.md" version={null} onSaved={onDocChanged} />
      ) : activeTab === 'plan' ? (
        <>
          {/* The decision → phase → evidence table, inline above the README;
              absent from an older daemon's payload. */}
          {epic.evidence !== undefined && <DecisionTable table={epic.evidence} />}
          <PlanReadme epic={epic} />
        </>
      ) : activeTab === 'plain' ? (
        <PlainSummary taskId={epic.taskId} kind="plan" />
      ) : activeTab === 'spec' ? (
        <PlanSpec epic={epic} />
      ) : activeTab === 'revisions' ? (
        <PlanRevisionsTab
          revisions={revisions}
          revisionsErr={revisionsErr}
          focusRevId={focusRevId}
          onChanged={onRevisionsChanged}
        />

      ) : (
        <PlanSummary epic={epic} resolvedSeqs={resolvedSeqs} />
      )}
    </DetailShell>
  );
}

const REVISION_STATUS_CHIP: Record<PlanRevision['status'], string> = {
  staged: 'border-amber/40 bg-amber/10 text-amber',
  applied: 'border-green/40 bg-green/10 text-green',
  rejected: 'border-red/40 bg-red/10 text-red',
  superseded: 'border-line text-ink-faint',
  failed: 'border-red/40 bg-red/10 text-red',
};

/** What a revision status reads as; the keys are the daemon's status codes. */
const REVISION_STATUS_LABEL: Record<PlanRevision['status'], MessageDescriptor> = {
  staged: msg`staged`,
  applied: msg`applied`,
  rejected: msg`rejected`,
  superseded: msg`superseded`,
  failed: msg`failed`,
};

/** One decided revision's timestamp line: who decided it and when, else when it
 * was created. */
function revisionWhen(r: PlanRevision): string {
  if (r.decidedAt === undefined) {
    const created = fmtDateTime(r.createdAt);
    return t`created ${created}`;
  }
  const decided = fmtDateTime(r.decidedAt);
  const by = r.decidedBy;
  return by !== undefined ? t`decided ${decided} by ${by}` : t`decided ${decided}`;
}

/** "3 docs: a.md (edit), b.md → c.md (rename)" under a decided revision. */
function revisionFilesLabel(r: PlanRevision): string {
  const docCount = r.files.length;
  const count = plural(docCount, { one: '# doc', few: '# docs', many: '# docs', other: '# docs' });
  if (r.files.length === 0) return count;
  const list = r.files
    .map((f) => {
      const action = f.action;
      const docPath = f.docPath;
      if (action === 'rename' && f.renameFrom !== undefined) {
        const from = f.renameFrom;
        return t`${from} → ${docPath} (rename)`;
      }
      return `${docPath} (${action})`;
    })
    .join(', ');
  return `${count}: ${list}`;
}

/** Revisions tab body: the open (staged) revision as a full diff review, and
 * below it the decided history — every row answers "was it manual or
 * automated?" with its `origin` and `decidedBy`, never only the open one. */
function PlanRevisionsTab({
  revisions,
  revisionsErr,
  focusRevId,
  onChanged,
}: {
  revisions: PlanRevision[] | null;
  revisionsErr: string | null;
  /** The linked revision (`/details/revisions/<revId>`): the staged one is
   * always shown as a full review; a decided row is expanded (reason unclamped)
   * and both are marked aria-current. */
  focusRevId: number | null;
  onChanged: () => void;
}): JSX.Element {
  const { t, i18n } = useLingui();
  if (revisionsErr !== null) return <ErrorBox message={revisionsErr} onRetry={onChanged} />;
  if (revisions === null) return <Loading label={t`revisions…`} />;
  const staged = revisions.find((r) => r.status === 'staged');
  const decided = revisions.filter((r) => r.status !== 'staged');
  if (staged === undefined && decided.length === 0) {
    return (
      <div className="font-mono text-[11.5px] text-ink-faint">
        <Trans>no revisions — &quot;Revise plan&quot; in the action row starts one</Trans>
      </div>
    );
  }
  return (
    <div className="space-y-4">
      {staged !== undefined && (
        <div
          data-revision-id={staged.id}
          {...(staged.id === focusRevId ? { 'aria-current': 'true' as const } : {})}
        >
          <RailSection label={t`staged — awaiting your decision`}>
            <RevisionReview revisionId={staged.id} onDecided={onChanged} />
          </RailSection>
        </div>
      )}
      {decided.length > 0 && (
        <RailSection label={t`history`}>
          <ul className="space-y-2">
            {decided.map((r) => (
              <li
                key={r.id}
                data-revision-id={r.id}
                {...(r.id === focusRevId ? { 'aria-current': 'true' as const } : {})}
                className={`rounded-lg border bg-surface/40 px-3 py-2.5 ${
                  r.id === focusRevId ? 'border-brand/50' : 'border-line'
                }`}
              >
                <div className="flex flex-wrap items-center gap-1.5">
                  <span
                    className={`rounded border px-1.5 py-px font-mono text-[9.5px] ${REVISION_STATUS_CHIP[r.status]}`}
                  >
                    {i18n._(REVISION_STATUS_LABEL[r.status])}
                  </span>
                  <span className="rounded border border-line-strong bg-surface2 px-1.5 py-px font-mono text-[9.5px] text-ink-dim">
                    {ORIGIN_LABEL[r.origin]}
                  </span>
                  <span className="font-mono text-[10px] text-ink-faint">{revisionWhen(r)}</span>
                </div>
                <div
                  className={`mt-1.5 text-[12px] leading-relaxed whitespace-pre-wrap text-ink-2 ${
                    r.id === focusRevId ? '' : 'line-clamp-3'
                  }`}
                >
                  {r.reason}
                </div>
                {r.error !== undefined && r.error !== '' && (
                  <div className="mt-1 font-mono text-[10.5px] break-words text-red">{r.error}</div>
                )}
                <div className="mt-1.5 font-mono text-[10px] text-ink-faint">{revisionFilesLabel(r)}</div>
              </li>
            ))}
          </ul>
        </RailSection>
      )}
    </div>
  );
}

/** A spec criterion's chip tooltip: which phase claims it. */
function coveredByTip(seq: number, name: string): string {
  const phase = String(seq);
  return t`covered by phase ${phase} — ${name}`;
}

/** A phase `**Covers:**` reference to a criterion id the spec never declared. */
function unknownRefLabel(seq: number, cid: string): string {
  const phase = String(seq);
  return t`phase ${phase} covers ${cid}, which the spec never declares`;
}

/** Plan tab body — the plan README markdown. */
function PlanReadme({ epic }: { epic: Epic }): JSX.Element {
  const { t } = useLingui();
  const [readme] = usePlanDoc(epic.taskId, 'README.md', null);
  if (readme === null) return <Loading label={t`readme…`} />;
  return <Markdown text={readme} />;
}

/** Spec tab body — the rendered plan/spec.md under a coverage rail: progress
 * bar plus one row per criterion (tick glyph, cid chip, text, then the phase
 * chips whose `**Covers:**` line claims it — or an amber `uncovered`). Phase
 * `Covers` references to ids the spec never declared surface as amber notes
 * (speculation signal). The rail exists only when the scanner parsed criteria
 * rows (`epic.spec`); a spec.md without SC lines still renders as markdown. */
function PlanSpec({ epic }: { epic: Epic }): JSX.Element {
  const { t } = useLingui();
  const [doc] = usePlanDoc(epic.taskId, 'spec.md', null);
  const spec = epic.spec;
  // Tone the P{seq} chips like the phase rows do — the covering phase's own
  // derived status through `phaseChip`, so a done phase covers in green.
  const resolvedSeqs = useMemo(() => computeResolvedSeqs(epic.phases), [epic.phases]);
  const phaseBySeq = useMemo(
    () => new Map(epic.phases.map((p) => [p.seq, p])),
    [epic.phases],
  );
  return (
    <>
      {spec !== null && (
        <RailSection label={t`coverage`}>
          <ProgressBar done={spec.covered} total={spec.total} className="mb-2.5 max-w-[220px]" />
          <ul className="space-y-1.5">
            {spec.criteria.map((c) => (
              <li key={c.cid} className="flex flex-wrap items-start gap-2">
                <span
                  aria-hidden="true"
                  className={`mt-px shrink-0 font-mono text-[12px] ${
                    c.done ? 'text-green' : 'text-ink-faint'
                  }`}
                >
                  {c.done ? '✓' : '○'}
                </span>
                <span className="shrink-0 rounded border border-line bg-surface2 px-1.5 py-px font-mono text-[9.5px] text-ink-dim">
                  {c.cid}
                </span>
                <span className={`min-w-0 flex-1 ${c.done ? 'text-ink-dim' : 'text-ink'}`}>
                  {c.text}
                </span>
                {c.coveredBy.length > 0 ? (
                  c.coveredBy.map((seq) => {
                    const p = phaseBySeq.get(seq);
                    const cls =
                      p !== undefined
                        ? phaseChip(phaseStatus(p, resolvedSeqs), false).cls
                        : PHASE_CHIP.pending.cls;
                    return (
                      <span
                        key={seq}
                        data-tip={p !== undefined ? coveredByTip(seq, p.name) : undefined}
                        className={`shrink-0 rounded border px-1.5 py-px font-mono text-[9.5px] ${cls}`}
                      >
                        P{seq}
                      </span>
                    );
                  })
                ) : (
                  <span
                    data-tip={t`no phase declares it covers this criterion`}
                    className="shrink-0 rounded border border-amber/40 bg-amber/10 px-1.5 py-px font-mono text-[9.5px] text-amber"
                  >
                    <Trans>uncovered</Trans>
                  </span>
                )}
              </li>
            ))}
          </ul>
          {spec.unknownRefs.length > 0 && (
            <div className="mt-2 space-y-0.5">
              {spec.unknownRefs.map((r) => (
                <div
                  key={`${String(r.seq)}-${r.cid}`}
                  className="font-mono text-[10.5px] text-amber"
                >
                  {unknownRefLabel(r.seq, r.cid)}
                </div>
              ))}
            </div>
          )}
        </RailSection>
      )}
      {doc === null ? (
        <Loading label={t`spec…`} />
      ) : spec !== null ? (
        <RailSection label={t`doc`}>
          <Markdown text={doc} />
        </RailSection>
      ) : (
        <Markdown text={doc} />
      )}
    </>
  );
}

/** Summary tab body (complete plans only): per-phase executed work — phase
 * title + the N/N score, and what the executor reported doing (Completion
 * Report and/or the `## Execution record` doc section). The criteria
 * themselves stay on the phase docs — a summary reports work, not contract.
 * Derived
 * client-side from the existing docs endpoint (one fetch per phase doc, plus
 * plan/SUMMARY.md when the executor wrote one) — no API extension needed. */
function PlanSummary({
  epic,
  resolvedSeqs,
}: {
  epic: Epic;
  resolvedSeqs: Set<number>;
}): JSX.Element {
  const { t } = useLingui();
  const [docs, setDocs] = useState<Record<string, string> | null>(null);
  const [summaryMd, setSummaryMd] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    setDocs(null);
    setSummaryMd(null);
    void Promise.all(
      epic.phases.map(async (p) => {
        try {
          const d = await fetchPlanDoc(epic.taskId, p.docRelPath);
          return [p.docRelPath, d.content] as const;
        } catch {
          return [p.docRelPath, ''] as const; // a missing doc degrades to DTO-only data
        }
      }),
    ).then((entries) => {
      if (alive) setDocs(Object.fromEntries(entries));
    });
    if (epic.hasSummary) {
      fetchPlanDoc(epic.taskId, 'SUMMARY.md')
        .then((d) => {
          if (alive) setSummaryMd(d.content);
        })
        .catch(() => undefined); // optional artifact — skip silently
    }
    return () => {
      alive = false;
    };
  }, [epic.taskId, epic.hasSummary, epic.phases]);

  if (docs === null) return <Loading label={t`summary…`} />;

  return (
    <div className="space-y-4">
      <RailSection label={t`in plain words`}>
        <PlainSummary taskId={epic.taskId} kind="plan-report" />
      </RailSection>
      {summaryMd !== null && (
        <RailSection label={t`plan summary`}>
          <Markdown text={summaryMd} />
        </RailSection>
      )}
      {epic.phases.map((p) => {
        const doc = docs[p.docRelPath] ?? '';
        const execRecord = extractSection(doc, 'Execution record'); // i18n-ignore — the doc's own heading, matched in the markdown
        const status = phaseStatus(p, resolvedSeqs);
        const seq = p.seq;
        return (
          <section key={p.id} className="rounded-lg border border-line bg-surface/40 px-3 py-2.5">
            <div className="flex items-center gap-2">
              <span className="shrink-0 font-mono text-[10px] text-ink-faint">
                <Trans>Phase {seq}</Trans>
              </span>
              <span className="min-w-0 flex-1 truncate text-[13px] font-medium text-ink">
                {p.name}
              </span>
              <span
                className={`shrink-0 font-mono text-[10px] ${
                  status === 'done' ? 'text-green' : 'text-ink-faint'
                }`}
              >
                {p.checkboxesDone}/{p.checkboxesTotal || 0}
              </span>
            </div>
            {p.completionReport !== null && (
              <div className="mt-2 border-t border-line pt-2">
                <div className="mb-1 font-mono text-[10px] uppercase tracking-wider text-ink-faint">
                  <Trans>what was done</Trans>
                </div>
                <Markdown text={p.completionReport} />
              </div>
            )}
            {execRecord !== null && (
              <div className="mt-2 border-t border-line pt-2">
                <div className="mb-1 font-mono text-[10px] uppercase tracking-wider text-ink-faint">
                  <Trans>execution record</Trans>
                </div>
                <Markdown text={execRecord} />
              </div>
            )}
            {p.completionReport === null && execRecord === null && (
              <div className="mt-2 font-mono text-[10.5px] text-ink-faint">
                <Trans>no summary written for this phase</Trans>
              </div>
            )}
          </section>
        );
      })}
    </div>
  );
}
