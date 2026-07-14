// Daemon health polling (GET /api/health, every 60s), shared by the app shell
// header ("● daemon healthy") and the icon-rail footer ("v0.3 · :7777").
// `unreachable` flips true when the fetch fails so the UI can go red.

import { useEffect, useState } from 'react';
import type { HealthResponse } from '../api/types';
import { fetchHealth } from '../api';

const POLL_MS = 60_000;

/** "0.3.0" → "v0.3" (Canvas shows major.minor). */
export function shortVersion(version: string): string {
  const parts = version.split('.');
  return `v${parts.slice(0, 2).join('.')}`;
}

export interface HealthState {
  health: HealthResponse | null;
  unreachable: boolean;
}

export function useHealth(): HealthState {
  const [health, setHealth] = useState<HealthResponse | null>(null);
  const [unreachable, setUnreachable] = useState(false);

  useEffect(() => {
    let disposed = false;
    const poll = (): void => {
      fetchHealth()
        .then((h) => {
          if (disposed) return;
          setHealth(h);
          setUnreachable(false);
        })
        .catch(() => {
          if (!disposed) setUnreachable(true);
        });
    };
    poll();
    const timer = setInterval(poll, POLL_MS);
    return () => {
      disposed = true;
      clearInterval(timer);
    };
  }, []);

  return { health, unreachable };
}
