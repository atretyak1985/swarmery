// One project's code-host answer (GET /api/projects/{id}/vcs): provider terms
// for every landing label, and the sign-in state for the project banner.
//
// Fetched once per mount and per project switch; the daemon caches the answer
// for 60s, so a page open never spawns a CLI on every render. `reload` re-asks
// past that cache (the banner's "Re-check"). A failed fetch is `vcs: null` plus `error` — a
// caller degrades to the terms a review response carries, never to a label
// guessed from the provider.

import { useCallback, useEffect, useRef, useState } from 'react';
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
  // Set by `reload`, consumed by the fetch it triggers: a Re-check bypasses the
  // daemon's 60s cache (the operator just signed in), a project switch does not.
  const freshNext = useRef(false);

  useEffect(() => {
    if (projectId === null) return;
    let live = true;
    const fresh = freshNext.current;
    freshNext.current = false;
    getProjectVcs(projectId, fresh)
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

  const reload = useCallback(() => {
    freshNext.current = true;
    setNonce((n) => n + 1);
  }, []);
  // An answer for the project just left is not this project's answer.
  const current = state.projectId === projectId;
  return { vcs: current ? state.vcs : null, error: current ? state.error : null, reload };
}
