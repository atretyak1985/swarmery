// One project's code-host answer (GET /api/projects/{id}/vcs): provider terms
// for every landing label, and the sign-in state for the project banner.
//
// Fetched once per mount and per project switch; the daemon caches the answer
// for 60s, so a page open never spawns a CLI on every render. `reload` re-asks
// (the banner's "Re-check"). A failed fetch is `vcs: null` plus `error` — a
// caller degrades to the terms a review response carries, never to a label
// guessed from the provider.

import { useCallback, useEffect, useState } from 'react';
import { getProjectVcs } from '../api';
import type { VcsInfo } from '../api/types';

export interface ProjectVcsState {
  vcs: VcsInfo | null;
  error: string | null;
  reload: () => void;
}

export function useProjectVcs(projectId: number | null): ProjectVcsState {
  const [state, setState] = useState<{ projectId: number | null; vcs: VcsInfo | null; error: string | null }>({
    projectId: null,
    vcs: null,
    error: null,
  });
  const [nonce, setNonce] = useState(0);

  useEffect(() => {
    if (projectId === null) return;
    let live = true;
    getProjectVcs(projectId)
      .then((vcs) => {
        if (live) setState({ projectId, vcs, error: null });
      })
      .catch((e: unknown) => {
        if (live) setState({ projectId, vcs: null, error: e instanceof Error ? e.message : String(e) });
      });
    return () => {
      live = false;
    };
  }, [projectId, nonce]);

  const reload = useCallback(() => setNonce((n) => n + 1), []);
  // An answer for the project just left is not this project's answer.
  const current = state.projectId === projectId;
  return { vcs: current ? state.vcs : null, error: current ? state.error : null, reload };
}
