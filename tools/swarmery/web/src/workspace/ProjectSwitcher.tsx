// Workspace project switcher (fusion phase 4): the control at the top of the
// project-workspace sidebar. A trigger showing the current project, opening a
// dropdown with a search input + filtered project list + an "All projects →"
// link back to the global fleet view. Selecting a project navigates to
// /p/{slug} (preserving the current sub-route tab where possible). Distinct
// from the header ProjectDropdown (that filters fleet scope; this NAVIGATES).

import { useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import type { Project } from '../api/types';
import { projectLabel } from '../lib/format';
import { displaySlug, findProject } from '../lib/projectSlug';
import { useProjectColor } from '../lib/projectColors';
import { isOnboarded, loadOnboardedOnly, saveOnboardedOnly } from '../lib/onboardedFilter';

export function ProjectSwitcher({
  projects,
  currentSlug,
  /** Current workspace sub-tab (e.g. "board") to preserve across a switch. */
  subPath,
}: {
  projects: Project[];
  currentSlug: string;
  subPath: string;
}): JSX.Element {
  const navigate = useNavigate();
  const colorFor = useProjectColor();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');
  const [onboardedOnly, setOnboardedOnly] = useState(loadOnboardedOnly);
  const rootRef = useRef<HTMLDivElement>(null);
  const buttonRef = useRef<HTMLButtonElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);

  const current = findProject(projects, currentSlug);
  const label = current !== null ? projectLabel(current.name, current.slug) : currentSlug;

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    // The current project always stays in the list even when the onboarded-
    // only filter would otherwise drop it — this is a NAVIGATION control, and
    // hiding the project the operator is standing in from its own switcher
    // (e.g. a sub-repo demoted by the umbrella-nesting check below) would
    // strand them with no way back short of "All projects →".
    const base = onboardedOnly
      ? projects.filter((p) => p.id === current?.id || isOnboarded(p, projects))
      : projects;
    const rows = q === ''
      ? base
      : base.filter((p) =>
          [p.name, p.slug].some((v) => v != null && v.toLowerCase().includes(q)),
        );
    return [...rows].sort((a, b) => projectLabel(a.name, a.slug).localeCompare(projectLabel(b.name, b.slug)));
  }, [projects, query, onboardedOnly, current]);

  useEffect(() => {
    if (!open) return undefined;
    inputRef.current?.focus();
    const onPointerDown = (e: MouseEvent): void => {
      if (rootRef.current !== null && !rootRef.current.contains(e.target as Node)) setOpen(false);
    };
    const onKeyDown = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') {
        setOpen(false);
        buttonRef.current?.focus();
      }
    };
    document.addEventListener('mousedown', onPointerDown);
    document.addEventListener('keydown', onKeyDown);
    return () => {
      document.removeEventListener('mousedown', onPointerDown);
      document.removeEventListener('keydown', onKeyDown);
    };
  }, [open]);

  const focusOption = (delta: 1 | -1): void => {
    const options = menuRef.current?.querySelectorAll<HTMLButtonElement>('[role="option"]');
    if (options === undefined || options.length === 0) return;
    const list = Array.from(options);
    const idx = list.indexOf(document.activeElement as HTMLButtonElement);
    const next = list[(idx + delta + list.length) % list.length];
    next?.focus();
  };

  const go = (slug: string): void => {
    setOpen(false);
    setQuery('');
    navigate(`/p/${slug}${subPath}`);
  };

  return (
    <div ref={rootRef} className="relative">
      <button
        ref={buttonRef}
        type="button"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-label="switch project"
        onClick={() => setOpen((v) => !v)}
        onKeyDown={(e) => {
          if (e.key === 'ArrowDown' && open) {
            e.preventDefault();
            focusOption(1);
          }
        }}
        className="flex w-full items-center gap-2 rounded-[10px] border border-line-strong bg-field px-3 py-2 text-left transition-colors hover:border-line-strong hover:bg-surface2 aria-expanded:border-[#4a4e58] aria-expanded:bg-surface2"
      >
        <span
          aria-hidden="true"
          className="h-[8px] w-[8px] shrink-0 rounded-full"
          style={{ backgroundColor: colorFor(current?.slug ?? currentSlug) }}
        />
        <span className="min-w-0 flex-1 truncate text-[13px] font-semibold text-ink">{label}</span>
        <span aria-hidden="true" className="text-[9px] text-ink-faint">
          ▾
        </span>
      </button>
      {open && (
        <div
          ref={menuRef}
          className="absolute top-full left-0 z-30 mt-1.5 max-h-[70vh] w-full min-w-[220px] overflow-hidden rounded-[11px] border border-line-strong bg-field shadow-[0_16px_34px_rgba(0,0,0,0.5)]"
        >
          <div className="border-b border-line p-2">
            <input
              ref={inputRef}
              type="text"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'ArrowDown') {
                  e.preventDefault();
                  focusOption(1);
                }
              }}
              placeholder="search projects…"
              aria-label="search projects"
              className="w-full rounded-[8px] border border-line bg-surface px-2.5 py-1.5 font-mono text-[11px] text-ink outline-none placeholder:text-ink-faint focus:border-ink-dim"
            />
          </div>
          <div
            role="listbox"
            aria-label="projects"
            onKeyDown={(e) => {
              if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
                e.preventDefault();
                focusOption(e.key === 'ArrowDown' ? 1 : -1);
              }
            }}
            className="max-h-[46vh] overflow-y-auto py-1"
          >
            {filtered.length === 0 ? (
              <div className="px-3 py-2 font-mono text-[11px] text-ink-faint">no match</div>
            ) : (
              filtered.map((p) => (
                <button
                  key={p.id}
                  type="button"
                  role="option"
                  aria-selected={p.id === current?.id}
                  onClick={() => go(displaySlug(p, projects))}
                  className={`flex w-full items-center gap-2 px-3 py-2 text-left font-mono text-[11px] transition-colors hover:bg-surface2 ${
                    p.id === current?.id ? 'bg-surface2 text-ink' : 'text-ink-3'
                  }`}
                >
                  <span
                    aria-hidden="true"
                    className="h-[7px] w-[7px] shrink-0 rounded-full"
                    style={{ backgroundColor: colorFor(p.slug) }}
                  />
                  <span className="min-w-0 flex-1 truncate">{projectLabel(p.name, p.slug)}</span>
                  {p.id === current?.id && <span aria-hidden="true">✓</span>}
                </button>
              ))
            )}
          </div>
          <label className="flex w-full cursor-pointer items-center gap-1.5 border-t border-line px-3 py-1.5 font-mono text-[10px] text-ink-faint transition-colors hover:text-ink-dim">
            <input
              type="checkbox"
              checked={onboardedOnly}
              onChange={(e) => {
                setOnboardedOnly(e.target.checked);
                saveOnboardedOnly(e.target.checked);
              }}
              className="accent-brand"
            />
            onboarded only
          </label>
          <button
            type="button"
            onClick={() => {
              setOpen(false);
              navigate('/projects');
            }}
            className="flex w-full items-center gap-2 border-t border-line px-3 py-2 text-left font-mono text-[11px] text-ink-dim transition-colors hover:bg-surface2 hover:text-ink"
          >
            All projects →
          </button>
        </div>
      )}
    </div>
  );
}
