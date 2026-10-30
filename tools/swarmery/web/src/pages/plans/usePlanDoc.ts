import { t } from '@lingui/core/macro';
import { useEffect, useState } from 'react';
import { fetchPlanDoc } from '../../api';

/** Lazily fetches one plan doc; errors fold into the returned text (same
 * behaviour the old details modal had). `version` re-triggers the fetch when
 * the doc changes server-side (docUpdatedAt from a plan_updated refetch). The
 * setter lets a local write (checkbox toggle, Save) adopt the server's response
 * without waiting for the next refetch. */
export function usePlanDoc(
  taskId: number,
  path: string,
  version: string | null,
): [string | null, (text: string) => void] {
  const [text, setText] = useState<string | null>(null);
  useEffect(() => {
    let alive = true;
    setText(null);
    fetchPlanDoc(taskId, path)
      .then((d) => {
        if (alive) setText(d.content);
      })
      .catch((e: unknown) => {
        if (alive) {
          const reason = e instanceof Error ? e.message : String(e);
          setText(t`failed to load ${path}: ${reason}`);
        }
      });
    return () => {
      alive = false;
    };
  }, [taskId, path, version]);
  return [text, setText];
}
