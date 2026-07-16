// Shared headless project dropdown ("● all projects ▾") — extracted from the
// Sessions filter so the global header scope switcher renders the exact same
// control. Optional grouping (groupByTag) renders pinned projects first, then
// one group per tag (a project appears under each of its tags), then the
// untagged rest — the layout the global switcher uses.

import { useEffect, useRef, useState } from 'react';
import type { Project } from '../api/types';
import { projectColor } from '../lib/colors';
import { projectLabel } from '../lib/format';

interface ProjectGroup {
  /** Group eyebrow; null = no header (flat list). */
  label: string | null;
  projects: Project[];
}

/** Pinned first, then per-tag groups (alphabetical), then the untagged rest. */
function groupProjects(projects: Project[]): ProjectGroup[] {
  const pinned = projects.filter((p) => p.pinned);
  const rest = projects.filter((p) => !p.pinned);
  const tags = [...new Set(rest.flatMap((p) => p.tags))].sort();
  const groups: ProjectGroup[] = [];
  if (pinned.length > 0) groups.push({ label: 'pinned', projects: pinned });
  for (const tag of tags) {
    groups.push({ label: tag, projects: rest.filter((p) => p.tags.includes(tag)) });
  }
  const untagged = rest.filter((p) => p.tags.length === 0);
  if (untagged.length > 0) {
    groups.push({ label: groups.length > 0 ? 'other' : null, projects: untagged });
  }
  return groups;
}

export function ProjectDropdown({
  projects,
  value,
  onChange,
  allLabel = 'all projects',
  groupByTag = false,
}: {
  projects: Project[];
  /** Selected project slug, or null = all projects. */
  value: string | null;
  onChange: (slug: string | null) => void;
  /** Trigger/menu label of the null option ("all projects" / "All projects"). */
  allLabel?: string;
  /** Group the menu by pinned/tags (global scope switcher). */
  groupByTag?: boolean;
}): JSX.Element {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const buttonRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);

  // Escape closes (restoring focus to the trigger); outside click closes.
  useEffect(() => {
    if (!open) return undefined;
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

  const select = (slug: string | null): void => {
    onChange(slug);
    setOpen(false);
    buttonRef.current?.focus();
  };

  const selected = value !== null ? (projects.find((p) => p.slug === value) ?? null) : null;
  // Deep-linked slug not in /api/projects yet — show the raw slug, keep the filter.
  const label =
    value === null ? allLabel : selected !== null ? projectLabel(selected.name, selected.slug) : value;
  const groups = groupByTag ? groupProjects(projects) : [{ label: null, projects }];

  return (
    <div ref={rootRef} className="relative shrink-0">
      <button
        ref={buttonRef}
        type="button"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-label="filter by project"
        onClick={() => setOpen((v) => !v)}
        onKeyDown={(e) => {
          if (e.key === 'ArrowDown' && open) {
            e.preventDefault();
            focusOption(1);
          }
        }}
        className="flex max-w-[200px] items-center gap-1.5 rounded-full border border-line-strong px-[11px] py-[5px] font-mono text-[10.5px] whitespace-nowrap text-ink-dim transition-colors hover:text-ink aria-expanded:border-[#4a4e58] aria-expanded:bg-surface2 aria-expanded:text-ink"
      >
        <span className="truncate" style={value !== null ? { color: projectColor(value) } : undefined}>
          {label}
        </span>
        <span aria-hidden="true" className="text-[9px] text-ink-faint">
          ▾
        </span>
      </button>
      {open && (
        <div
          ref={menuRef}
          role="listbox"
          aria-label="project"
          onKeyDown={(e) => {
            if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
              e.preventDefault();
              focusOption(e.key === 'ArrowDown' ? 1 : -1);
            }
          }}
          className="absolute top-full left-0 z-20 mt-1.5 max-h-[60vh] min-w-[210px] overflow-y-auto rounded-[11px] border border-line-strong bg-field shadow-[0_16px_34px_rgba(0,0,0,0.5)]"
        >
          <DropdownOption
            selected={value === null}
            label={allLabel}
            onSelect={() => select(null)}
          />
          {groups.map((g, gi) => (
            <div key={g.label ?? `group-${String(gi)}`}>
              {g.label !== null && (
                <div className="border-t border-line px-3 pt-2 pb-1 font-mono text-[9.5px] tracking-[0.14em] text-ink-faint uppercase">
                  {g.label}
                </div>
              )}
              {g.projects.map((p) => (
                <DropdownOption
                  key={`${g.label ?? ''}:${String(p.id)}`}
                  selected={value === p.slug}
                  label={projectLabel(p.name, p.slug)}
                  labelColor={projectColor(p.slug)}
                  onSelect={() => select(p.slug)}
                />
              ))}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function DropdownOption({
  selected,
  label,
  labelColor,
  onSelect,
}: {
  selected: boolean;
  label: string;
  /** Color the option label (project rows); omit for "all projects". */
  labelColor?: string;
  onSelect: () => void;
}): JSX.Element {
  return (
    <button
      type="button"
      role="option"
      aria-selected={selected}
      onClick={onSelect}
      className={`flex w-full items-center gap-2 px-3 py-2 text-left font-mono text-[11px] transition-colors hover:bg-surface2 ${
        selected ? 'bg-surface2 text-ink' : 'text-ink-3'
      }`}
    >
      <span
        className="min-w-0 flex-1 truncate"
        style={labelColor !== undefined ? { color: labelColor } : undefined}
      >
        {label}
      </span>
      {selected && <span aria-hidden="true">✓</span>}
    </button>
  );
}
