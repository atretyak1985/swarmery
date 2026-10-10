// "Run now" — the Agent Hub's dispatch shortcut. Per the phase spec it lands on
// a project board with the create modal open and prefilled `@<agent>: ` (Board
// reads ?compose= and seeds NewTaskButton with it; the modal then resolves the
// "@name:" prefix into its agent picker).
// When a project scope is active (workspace mount, or the fleet scope switcher
// is set) it navigates straight there; otherwise it opens a small project
// picker first (the spec's "ask to pick a project" step), reusing the shared
// projects list from the scope store.

import { Trans, useLingui } from '@lingui/react/macro';
import { useEffect, useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { displaySlug } from '../../lib/projectSlug';
import { useScope } from '../../lib/scope';

/** Build the board deep-link that opens the create modal on `@<agent>: `. */
function composeHref(slug: string, agentName: string): string {
  const prompt = `@${agentName}: `;
  return `/p/${encodeURIComponent(slug)}/board?compose=${encodeURIComponent(prompt)}`;
}

export function RunNowButton({
  agentName,
  scopeSlug,
}: {
  agentName: string;
  /** Active project scope (workspace slug or fleet scope); null = fleet, unscoped. */
  scopeSlug: string | null;
}): JSX.Element {
  const { t } = useLingui();
  const navigate = useNavigate();
  const { projects } = useScope();
  const [picking, setPicking] = useState(false);
  const wrapRef = useRef<HTMLDivElement>(null);

  // Dismiss the picker on an outside click / Escape.
  useEffect(() => {
    if (!picking) return;
    const onDown = (e: MouseEvent): void => {
      if (wrapRef.current !== null && !wrapRef.current.contains(e.target as Node)) setPicking(false);
    };
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') setPicking(false);
    };
    window.addEventListener('mousedown', onDown);
    window.addEventListener('keydown', onKey);
    return () => {
      window.removeEventListener('mousedown', onDown);
      window.removeEventListener('keydown', onKey);
    };
  }, [picking]);

  const go = (slug: string): void => {
    setPicking(false);
    navigate(composeHref(slug, agentName));
  };

  const onClick = (): void => {
    if (scopeSlug !== null && scopeSlug !== '') {
      go(scopeSlug);
      return;
    }
    // Fleet, unscoped: a single project short-circuits the picker.
    if (projects.length === 1 && projects[0] !== undefined) {
      go(displaySlug(projects[0], projects));
      return;
    }
    setPicking((v) => !v);
  };

  return (
    <div ref={wrapRef} className="relative">
      <button
        type="button"
        onClick={onClick}
        className="rounded-lg border border-brand/40 bg-brand/10 px-3 py-1.5 text-[12px] font-semibold text-brand transition-colors hover:bg-brand/20"
        data-tip={t`prefill a new board task with @${agentName}:`}
      >
        <Trans>▸ Run now</Trans>
      </button>
      {picking && (
        <div className="absolute right-0 z-30 mt-1 max-h-[280px] w-[220px] overflow-y-auto rounded-lg border border-line-strong bg-surface py-1 shadow-lg">
          <div className="px-3 py-1.5 font-mono text-[10px] tracking-[0.1em] text-ink-faint uppercase">
            <Trans>pick a project</Trans>
          </div>
          {projects.length === 0 && (
            <div className="px-3 py-2 font-mono text-[11px] text-ink-dim">
              <Trans>no projects</Trans>
            </div>
          )}
          {projects.map((p) => (
            <button
              key={p.slug}
              type="button"
              onClick={() => go(displaySlug(p, projects))}
              className="block w-full px-3 py-1.5 text-left text-[12.5px] text-ink-dim transition-colors hover:bg-surface2 hover:text-ink"
            >
              {p.name ?? p.slug}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
