// The presentational half of a run branch's diff: the branch/base line, the
// commits, the per-file line counts and the unified patch split per file. No
// fetching and no chrome — the board card's collapsible panel
// (workspace/TaskDiff.tsx) and a plan phase's Review tab
// (pages/plans/PhaseReview.tsx) both wrap it, over the same TaskDiff shape
// (the phase review endpoint reuses the board diff's reader).
//
// The server returns ONE unified patch, not a patch per file; splitting it on
// `diff --git` boundaries client-side keeps the endpoint's contract simple and
// costs nothing here — the text is already in memory and capped at 200 KB.

import { useState } from 'react';
import type { TaskDiff } from '../api/types';

/** One file's slice of the unified patch. */
export interface PatchSection {
  path: string;
  body: string;
}

/**
 * Splits a unified diff into per-file sections. The header line is
 * `diff --git a/<path> b/<path>`; the second path is the one that matters (a
 * rename's `b/` side is where the file ended up). A patch that does not start
 * with a header — the leading slice of a truncated diff, or an unfamiliar
 * format — is kept whole under an empty path rather than silently dropped.
 */
export function splitPatch(patch: string): PatchSection[] {
  if (patch.trim() === '') return [];
  const lines = patch.split('\n');
  const out: PatchSection[] = [];
  let current: PatchSection | null = null;
  for (const line of lines) {
    if (line.startsWith('diff --git ')) {
      if (current !== null) out.push(current);
      const m = /^diff --git a\/(.+) b\/(.+)$/.exec(line);
      current = { path: m?.[2] ?? line.slice('diff --git '.length), body: line };
      continue;
    }
    if (current === null) {
      current = { path: '', body: line };
      continue;
    }
    current.body += `\n${line}`;
  }
  if (current !== null) out.push(current);
  return out;
}

function StatCount({ additions, deletions }: { additions: number; deletions: number }): JSX.Element {
  return (
    <span className="shrink-0 font-mono text-[10.5px] tabular-nums">
      <span className="text-green">+{additions}</span>{' '}
      <span className="text-red">−{deletions}</span>
    </span>
  );
}

function PatchBlock({ section }: { section: PatchSection }): JSX.Element {
  const [open, setOpen] = useState(false);
  return (
    <div className="border-t border-line/60 first:border-t-0">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        className="flex w-full items-center gap-1.5 py-1 text-left font-mono text-[10.5px] text-ink-2 transition-colors hover:text-ink"
      >
        <span aria-hidden="true" className="w-2 shrink-0 text-ink-faint">
          {open ? '▾' : '▸'}
        </span>
        <span className="min-w-0 flex-1 truncate">{section.path === '' ? '(patch)' : section.path}</span>
      </button>
      {open && (
        <pre className="mb-1.5 max-h-80 overflow-auto rounded-md border border-line bg-field px-2 py-1.5 font-mono text-[10.5px] leading-relaxed text-ink-2">
          {section.body}
        </pre>
      )}
    </div>
  );
}

/** A run branch's diff, rendered from already-fetched data. */
export function DiffView({ diff }: { diff: TaskDiff }): JSX.Element {
  const sections = splitPatch(diff.patch);
  return (
    <>
      <div className="pb-1.5 font-mono text-[10px] text-ink-faint">
        {diff.branch} · base {diff.base.slice(0, 10)}
      </div>

      {diff.commits.length === 0 ? (
        <div className="font-mono text-[10.5px] text-ink-faint">
          the run branch carries no commits ahead of its start point — the agent
          changed nothing
        </div>
      ) : (
        <ul className="mb-2 flex flex-col gap-0.5">
          {diff.commits.map((c) => (
            <li key={c.sha} className="flex items-baseline gap-2 font-mono text-[10.5px]">
              <span className="shrink-0 text-ink-faint">{c.sha.slice(0, 8)}</span>
              <span className="min-w-0 flex-1 truncate text-ink-2">{c.subject}</span>
            </li>
          ))}
        </ul>
      )}

      {diff.files.length > 0 && (
        <ul className="mb-2 flex flex-col gap-0.5">
          {diff.files.map((f) => (
            <li key={f.path} className="flex items-baseline gap-2 font-mono text-[10.5px]">
              <span className="min-w-0 flex-1 truncate text-ink-2">{f.path}</span>
              <StatCount additions={f.additions} deletions={f.deletions} />
            </li>
          ))}
        </ul>
      )}

      {sections.length > 0 && <div>{sections.map((s, i) => <PatchBlock key={`${s.path}-${String(i)}`} section={s} />)}</div>}

      {diff.patchTruncated && (
        <div className="mt-1.5 font-mono text-[10px] text-ink-faint">
          diff truncated at 200 KB — open a terminal in the worktree to read the rest
        </div>
      )}
    </>
  );
}
