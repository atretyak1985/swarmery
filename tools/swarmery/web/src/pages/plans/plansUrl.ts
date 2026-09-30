// Plans deep links (plans-deep-links phase 1): the URL IS the Plans place's
// selection. One pure module owns the scheme so Plans, the link producers and
// the tests cannot disagree about it:
//
//   /p/<slug>/plans[?status=done|archived]
//   /p/<slug>/plans/<externalId>
//   /p/<slug>/plans/<externalId>/phase/<seq>[/<phaseTab>]        (story omitted)
//   /p/<slug>/plans/<externalId>/details/<planTab>
//   /p/<slug>/plans/<externalId>/details/revisions/<revId>
//   legacy, permanent: /p/<slug>/plans?task=<id>|?plan=<id>[&phase=<seq>][&tab=revisions]
//
// The plan is addressed by `externalId` (survives a DB rebuild), the phase by
// `seq` (phase row ids churn on every rescan). Link producers outside Plans
// (board cards, TaskBrief, PlanRunCard, PlanningMode) build their hrefs here
// too — `plansPath` when they know the externalId, `plansTaskHref` (the
// numeric hand-off) when they only know the task id. Every search param this module
// does not own — notably ScopeProvider's `?scope=` and PlansPlace's own
// `?tab=new|board|playbooks` — is carried through untouched.

import type { PhaseTab } from './PhaseDrawer';

export type PlansStatus = 'active' | 'done' | 'archived';

/** Plan-details tab ids. Spec exists only on plans with a spec.md; Summary
 * only on complete plans. */
export type PlanDetailTab = 'plan' | 'spec' | 'summary' | 'revisions' | 'edit';

export const PLANS_STATUSES: readonly PlansStatus[] = ['active', 'done', 'archived'];
export const PHASE_TAB_IDS: readonly PhaseTab[] = ['story', 'criteria', 'runs', 'report', 'edit'];
export const PLAN_DETAIL_TAB_IDS: readonly PlanDetailTab[] = ['plan', 'spec', 'summary', 'revisions', 'edit'];

export type PlansDetail =
  | { kind: 'phase'; seq: number; tab: PhaseTab }
  | { kind: 'plan'; tab: PlanDetailTab; revId?: number };

/** One addressable state of the Plans place. `status` only matters while no
 * plan is named (a plan's own status decides its filter). */
export interface PlansTarget {
  plan: string | null;
  status?: PlansStatus;
  detail?: PlansDetail;
}

/** The child route paths under `/p/:slug` that render the Plans place. Shared
 * by main.tsx and the tests so both mount the exact same params. */
export const PLANS_ROUTE_PATHS: readonly string[] = [
  'plans',
  'plans/:plan',
  'plans/:plan/phase/:seq',
  'plans/:plan/phase/:seq/:phaseTab',
  'plans/:plan/details/:planTab',
  'plans/:plan/details/revisions/:revId',
];

/** The route params those paths produce (already URI-decoded by the router). */
export interface PlansRouteParams {
  plan?: string | undefined;
  seq?: string | undefined;
  phaseTab?: string | undefined;
  planTab?: string | undefined;
  revId?: string | undefined;
}

/** Search params this module owns. `tab` is owned only when it is the legacy
 * `revisions` hand-off — any other `?tab=` belongs to PlansPlace. */
const OWNED_PARAMS = ['status', 'task', 'plan', 'phase'] as const;

/** The path (no query) of a target. */
function pathOf(slug: string, target: PlansTarget): string {
  let path = `/p/${encodeURIComponent(slug)}/plans`;
  if (target.plan === null) return path;
  path += `/${encodeURIComponent(target.plan)}`;
  const d = target.detail;
  if (d === undefined) return path;
  if (d.kind === 'phase') {
    path += `/phase/${String(d.seq)}`;
    if (d.tab !== 'story') path += `/${d.tab}`;
    return path;
  }
  path += `/details/${d.tab}`;
  if (d.tab === 'revisions' && d.revId !== undefined) path += `/${String(d.revId)}`;
  return path;
}

/** `?status=` belongs on the URL only while no plan is named, and never for
 * the default (active) filter. */
function statusOf(target: PlansTarget): PlansStatus | null {
  return target.plan === null && target.status !== undefined && target.status !== 'active'
    ? target.status
    : null;
}

/** Canonical href of a target, with no other search params. */
export function plansPath(slug: string, target: PlansTarget): string {
  const path = pathOf(slug, target);
  const status = statusOf(target);
  return status !== null ? `${path}?status=${status}` : path;
}

/**
 * Href of a target, MERGED with the current search: every param this module
 * does not own survives (`?scope=`, PlansPlace's `?tab=new|board|playbooks`);
 * the owned ones (`status`, `task`, `plan`, `phase`, and `tab` only while it is
 * `revisions`) are dropped and `status` re-set from the target.
 */
export function plansHref(slug: string, target: PlansTarget, currentSearch = ''): string {
  const q = new URLSearchParams(currentSearch);
  for (const k of OWNED_PARAMS) q.delete(k);
  if (q.get('tab') === 'revisions') q.delete('tab');
  const status = statusOf(target);
  if (status !== null) q.set('status', status);
  const qs = q.toString();
  return qs === '' ? pathOf(slug, target) : `${pathOf(slug, target)}?${qs}`;
}

/**
 * Href of the permanent numeric entry point, for producers that know a plan's
 * task id but not its `externalId` (a plan run's session rows, the Planning
 * wizard before its task summary loads). Plans resolves it and replaces to the
 * canonical path: `?task=<id>[&phase=<seq>][&tab=revisions]`.
 */
export function plansTaskHref(
  slug: string,
  taskId: number,
  opts: { phase?: number; revisions?: boolean } = {},
): string {
  const q = new URLSearchParams({ task: String(taskId) });
  if (opts.phase !== undefined) q.set('phase', String(opts.phase));
  if (opts.revisions === true) q.set('tab', 'revisions');
  return `${pathOf(slug, { plan: null })}?${q.toString()}`;
}

/** The numeric hand-off (`?task=` / `?plan=`), resolved against the epics by
 * the caller. `taskId` is NaN for a non-numeric id (resolves to nothing). */
export interface LegacyPlansLink {
  taskId: number;
  phaseSeq: number | null;
  wantRevisions: boolean;
}

export interface ParsedPlansRoute {
  plan: string | null;
  /** `?status=` as written (null when absent or unknown). */
  status: PlansStatus | null;
  /** The detail the URL names, with malformed parts degraded to the nearest
   * valid level: an unknown tab string reads as the default tab, a
   * non-numeric seq as "no phase", a non-numeric revId as "no revision". */
  detail: PlansDetail | null;
  /** A tab / seq / revId segment was present but malformed — the caller
   * canonicalises by replacing to the degraded target. */
  invalidTab: boolean;
  /** `?task=` / `?plan=` on a plan-less path; null otherwise. */
  legacy: LegacyPlansLink | null;
}

function positiveInt(raw: string | undefined | null): number | null {
  if (raw === undefined || raw === null || !/^\d+$/.test(raw)) return null;
  const n = Number(raw);
  return Number.isSafeInteger(n) ? n : null;
}

function isOneOf<T extends string>(ids: readonly T[], raw: string): raw is T {
  return (ids as readonly string[]).includes(raw);
}

/** Parse the route params + search of a Plans URL. Pure. */
export function parsePlansRoute(params: PlansRouteParams, search: string): ParsedPlansRoute {
  const q = new URLSearchParams(search);
  const rawStatus = q.get('status');
  const status = rawStatus !== null && isOneOf(PLANS_STATUSES, rawStatus) ? rawStatus : null;
  const plan = params.plan !== undefined && params.plan !== '' ? params.plan : null;

  if (plan === null) {
    const rawTask = q.get('task') ?? q.get('plan');
    const legacy: LegacyPlansLink | null =
      rawTask === null
        ? null
        : {
            taskId: positiveInt(rawTask) ?? Number.NaN,
            phaseSeq: positiveInt(q.get('phase')),
            wantRevisions: q.get('tab') === 'revisions',
          };
    return { plan: null, status, detail: null, invalidTab: false, legacy };
  }

  let detail: PlansDetail | null = null;
  let invalidTab = false;
  if (params.seq !== undefined) {
    const seq = positiveInt(params.seq);
    if (seq === null) {
      invalidTab = true;
    } else {
      const raw = params.phaseTab;
      const tab = raw === undefined ? 'story' : isOneOf(PHASE_TAB_IDS, raw) ? raw : null;
      if (tab === null) invalidTab = true;
      detail = { kind: 'phase', seq, tab: tab ?? 'story' };
    }
  } else if (params.revId !== undefined) {
    const revId = positiveInt(params.revId);
    if (revId === null) invalidTab = true;
    detail = revId === null ? { kind: 'plan', tab: 'revisions' } : { kind: 'plan', tab: 'revisions', revId };
  } else if (params.planTab !== undefined) {
    const raw = params.planTab;
    const tab = isOneOf(PLAN_DETAIL_TAB_IDS, raw) ? raw : null;
    if (tab === null) invalidTab = true;
    detail = { kind: 'plan', tab: tab ?? 'plan' };
  }
  return { plan, status, detail, invalidTab, legacy: null };
}

/** Two hrefs name the same location (tolerates encoding differences, so a
 * correction can never loop on `%7E` vs `~`). */
export function sameHref(a: string, b: string): boolean {
  if (a === b) return true;
  const dec = (s: string): string => {
    try {
      return decodeURIComponent(s);
    } catch {
      return s;
    }
  };
  return dec(a) === dec(b);
}
