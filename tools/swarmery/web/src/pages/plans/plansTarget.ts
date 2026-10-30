// What a Plans URL resolves to once the epics are loaded: the canonical target
// (plansUrl.ts parses, this judges against the data), the notice for a link whose
// target is gone, and the browser-tab title of what is open.

import { i18n, type MessageDescriptor } from '@lingui/core';
import { msg, t } from '@lingui/core/macro';
import type { Epic, EpicPhase } from '../../api/types';
import { showsReviewTab } from './landingModel';
import { PHASE_TABS, type PhaseTab } from './PhasePanel';
import { computeResolvedSeqs, phaseStatus, planComplete } from './planModel';
import type { ParsedPlansRoute, PlanDetailTab, PlansDetail, PlansStatus, PlansTarget } from './plansUrl';

/** The `?status=` of the URL, and the list group a plan belongs to. */
export type EpicFilter = PlansStatus;

/** What a plan status reads as. The keys are the daemon's status codes. */
export const EPIC_STATUS_LABEL: Record<Epic['status'], MessageDescriptor> = {
  active: msg`active`,
  paused: msg`paused`,
  done: msg`done`,
  archived: msg`archived`,
};
export const FILTER_LABEL: Record<EpicFilter, MessageDescriptor> = EPIC_STATUS_LABEL;

/** Which group an epic belongs to (paused plans live with the active ones). */
export function epicFilterOf(status: Epic['status']): EpicFilter {
  return status === 'active' || status === 'paused' ? 'active' : status;
}

/** The stale-link notice for a revision id the plan does not have (SC-8). */
export function revisionNotFound(revId: number): string {
  const id = String(revId);
  return t`Revision #${id} not found`;
}

// PlanDetailTab (plan/spec/summary/revisions/edit) lives in plans/plansUrl.ts:
// it is part of the URL scheme now.

/** Phase panel tabs (story/criteria/runs/report/edit). Report always exists —
 * an empty note beats hiding it ("where do I read the summary?" was a dead end). */
export type PhaseDetailTab = PhaseTab;

/** What the inline detail panel shows: one phase's details, or the plan's (both
 * tabbed). `null` means "no details — show the phase list".
 *
 * The phase is addressed by SEQ, not by `epic_phases.id`: a plan rescan
 * (wsingest/epics.go applyEpics) DELETEs and re-INSERTs every phase row, so the
 * surrogate id churns on each rescan — and a rescan is exactly what a checkbox
 * tick triggers. Keying by id made the open panel close, or silently jump to
 * whichever phase inherited the old id. Seq is the plan's own numbering and
 * survives. */
export type DetailTarget =
  | { kind: 'phase'; seq: number; tab: PhaseDetailTab }
  | { kind: 'plan'; tab: PlanDetailTab };

/** Same selection with the phase resolved against the current epic. */
export type DetailSel =
  | { kind: 'phase'; phase: EpicPhase; tab: PhaseDetailTab }
  | { kind: 'plan'; tab: PlanDetailTab };

/** Which URL-named level did not exist — named by the stale-link notice (SC-8).
 * `task` is the numeric `?task=`/`?plan=` hand-off (NaN for a non-numeric id). */
export type PlansMissing =
  | { kind: 'plan'; externalId: string }
  | { kind: 'task'; taskId: number }
  | { kind: 'phase'; seq: number; planTitle: string }
  | null;

/** The one-line notice for a link whose target is gone; null when nothing is.
 * `search` supplies a non-numeric legacy id verbatim. */
export function staleNotice(missing: PlansMissing, search: string): string | null {
  if (missing === null) return null;
  switch (missing.kind) {
    case 'plan': {
      const externalId = missing.externalId;
      return t`Plan ${externalId} not found — showing the plan list`;
    }
    case 'task': {
      const q = new URLSearchParams(search);
      const id = Number.isNaN(missing.taskId) ? (q.get('task') ?? q.get('plan') ?? '') : String(missing.taskId);
      return t`Plan #${id} not found`;
    }
    case 'phase': {
      const seq = String(missing.seq);
      const planTitle = missing.planTitle;
      return t`Phase ${seq} not found in ${planTitle}`;
    }
  }
}

/** Plan-details tab labels, as PlanDetailPanel shows them (Revisions without its count). */
export const PLAN_DETAIL_TAB_LABEL: Record<PlanDetailTab, MessageDescriptor> = {
  plan: msg`Plan`,
  plain: msg`In plain words`,
  spec: msg`Spec`,
  summary: msg`Summary`,
  revisions: msg`Revisions`,
  edit: msg`Edit`,
};

/**
 * `document.title` of the Plans tab (SC-11):
 *   phase         `Runs · Phase 3 — <plan> · <project> — Swarmery`
 *   plan details  `Revisions — <plan> · <project> — Swarmery`
 *   plan          `<plan> · Plans · <project> — Swarmery`
 *   no plan       `Plans · <project> — Swarmery`
 * A phase the plan does not have reads as the plan (the URL is about to be
 * corrected to it).
 */
export function plansTitle(epic: Epic | null, detail: DetailTarget | null, project: string): string {
  const tail = `${project} — Swarmery`;
  if (epic === null) return t`Plans · ${tail}`;
  const title = epic.title;
  if (detail?.kind === 'phase' && epic.phases.some((p) => p.seq === detail.seq)) {
    const def = PHASE_TABS.find((d) => d.id === detail.tab);
    const tab = def !== undefined ? i18n._(def.label) : detail.tab;
    const seq = String(detail.seq);
    return t`${tab} · Phase ${seq} — ${title} · ${tail}`;
  }
  if (detail?.kind === 'plan') {
    const tab = i18n._(PLAN_DETAIL_TAB_LABEL[detail.tab]);
    return `${tab} — ${title} · ${tail}`;
  }
  return t`${title} · Plans · ${tail}`;
}

/**
 * The canonical target for a parsed Plans URL against the loaded epics: the
 * numeric `?task=`/`?plan=` hand-off resolved to its externalId, the first plan
 * of the status chosen when none is named, an unknown plan / phase dropped to
 * the nearest valid level, and an unavailable tab mapped to the tab its panel
 * already falls back to (Edit on a done phase → Report; Summary on an
 * incomplete plan or Spec without a spec.md → Plan).
 *
 * Pure and deterministic in (route, epics), so replacing to it is idempotent:
 * its own output parses back to the same target. A revision id is validated
 * separately, against the revisions EpicDetail fetches.
 */
export function canonicalPlansTarget(
  route: ParsedPlansRoute,
  epics: Epic[],
): { target: PlansTarget; missing: PlansMissing } {
  const listOf = (f: EpicFilter, missing: PlansMissing): { target: PlansTarget; missing: PlansMissing } => {
    const first = epics.find((e) => epicFilterOf(e.status) === f);
    return { target: first !== undefined ? { plan: first.externalId } : { plan: null, status: f }, missing };
  };

  if (route.plan === null) {
    if (route.legacy === null) return listOf(route.status ?? 'active', null);
    const { taskId, phaseSeq, wantRevisions } = route.legacy;
    const epic = epics.find((e) => e.taskId === taskId);
    if (epic === undefined) return listOf('active', { kind: 'task', taskId });
    if (wantRevisions) {
      return { target: { plan: epic.externalId, detail: { kind: 'plan', tab: 'revisions' } }, missing: null };
    }
    if (phaseSeq === null) return { target: { plan: epic.externalId }, missing: null };
    return epic.phases.some((p) => p.seq === phaseSeq)
      ? { target: { plan: epic.externalId, detail: { kind: 'phase', seq: phaseSeq, tab: 'story' } }, missing: null }
      : { target: { plan: epic.externalId }, missing: { kind: 'phase', seq: phaseSeq, planTitle: epic.title } };
  }

  const epic = epics.find((e) => e.externalId === route.plan);
  if (epic === undefined) return listOf('active', { kind: 'plan', externalId: route.plan });
  const d = route.detail;
  if (d === null) return { target: { plan: epic.externalId }, missing: null };
  const resolvedSeqs = computeResolvedSeqs(epic.phases);
  if (d.kind === 'phase') {
    const p = epic.phases.find((x) => x.seq === d.seq);
    if (p === undefined) {
      return { target: { plan: epic.externalId }, missing: { kind: 'phase', seq: d.seq, planTitle: epic.title } };
    }
    // Edit on a done phase → Report; Review on a phase that never ran → Runs
    // (the tabs each panel falls back to).
    const tab: PhaseDetailTab =
      d.tab === 'edit' && phaseStatus(p, resolvedSeqs) === 'done'
        ? 'report'
        : d.tab === 'review' && !showsReviewTab(p)
          ? 'runs'
          : d.tab;
    return { target: { plan: epic.externalId, detail: { kind: 'phase', seq: d.seq, tab } }, missing: null };
  }
  const complete = planComplete(epic, resolvedSeqs);
  const tab: PlanDetailTab =
    (d.tab === 'summary' && !complete) || (d.tab === 'spec' && !epic.hasSpec) ? 'plan' : d.tab;
  const detail: PlansDetail =
    tab === 'revisions' && d.revId !== undefined ? { kind: 'plan', tab, revId: d.revId } : { kind: 'plan', tab };
  return { target: { plan: epic.externalId, detail }, missing: null };
}
