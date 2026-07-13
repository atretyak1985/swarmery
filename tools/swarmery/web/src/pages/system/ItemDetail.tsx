// Detail panel for agents/skills (System screen, Stage 1 read-only):
// frontmatter as a key/value table, rendered markdown body (lib/markdown —
// the same dependency-free renderer Docs.tsx uses), 30-day usage metrics,
// and version history with pick-two + Compare → GET .../diff unified-diff
// view (own lightweight line renderer, mirrors the Diffs-tab tones; no new
// dependencies). No edit/save/delete anywhere — Edit arrives in step-12.

import { useEffect, useMemo, useState } from 'react';
import type { SystemDiff, SystemItemDetail, SystemVersion } from '../../api/types';
import { fetchSystemDiff, fetchSystemItemDetail, type SystemItemsKind } from '../../api/system';
import { fmtAgo, fmtDateTime } from '../../lib/format';
import { Markdown } from '../../lib/markdown';
import { ErrorBox, Loading, SectionTitle } from '../../components/ui';
import { LintDot, OriginBadge, ScopeBadge } from './shared';

/* ----- frontmatter → table rows -----
 * The contract serves the RAW YAML block (redacted). Top-level `key: value`
 * lines become rows; indented/list continuation lines append to the previous
 * row's value. Anything unparseable falls back to a mono <pre>. */

interface FmRow {
  key: string;
  value: string;
}

function parseFrontmatter(frontmatter: string): FmRow[] | null {
  const rows: FmRow[] = [];
  for (const line of frontmatter.split('\n')) {
    if (line.trim() === '') continue;
    const top = /^([A-Za-z0-9_-]+):\s*(.*)$/.exec(line);
    if (top !== null && !line.startsWith(' ') && !line.startsWith('\t')) {
      rows.push({ key: top[1] ?? '', value: top[2] ?? '' });
      continue;
    }
    const last = rows[rows.length - 1];
    if (last === undefined) return null; // continuation before any key
    last.value = last.value === '' ? line.trim() : `${last.value}\n${line.trim()}`;
  }
  return rows.length > 0 ? rows : null;
}

/* ----- unified diff block (tones match pages/detail/Diffs.tsx) ----- */

function DiffBlock({ diff }: { diff: string }): JSX.Element {
  const lines = diff.split('\n');
  return (
    <div className="my-2 overflow-x-auto rounded-lg border border-line bg-bg py-2 font-mono text-[11px] leading-[1.6]">
      {lines.map((line, i) => {
        let tone = 'text-ink/85';
        if (line.startsWith('@@')) tone = 'text-blue';
        else if (line.startsWith('+++') || line.startsWith('---')) tone = 'text-ink-dim';
        else if (line.startsWith('+')) tone = 'bg-green/10 text-green';
        else if (line.startsWith('-')) tone = 'bg-red/10 text-red';
        return (
          // Static diff text — index keys are fine here (same as Diffs.tsx).
          <div key={i} className={`px-3 whitespace-pre ${tone}`}>
            {line === '' ? ' ' : line}
          </div>
        );
      })}
    </div>
  );
}

/* ----- version history + pick-two compare ----- */

function Versions({
  kind,
  itemId,
  versions,
  currentVersionId,
}: {
  kind: SystemItemsKind;
  itemId: number;
  versions: SystemVersion[];
  currentVersionId: number | null;
}): JSX.Element {
  const [picked, setPicked] = useState<number[]>([]);
  const [diff, setDiff] = useState<SystemDiff | null>(null);
  const [diffError, setDiffError] = useState<string | null>(null);
  const [comparing, setComparing] = useState(false);

  // Reset the picker when the panel switches to another item.
  useEffect(() => {
    setPicked([]);
    setDiff(null);
    setDiffError(null);
  }, [kind, itemId]);

  const toggle = (id: number): void => {
    setDiff(null);
    setDiffError(null);
    setPicked((prev) => {
      if (prev.includes(id)) return prev.filter((p) => p !== id);
      // Third pick replaces the oldest selection — two checkboxes max.
      return prev.length >= 2 ? [...prev.slice(1), id] : [...prev, id];
    });
  };

  const compare = (): void => {
    if (picked.length !== 2) return;
    // versions arrive newest-first; diff FROM the older TO the newer snapshot.
    const [a = 0, b = 0] = picked;
    const from = Math.min(a, b);
    const to = Math.max(a, b);
    setComparing(true);
    setDiffError(null);
    fetchSystemDiff(kind, itemId, from, to)
      .then((d) => {
        setDiff(d);
        setComparing(false);
      })
      .catch((e: unknown) => {
        setDiffError(String(e));
        setComparing(false);
      });
  };

  if (versions.length === 0) {
    return <div className="text-[12px] text-ink-dim">no versions captured yet</div>;
  }

  return (
    <div>
      <div className="overflow-hidden rounded-lg border border-line">
        {versions.map((v) => (
          <label
            key={v.id}
            className="flex cursor-pointer items-baseline gap-2.5 border-b border-line-soft px-3 py-2 transition-colors last:border-b-0 hover:bg-surface2/50"
          >
            <input
              type="checkbox"
              checked={picked.includes(v.id)}
              onChange={() => toggle(v.id)}
              className="relative top-[1px] shrink-0 accent-brand"
              aria-label={`select version ${String(v.id)} for compare`}
            />
            <span className="font-mono text-[11px] whitespace-nowrap text-ink-dim">
              {fmtDateTime(v.createdAt)}
            </span>
            <span className="min-w-0 flex-1 truncate text-[12px] text-ink-2">
              {v.changeNote ?? '—'}
            </span>
            {v.id === currentVersionId && (
              <span className="rounded-full border border-green/40 px-2 py-px font-mono text-[10px] text-green">
                current
              </span>
            )}
            <span className="font-mono text-[10px] text-ink-dim" title={v.contentHash}>
              {v.contentHash.slice(0, 8)}
            </span>
          </label>
        ))}
      </div>

      <div className="mt-2 flex items-center gap-2.5">
        <button
          type="button"
          onClick={compare}
          disabled={picked.length !== 2 || comparing}
          className="rounded-lg border border-line bg-surface px-3 py-1.5 text-[12px] font-semibold text-ink-2 transition-colors enabled:hover:bg-surface2 disabled:cursor-not-allowed disabled:opacity-40"
        >
          {comparing ? 'comparing…' : 'Compare'}
        </button>
        <span className="font-mono text-[10.5px] text-ink-dim">
          {picked.length === 2 ? 'two versions selected' : 'pick two versions to diff'}
        </span>
      </div>

      {diffError !== null && <ErrorBox message={diffError} />}
      {diff !== null &&
        (diff.diff === '' ? (
          <div className="mt-2 font-mono text-[11.5px] text-ink-dim">
            versions are identical — no content change between the snapshots
          </div>
        ) : (
          <DiffBlock diff={diff.diff} />
        ))}
    </div>
  );
}

/* ----- the panel ----- */

export function SystemItemPanel({
  kind,
  id,
  refreshKey,
  onClose,
}: {
  kind: SystemItemsKind;
  id: number;
  /** Bumped on WS system_item_updated — refetches the open detail. */
  refreshKey: number;
  onClose: () => void;
}): JSX.Element {
  const [detail, setDetail] = useState<SystemItemDetail | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    setError(null);
    fetchSystemItemDetail(kind, id)
      .then((d) => {
        if (!cancelled) setDetail(d);
      })
      .catch((e: unknown) => {
        if (!cancelled) setError(String(e));
      });
    return () => {
      cancelled = true;
    };
  }, [kind, id, refreshKey]);

  const fmRows = useMemo(
    () => (detail === null ? null : parseFrontmatter(detail.frontmatter)),
    [detail],
  );

  if (error !== null) return <ErrorBox message={error} />;
  if (detail === null) return <Loading label="detail…" />;

  return (
    <div className="rounded-xl border border-line bg-surface px-4 py-4 desk:px-5">
      <div className="flex flex-wrap items-center gap-2">
        <LintDot severity={detail.lintMax} />
        <h1 className={`font-display text-[17px] leading-tight font-bold ${detail.dead ? 'opacity-45' : ''}`}>
          {detail.name}
        </h1>
        {detail.model !== null && (
          <span className="font-mono text-[10.5px] text-ink-dim">{detail.model}</span>
        )}
        <span className="ml-auto flex items-center gap-1.5">
          <ScopeBadge scope={detail.scope} projectSlug={detail.projectSlug} />
          <OriginBadge origin={detail.origin} pluginName={detail.pluginName} />
          {detail.deleted && (
            <span className="rounded-full border border-red/40 px-2 py-px font-mono text-[10px] text-red">
              deleted
            </span>
          )}
          <button
            type="button"
            onClick={onClose}
            aria-label="close detail"
            className="ml-1 rounded-lg border border-line px-2 py-0.5 text-[12px] text-ink-dim transition-colors hover:text-ink"
          >
            ✕
          </button>
        </span>
      </div>
      <div className="mt-1 font-mono text-[10.5px] break-all text-ink-dim">{detail.path}</div>

      {/* usage metrics (30-day window, per the registry aggregates) */}
      <div className="mt-3 flex gap-6 border-b border-line pb-3 font-mono text-[11px] text-ink-dim">
        <span>
          tasks 30d <b className="font-medium text-ink">{String(detail.tasks30d)}</b>
        </span>
        <span>
          last used{' '}
          <b className="font-medium text-ink">
            {detail.lastUsed !== null ? fmtAgo(detail.lastUsed) : 'never'}
          </b>
        </span>
        <span>
          versions <b className="font-medium text-ink">{String(detail.versions.length)}</b>
        </span>
      </div>

      <SectionTitle>Frontmatter</SectionTitle>
      {fmRows !== null ? (
        <table className="w-full border-collapse">
          <tbody>
            {fmRows.map((row) => (
              <tr key={row.key}>
                <td className="w-[120px] border-b border-line-soft px-2 py-1.5 align-top font-mono text-[10.5px] tracking-[0.06em] text-ink-dim uppercase">
                  {row.key}
                </td>
                <td className="border-b border-line-soft px-2 py-1.5 align-top font-mono text-[11.5px] whitespace-pre-wrap text-ink-2">
                  {row.value === '' ? '—' : row.value}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : (
        <pre className="overflow-x-auto rounded-lg border border-line bg-bg px-3 py-2.5 font-mono text-[11px] leading-relaxed text-ink-2">
          {detail.frontmatter}
        </pre>
      )}

      <SectionTitle>Body</SectionTitle>
      <div className="text-[13px] leading-[1.65] text-ink-2">
        <Markdown text={detail.body} />
      </div>

      <SectionTitle>Versions</SectionTitle>
      <Versions
        kind={kind}
        itemId={detail.id}
        versions={detail.versions}
        currentVersionId={detail.currentVersionId}
      />
    </div>
  );
}
