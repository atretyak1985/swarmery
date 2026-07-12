// phase 3.5: workspaces — the task chip. Shows which workspace card a
// session worked on (external_id = yyyy-mm-dd-slug). Heuristic links render
// with a dashed border (design §7.4: dotted until a human confirms); the
// chip carries no link target yet (E-lite — no Workspaces screen).

import type { TaskLinkSource } from '../api/types';

export function TaskChip({
  externalId,
  linkSource,
  confidence,
}: {
  externalId: string;
  linkSource?: TaskLinkSource | null | undefined;
  confidence?: number | null | undefined;
}): JSX.Element {
  const heuristic = linkSource === 'heuristic';
  const title = heuristic
    ? `linked heuristically${confidence != null ? ` · ${String(Math.round(confidence * 100))}% overlap` : ''}`
    : 'linked via the task card (logs/sessions.md)';
  return (
    <span
      title={title}
      className={`inline-flex max-w-full min-w-0 items-center gap-1 rounded-md border px-1.5 py-[1px] font-mono text-[10px] text-ink-3 ${
        heuristic ? 'border-dashed border-ink-dim/60' : 'border-line bg-surface2'
      }`}
    >
      <span aria-hidden="true" className="text-brand">
        ⧉
      </span>
      <span className="truncate">{externalId}</span>
      {heuristic && confidence != null && (
        <span className="text-ink-dim">~{String(Math.round(confidence * 100))}%</span>
      )}
    </span>
  );
}
