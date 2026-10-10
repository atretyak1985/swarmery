// The Inbox's data: six existing fetchers, aggregated client-side (Canvas v3
// D3 — there is no inbox endpoint). Promise.allSettled, so one failing source is a
// "couldn't load <kind>" row, never a blank Inbox.
//
// Scope: under a project, approvals, advisor recommendations and the classifier
// queue narrow (their APIs take a project; a classifier question is about one
// session, so it belongs to that session's project; a plan review carries its
// plan's project slug and is filtered by it). Lessons, proposals and
// retirements are fleet-wide by nature and the page labels them so.
//
// Refetch: the shared WS stream (lib/ws.ts) on permission_* frames and
// task_updated, debounced so a burst of frames is one refetch of six calls,
// plus the reconnect/reconcile resync.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  fetchApprovals,
  fetchProjectRecommendations,
  fetchProposals,
  fetchRecommendations,
} from '../../api';
import { fetchAlerts } from '../../api/alerts';
import { fetchLabelQueue } from '../../api/decisions';
import { fetchLessons, fetchRetirements } from '../../api/lessons';
import { fetchUnackedPlanReviews } from '../../api/reviews';
import type { WSMessage } from '../../api/types';
import { useLiveUpdates } from '../../lib/ws';
import {
  attachSuggestions,
  sortItems,
  toItems,
  type InboxItem,
  type InboxKind,
  type InboxSources,
} from './inboxModel';
import { useTriage, type TriageState } from './useTriage';

export interface InboxState {
  items: InboxItem[];
  count: number;
  loading: boolean;
  /** Sources whose fetch failed on the latest load. */
  errors: InboxKind[];
  reload: () => void;
  /** The triage agent's state; empty and inert unless the hook was called `withTriage`. */
  triage: TriageState;
}

/** Kinds that stay fleet-wide under a project scope. */
export const FLEET_WIDE_KINDS: ReadonlySet<InboxKind> = new Set([
  'lesson',
  'proposal',
  'retire',
  'alert',
]);

const REFETCH_DEBOUNCE_MS = 400;

function value<T>(r: PromiseSettledResult<T>): T | undefined {
  return r.status === 'fulfilled' ? r.value : undefined;
}

async function loadSources(scope: string | null): Promise<{ src: InboxSources; errors: InboxKind[] }> {
  const [approvals, lessons, recs, proposals, classifier, retirements, alerts, reviews] = await Promise.allSettled([
    fetchApprovals('pending', scope),
    fetchLessons('candidate'),
    scope === null ? fetchRecommendations('proposed') : fetchProjectRecommendations(scope, 'proposed'),
    fetchProposals('proposed,needs_target'),
    fetchLabelQueue('all', scope),
    fetchRetirements(),
    fetchAlerts(),
    fetchUnackedPlanReviews(),
  ]);
  const errors: InboxKind[] = [];
  const settled: [PromiseSettledResult<unknown>, InboxKind][] = [
    [approvals, 'approval'],
    [lessons, 'lesson'],
    [recs, 'advisor'],
    [proposals, 'proposal'],
    [classifier, 'classifier'],
    [retirements, 'retire'],
    [alerts, 'alert'],
    [reviews, 'review'],
  ];
  for (const [r, kind] of settled) if (r.status === 'rejected') errors.push(kind);
  return {
    src: {
      approvals: value(approvals),
      lessons: value(lessons),
      recommendations: value(recs)?.recommendations,
      proposals: value(proposals)?.proposals,
      classifier: value(classifier),
      retirements: value(retirements),
      alerts: value(alerts),
      // A plan belongs to one project: under a project scope only its reviews show.
      reviews: value(reviews)?.filter((r) => scope === null || r.projectSlug === scope),
    },
    errors,
  };
}

/**
 * `withTriage` (the Inbox page) adds the triage agent's state and attaches its
 * open suggestions to the items. Without it the hook makes no triage requests,
 * so the sidebar badge and the Today page stay as cheap as before.
 */
export function useInboxItems(scope: string | null, withTriage = false): InboxState {
  const [rawItems, setItems] = useState<InboxItem[]>([]);
  const [errors, setErrors] = useState<InboxKind[]>([]);
  const [loading, setLoading] = useState(true);
  // Drops a slow response that lands after a newer load (or a scope switch).
  const generation = useRef(0);

  const reload = useCallback((): void => {
    generation.current += 1;
    const mine = generation.current;
    void loadSources(scope).then(({ src, errors: failed }) => {
      if (mine !== generation.current) return;
      setItems(sortItems(toItems(src)));
      setErrors(failed);
      setLoading(false);
    });
  }, [scope]);

  useEffect(() => {
    setLoading(true);
    reload();
  }, [reload]);

  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(
    () => () => {
      if (timer.current !== null) clearTimeout(timer.current);
    },
    [],
  );
  const onMessage = useCallback(
    (msg: WSMessage): void => {
      if (
        msg.type !== 'permission_requested' &&
        msg.type !== 'permission_resolved' &&
        msg.type !== 'task_updated' &&
        // A recorded plan review nudges its plan the way a phase run does.
        msg.type !== 'plan_updated'
      ) {
        return;
      }
      if (timer.current !== null) clearTimeout(timer.current);
      timer.current = setTimeout(reload, REFETCH_DEBOUNCE_MS);
    },
    [reload],
  );
  useLiveUpdates(onMessage, reload);

  // A finished run changed what is open: refetch the items too.
  const triage = useTriage(scope, reload, withTriage);
  const items = useMemo(() => attachSuggestions(rawItems, triage.open), [rawItems, triage.open]);

  return { items, count: items.length, loading, errors, reload, triage };
}
