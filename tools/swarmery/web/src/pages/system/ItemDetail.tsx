// Detail panel for agents/skills (System screen): frontmatter as a key/value
// table, rendered markdown body (lib/markdown — the same dependency-free
// renderer Docs.tsx uses), 30-day usage metrics, and version history with
// pick-two + Compare → GET .../diff unified-diff view. Stage 2 (step-12) adds
// the write surface: raw-md editor (textarea + Preview tab, NO new deps) with
// base_hash plumbing — the hash of the CURRENT version is captured the moment
// Edit opens, so a save always races honestly against disk (409 → conflict
// banner with the base→disk diff; resolved ONLY by an explicit reload, no
// force mode exists) — plus per-version rollback with an on-disk diff
// preview, soft delete/restore (agents), and plugin/readonly guards.

import { plural, t } from '@lingui/core/macro';
import { Trans, useLingui } from '@lingui/react/macro';
import { useEffect, useState } from 'react';
import type { ReactNode } from 'react';
import { Link } from 'react-router-dom';
import type {
  AgentHistory,
  SystemConflict,
  SystemDiff,
  SystemItemDetail,
  SystemLintFinding,
  SystemVersion,
} from '../../api/types';
import {
  currentContentHash,
  deleteSystemAgent,
  fetchAgentHistory,
  fetchSystemDiff,
  fetchSystemItemDetail,
  putSystemItem,
  restoreSystemAgent,
  rollbackSystemItem,
  SystemWriteError,
  type SystemItemsKind,
} from '../../api/system';
import { fmtAgo, fmtDateTime, fmtDurationMs } from '../../lib/format';
import { ProjectName } from '../../components/ProjectName';
import { Markdown } from '../../lib/markdown';
import { ConfirmDialog, ErrorBox, Loading, SectionTitle } from '../../components/ui';
import { FrontmatterTable, LINT_TONES, LintDot, OriginBadge, ScopeBadge } from './shared';
import { DocsPanel } from './DocsPanel';
import { SectionPanel, SectionTabs } from './SectionTabs';
import { defaultSection, guidePath, type DocsSection } from './docsSection';

/** The panel's section tablist: the usage guide, or the item's source. */
const SECTION_TABS = ['docs', 'definition'] as const;

/** Raw file content of the open detail — what the editor edits. */
function composeContent(detail: SystemItemDetail): string {
  return `---\n${detail.frontmatter}\n---\n\n${detail.body}\n`;
}

/** Markdown body of a draft (frontmatter stripped) for the Preview tab. */
function bodyOf(content: string): string {
  if (!content.startsWith('---\n')) return content;
  const end = content.indexOf('\n---', 4);
  return end === -1 ? content : content.slice(end + 4).replace(/^\n+/, '');
}

/** Ride-along lint of a successful write — warnings never block. */
function LintList({ lint }: { lint: SystemLintFinding[] }): JSX.Element | null {
  if (lint.length === 0) return null;
  return (
    <div className="mt-2 space-y-1" role="status">
      {lint.map((f) => (
        <div key={`${f.rule}:${f.message}`} className={`font-mono text-[11px] ${LINT_TONES[f.severity]}`}>
          {f.severity === 'info' ? '●' : '▲'} {f.rule}: {f.message}
        </div>
      ))}
    </div>
  );
}

/* ----- unified diff block (tones match pages/detail/Diffs.tsx) ----- */

export function DiffBlock({ diff }: { diff: string }): JSX.Element {
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
  writable,
  diskHash,
  onMutated,
  onReadonly,
}: {
  kind: SystemItemsKind;
  itemId: number;
  versions: SystemVersion[];
  currentVersionId: number | null;
  /** Rollback offered only on writable items (origin=local, not deleted). */
  writable: boolean;
  /** contentHash of the CURRENT version — the base_hash a rollback races on. */
  diskHash: string | null;
  onMutated: () => void;
  onReadonly: () => void;
}): JSX.Element {
  const { t } = useLingui();
  const [picked, setPicked] = useState<number[]>([]);
  const [diff, setDiff] = useState<SystemDiff | null>(null);
  const [diffError, setDiffError] = useState<string | null>(null);
  const [comparing, setComparing] = useState(false);
  // Rollback flow: pick a version → on-disk diff preview → confirm → POST.
  const [rbTarget, setRbTarget] = useState<SystemVersion | null>(null);
  const [rbDiff, setRbDiff] = useState<string | null>(null);
  const [rbError, setRbError] = useState<string | null>(null);
  const [rbConfirm, setRbConfirm] = useState(false);
  const [rbBusy, setRbBusy] = useState(false);

  // Reset the picker when the panel switches to another item.
  useEffect(() => {
    setPicked([]);
    setDiff(null);
    setDiffError(null);
    setRbTarget(null);
    setRbDiff(null);
    setRbError(null);
    setRbConfirm(false);
  }, [kind, itemId]);

  const openRollback = (v: SystemVersion): void => {
    if (currentVersionId === null) return;
    setRbTarget(v);
    setRbDiff(null);
    setRbError(null);
    // The preview is the CURRENT (disk) content → target snapshot: exactly
    // what will change ON DISK if the rollback is confirmed.
    fetchSystemDiff(kind, itemId, currentVersionId, v.id)
      .then((d) => setRbDiff(d.diff))
      .catch((e: unknown) => setRbError(String(e)));
  };

  const doRollback = (): void => {
    if (rbTarget === null || diskHash === null) return;
    setRbBusy(true);
    setRbError(null);
    rollbackSystemItem(kind, itemId, { version_id: rbTarget.id, base_hash: diskHash })
      .then(() => {
        setRbConfirm(false);
        setRbTarget(null);
        setRbDiff(null);
        onMutated(); // refetch detail + history — the new current arrives from the server
      })
      .catch((e: unknown) => {
        setRbConfirm(false);
        if (e instanceof SystemWriteError) {
          if (e.forbidden === 'readonly') onReadonly();
          if (e.conflict !== null) {
            // Stale preview: drop the diff (disables the confirm button) and
            // refetch — a NEW preview on the fresh detail (fresh diskHash) is
            // the only way to try again; the old base_hash is never re-sent.
            setRbDiff(null);
            onMutated();
            setRbError(
              t`the file changed on disk since this preview — the detail was refreshed, open the preview again`,
            );
          } else {
            setRbError(e.message);
          }
        } else {
          setRbError(String(e));
        }
      })
      .finally(() => setRbBusy(false));
  };

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

  const rbWhen = rbTarget !== null ? fmtDateTime(rbTarget.createdAt) : '';

  if (versions.length === 0) {
    return (
      <div className="text-[12px] text-ink-dim">
        <Trans>no versions captured yet</Trans>
      </div>
    );
  }

  return (
    <div>
      <div className="overflow-hidden rounded-lg border border-line">
        {versions.map((v) => {
          const versionId = String(v.id);
          return (
          <label
            key={v.id}
            className="flex cursor-pointer items-baseline gap-2.5 border-b border-line-soft px-3 py-2 transition-colors last:border-b-0 hover:bg-surface2/50"
          >
            <input
              type="checkbox"
              checked={picked.includes(v.id)}
              onChange={() => toggle(v.id)}
              className="relative top-[1px] shrink-0 accent-brand"
              aria-label={t`select version ${versionId} for compare`}
            />
            <span className="font-mono text-[11px] whitespace-nowrap text-ink-dim">
              {fmtDateTime(v.createdAt)}
            </span>
            <span className="min-w-0 flex-1 truncate text-[12px] text-ink-2">
              {v.changeNote ?? '—'}
            </span>
            {v.id === currentVersionId && (
              <span className="rounded-full border border-green/40 px-2 py-px font-mono text-[10px] text-green">
                <Trans>current</Trans>
              </span>
            )}
            <span className="font-mono text-[10px] text-ink-dim" data-tip-mono data-tip={v.contentHash}>
              {v.contentHash.slice(0, 8)}
            </span>
            {writable && v.id !== currentVersionId && currentVersionId !== null && (
              <button
                type="button"
                onClick={(e) => {
                  // Inside a <label> — keep the click off the compare checkbox.
                  e.preventDefault();
                  e.stopPropagation();
                  openRollback(v);
                }}
                className="rounded-lg border border-line px-2 py-px font-mono text-[10px] text-ink-dim transition-colors hover:border-amber/50 hover:text-amber"
              >
                <Trans>rollback</Trans>
              </button>
            )}
          </label>
          );
        })}
      </div>

      {rbTarget !== null && (
        <div className="mt-2 rounded-lg border border-amber/35 bg-surface px-3 py-2.5">
          <div className="font-mono text-[11px] text-amber">
            <Trans>rollback to {rbWhen} — what will change on disk:</Trans>
          </div>
          {rbDiff === null && rbError === null && <Loading label={t`diff…`} />}
          {rbDiff !== null &&
            (rbDiff === '' ? (
              <div className="mt-1.5 font-mono text-[11.5px] text-ink-dim">
                <Trans>identical to the current content — nothing changes on disk</Trans>
              </div>
            ) : (
              <DiffBlock diff={rbDiff} />
            ))}
          {rbError !== null && <ErrorBox message={rbError} />}
          <div className="mt-2 flex flex-wrap items-center gap-2">
            <button
              type="button"
              disabled={rbDiff === null || rbDiff === '' || rbBusy || diskHash === null}
              onClick={() => setRbConfirm(true)}
              className="rounded-lg border border-red/40 bg-red/10 px-3 py-1.5 font-mono text-[11.5px] font-semibold text-red transition-colors enabled:hover:bg-red/20 disabled:cursor-not-allowed disabled:opacity-40"
            >
              <Trans>rollback on disk…</Trans>
            </button>
            <button
              type="button"
              onClick={() => {
                setRbTarget(null);
                setRbDiff(null);
                setRbError(null);
              }}
              className="rounded-lg border border-line bg-surface px-3 py-1.5 font-mono text-[11.5px] text-ink-2 transition-colors hover:bg-surface2"
            >
              <Trans>cancel</Trans>
            </button>
          </div>
        </div>
      )}

      <ConfirmDialog
        open={rbConfirm}
        title={t`Rollback on disk`}
        confirmLabel={t`rollback`}
        danger
        busy={rbBusy}
        onConfirm={doRollback}
        onCancel={() => setRbConfirm(false)}
      >
        <Trans>
          The file on disk will be replaced with the snapshot from <b>{rbWhen}</b>. A timestamped
          backup is written to config-backups first, and version history is never rewritten.
        </Trans>
      </ConfirmDialog>

      <div className="mt-2 flex items-center gap-2.5">
        <button
          type="button"
          onClick={compare}
          disabled={picked.length !== 2 || comparing}
          className="rounded-lg border border-line bg-surface px-3 py-1.5 text-[12px] font-semibold text-ink-2 transition-colors enabled:hover:bg-surface2 disabled:cursor-not-allowed disabled:opacity-40"
        >
          {comparing ? t`comparing…` : t`Compare`}
        </button>
        <span className="font-mono text-[10.5px] text-ink-dim">
          {picked.length === 2 ? t`two versions selected` : t`pick two versions to diff`}
        </span>
      </div>

      {diffError !== null && <ErrorBox message={diffError} />}
      {diff !== null &&
        (diff.diff === '' ? (
          <div className="mt-2 font-mono text-[11.5px] text-ink-dim">
            <Trans>versions are identical — no content change between the snapshots</Trans>
          </div>
        ) : (
          <DiffBlock diff={diff.diff} />
        ))}
    </div>
  );
}

/* ----- run history & statistics (agents only) -----
   Runs are folded across every notation of the agent name (core:x + x) and
   across all projects — see GET /api/system/agents/{id}/history. */

const HISTORY_WINDOWS = [30, 90, 365] as const;

function StatCard({ label, value }: { label: string; value: string }): JSX.Element {
  return (
    <div className="rounded-lg border border-line bg-surface px-3 py-2">
      <div className="font-mono text-[10px] uppercase tracking-wide text-ink-dim">{label}</div>
      <div className="mt-0.5 text-[15px] font-semibold text-ink">{value}</div>
    </div>
  );
}

function dayTip(day: string, runs: number): string {
  return t`${day}: ${plural(runs, { one: '# run', few: '# runs', many: '# runs', other: '# runs' })}`;
}

/** Dependency-free activity bars — one column per day that had ≥1 run. */
function DaySparkline({ days }: { days: AgentHistory['byDay'] }): JSX.Element | null {
  if (days.length === 0) return null;
  const max = Math.max(...days.map((d) => d.runs), 1);
  return (
    <div className="mt-3 flex items-end gap-0.5" style={{ height: 40 }} aria-hidden>
      {days.map((d) => (
        <div
          key={d.day}
          data-tip={dayTip(d.day, d.runs)}
          className="min-w-[3px] flex-1 rounded-sm bg-brand/60"
          style={{ height: `${String(Math.max(6, (d.runs / max) * 40))}px` }}
        />
      ))}
    </div>
  );
}

const RECENT_RUNS_COLLAPSED = 10;

function errRate(percent: string): string {
  return t`${percent}% err`;
}

function AgentHistoryPanel({ agentId }: { agentId: number }): JSX.Element {
  const { t } = useLingui();
  const [hist, setHist] = useState<AgentHistory | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [days, setDays] = useState<number>(90);
  // Collapse the recent-runs list to the first N; "show more" reveals the rest.
  // Reset on every reload (agent or window change) so it never opens stale.
  const [showAllRuns, setShowAllRuns] = useState(false);

  useEffect(() => {
    let live = true;
    setHist(null);
    setError(null);
    setShowAllRuns(false);
    fetchAgentHistory(agentId, days)
      .then((h) => {
        if (live) setHist(h);
      })
      .catch((e: unknown) => {
        if (live) setError(e instanceof Error ? e.message : String(e));
      });
    return () => {
      live = false;
    };
  }, [agentId, days]);

  const windowPicker = (
    <div className="flex gap-1 font-mono text-[11px]">
      {HISTORY_WINDOWS.map((d) => (
        <button
          key={d}
          type="button"
          onClick={() => setDays(d)}
          className={`rounded px-2 py-0.5 transition-colors ${
            days === d ? 'bg-brand/15 text-brand' : 'text-ink-dim hover:text-ink'
          }`}
        >
          {d}d
        </button>
      ))}
    </div>
  );

  let body: JSX.Element;
  if (error !== null) {
    body = <ErrorBox message={error} />;
  } else if (hist === null) {
    body = <Loading />;
  } else if (hist.totals.runs === 0) {
    body = (
      <div className="text-[12px] text-ink-dim">
        {t`no runs recorded in the last ${plural(days, { one: '# days', few: '# days', many: '# days', other: '# days' })}`}
      </div>
    );
  } else {
    const { totals, duration } = hist;
    const okRuns = totals.okRuns;
    const errorRuns = totals.errorRuns;
    const sessions = totals.sessions;
    const projects = totals.projects;
    const moreRuns = hist.recentRuns.length - RECENT_RUNS_COLLAPSED;
    body = (
      <>
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
          <StatCard label={t`runs`} value={String(totals.runs)} />
          <StatCard label={t`error rate`} value={`${(totals.errorRate * 100).toFixed(0)}%`} />
          <StatCard label={t`avg`} value={fmtDurationMs(duration.avgMs)} />
          <StatCard label="p95" value={fmtDurationMs(duration.p95Ms)} />
        </div>
        <div className="mt-1 font-mono text-[10.5px] text-ink-dim">
          {t`${okRuns} ok · ${errorRuns} error · ${plural(sessions, { one: '# sessions', few: '# sessions', many: '# sessions', other: '# sessions' })} · ${plural(projects, { one: '# project', few: '# projects', many: '# projects', other: '# projects' })}`}
        </div>

        <DaySparkline days={hist.byDay} />

        <div className="mt-4 font-mono text-[10px] uppercase tracking-wide text-ink-dim">
          <Trans>by project</Trans>
        </div>
        <div className="mt-1 overflow-hidden rounded-lg border border-line">
          <table className="w-full text-[12px]">
            <tbody>
              {hist.byProject.map((p) => (
                <tr key={p.slug} className="border-b border-line last:border-0">
                  <td className="px-3 py-1.5">
                    <ProjectName name={p.name} slug={p.slug} />
                  </td>
                  <td className="px-2 py-1.5 text-right font-mono text-ink-dim">{p.runs}×</td>
                  <td className="px-2 py-1.5 text-right font-mono text-ink-dim">
                    {fmtDurationMs(p.avgMs)}
                  </td>
                  <td className="px-3 py-1.5 text-right font-mono text-ink-dim">
                    {p.errorRate > 0 ? (
                      <span className="text-red">{errRate((p.errorRate * 100).toFixed(0))}</span>
                    ) : (
                      '—'
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>

        <div className="mt-4 font-mono text-[10px] uppercase tracking-wide text-ink-dim">
          <Trans>recent runs</Trans>
        </div>
        <ul className="mt-1 space-y-1">
          {(showAllRuns ? hist.recentRuns : hist.recentRuns.slice(0, RECENT_RUNS_COLLAPSED)).map((run, i) => (
            <li key={`${run.sessionUuid}-${String(i)}`}>
              <Link
                to={`/sessions/${run.sessionUuid}`}
                className="flex items-baseline gap-2 rounded-md px-2 py-1 text-[12px] transition-colors hover:bg-surface2"
                data-tip={run.description}
              >
                <span
                  className={`mt-1 inline-block h-1.5 w-1.5 shrink-0 rounded-full ${
                    run.status === 'error' ? 'bg-red' : 'bg-green'
                  }`}
                />
                <span className="min-w-0 flex-1 truncate text-ink-2">
                  {run.sessionTitle !== '' ? run.sessionTitle : run.description || run.projectSlug}
                </span>
                <span className="shrink-0 font-mono text-[10.5px] text-ink-dim">
                  {run.durationMs > 0 ? fmtDurationMs(run.durationMs) : '—'}
                </span>
                <span className="shrink-0 font-mono text-[10.5px] text-ink-dim">
                  {fmtAgo(run.ts)}
                </span>
              </Link>
            </li>
          ))}
        </ul>
        {hist.recentRuns.length > RECENT_RUNS_COLLAPSED && (
          <button
            type="button"
            onClick={() => setShowAllRuns((v) => !v)}
            aria-expanded={showAllRuns}
            className="mt-1.5 rounded-md px-2 py-1 font-mono text-[11px] text-ink-dim transition-colors hover:bg-surface2 hover:text-ink"
          >
            {showAllRuns ? t`show less` : t`show more (${moreRuns})`}
          </button>
        )}
      </>
    );
  }

  return (
    <>
      <div className="mt-4 flex items-center justify-between">
        <SectionTitle>
          <Trans>History</Trans>
        </SectionTitle>
        {windowPicker}
      </div>
      {body}
    </>
  );
}

/* ----- the panel ----- */

function conflictLine(base: string, disk: string): string {
  return t`your base ${base} → on disk ${disk} — what changed under you:`;
}

/** Wrapper of the Definition section: a real tabpanel in the tabbed ('full')
 * variant, a bare fragment in 'editor', where there is no tablist to label it
 * and a stray role="tabpanel" would be a lie to a screen reader. */
function DefinitionWrap({
  tabbed,
  idPrefix,
  children,
}: {
  tabbed: boolean;
  idPrefix: string;
  children: ReactNode;
}): JSX.Element {
  if (!tabbed) return <>{children}</>;
  return (
    <SectionPanel idPrefix={idPrefix} tab="definition">
      {children}
    </SectionPanel>
  );
}

export function SystemItemPanel({
  kind,
  id,
  refreshKey,
  projectNames,
  onClose,
  onMutated,
  onDeleted,
  onReadonly,
  variant = 'full',
  section,
  onSection,
}: {
  kind: SystemItemsKind;
  id: number;
  /** Bumped on WS system_item_updated / local writes — refetches the detail. */
  refreshKey: number;
  /** slug → short display name lookup (from /api/projects). */
  projectNames?: Record<string, string>;
  onClose: () => void;
  /** A write landed — the parent refetches list + summary + this detail. */
  onMutated: () => void;
  /** Soft delete landed — the parent closes the panel and refetches. */
  onDeleted: () => void;
  /** A write hit the global readonly kill-switch — page-level banner. */
  onReadonly: () => void;
  /** Section set. 'full' (default, System page) renders everything with a close
   * button; 'editor' = frontmatter / body / edit only (no ×, History or Versions);
   * 'meta' = History + Versions only (mounted in the agent Overview tab);
   * 'docs' = the usage guide only (mounted in the Agent Hub Docs tab). */
  variant?: 'full' | 'editor' | 'meta' | 'docs';
  /** Active section of the 'full' variant, when the PAGE owns it (?sec=).
   * null/undefined = uncontrolled: local state seeded from defaultSection(). */
  section?: DocsSection | null;
  /** Section changed — the page mirrors it into the URL. */
  onSection?: (section: DocsSection) => void;
}): JSX.Element {
  const { t } = useLingui();
  const [detail, setDetail] = useState<SystemItemDetail | null>(null);
  const [error, setError] = useState<string | null>(null);
  // Uncontrolled section fallback: null means "nobody has picked yet", so the
  // panel opens on defaultSection() of the guide it actually loaded.
  const [localSection, setLocalSection] = useState<DocsSection | null>(null);

  // Editor state (step-12). baseHash is captured the moment Edit OPENS and is
  // deliberately NOT refreshed by later refetches — that snapshot is what
  // makes the 409 protection real.
  const [editing, setEditing] = useState(false);
  const [view, setView] = useState<'edit' | 'preview'>('edit');
  const [draft, setDraft] = useState('');
  const [baseHash, setBaseHash] = useState('');
  const [changeNote, setChangeNote] = useState('');
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [conflict, setConflict] = useState<SystemConflict | null>(null);
  const [lint, setLint] = useState<SystemLintFinding[] | null>(null);
  const [savedVersion, setSavedVersion] = useState<number | null>(null);
  const [confirmReload, setConfirmReload] = useState(false);
  const [pendingReload, setPendingReload] = useState(false);
  const [copied, setCopied] = useState(false);
  // Delete / restore (agents only — no skill delete endpoint exists).
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [actionBusy, setActionBusy] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);

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

  // Switching items drops every write-surface state.
  useEffect(() => {
    setEditing(false);
    setConflict(null);
    setSaveError(null);
    setLint(null);
    setSavedVersion(null);
    setPendingReload(false);
    setConfirmReload(false);
    setConfirmDelete(false);
    setActionError(null);
    setLocalSection(null); // re-seed the section from the NEW item's guide
  }, [kind, id]);

  // Conflict "reload from disk": once the refetched detail arrives, re-seed
  // the editor from it — the draft is discarded (that is the point) and the
  // NEW base_hash is captured, which is the only sanctioned way past a 409.
  useEffect(() => {
    if (!pendingReload || detail === null) return;
    setDraft(composeContent(detail));
    setBaseHash(currentContentHash(detail) ?? '');
    setConflict(null);
    setCopied(false);
    setPendingReload(false);
  }, [detail, pendingReload]);

  if (error !== null) return <ErrorBox message={error} />;
  if (detail === null) return <Loading label={t`detail…`} />;

  const writable = detail.origin === 'local' && !detail.deleted;
  const diskHash = currentContentHash(detail);
  // The file that carries the guide — a skill is registered by DIRECTORY, so
  // the empty state must name its SKILL.md rather than the folder.
  const docsPath = guidePath(detail.path, kind);
  const tasks30d = String(detail.tasks30d);
  const lastUsed = detail.lastUsed !== null ? fmtAgo(detail.lastUsed) : t`never`;
  const versionCount = String(detail.versions.length);
  const itemName = detail.name;
  const sectionLabels: Record<string, string> = { docs: t`Docs`, definition: t`Definition` };
  const savedVersionId = String(savedVersion ?? '');

  // The section tablist is OPT-IN, via onSection. 'editor'/'meta'/'docs' have
  // their own fixed section sets, and the remaining variant='full' mounts are
  // two different things: the System page, which owns a URL to mirror the
  // section into (?sec=) and asks for tabs, and the System Hub's skill profile
  // (pages/system-hub/Profiles.tsx), whose surrounding tab is ALREADY labelled
  // "Definition" — growing a second tablist inside it, one that would open on
  // Docs the moment skills get guides, would be a bug, not a feature.
  const tabbed = variant === 'full' && onSection !== undefined;
  // Unique per mounted panel — two panels on one page must not share tab ids.
  const panelPrefix = `sys-${kind}-${String(detail.id)}`;
  const activeSection: DocsSection = tabbed
    ? (section ?? localSection ?? defaultSection(detail.docs))
    : 'definition';
  const selectSection = (next: string): void => {
    const s: DocsSection = next === 'docs' ? 'docs' : 'definition';
    setLocalSection(s);
    onSection?.(s);
  };

  const openEdit = (): void => {
    if (diskHash === null) return;
    setDraft(composeContent(detail));
    setBaseHash(diskHash);
    setChangeNote('');
    setConflict(null);
    setSaveError(null);
    setLint(null);
    setSavedVersion(null);
    setView('edit');
    setCopied(false);
    setEditing(true);
  };

  const save = (): void => {
    setSaving(true);
    setSaveError(null);
    putSystemItem(kind, detail.id, {
      content: draft,
      base_hash: baseHash,
      ...(changeNote.trim() !== '' ? { change_note: changeNote.trim() } : {}),
    })
      .then((res) => {
        setLint(res.lint);
        setSavedVersion(res.version_id);
        setEditing(false);
        onMutated();
      })
      .catch((e: unknown) => {
        if (e instanceof SystemWriteError) {
          if (e.conflict !== null) {
            setConflict(e.conflict); // Save stays disabled until an explicit reload
            return;
          }
          if (e.forbidden === 'readonly') onReadonly();
          setSaveError(e.message); // 422 parse/name, 403, 5xx
          return;
        }
        setSaveError(String(e));
      })
      .finally(() => setSaving(false));
  };

  const copyDraft = (): void => {
    navigator.clipboard
      .writeText(draft)
      .then(() => setCopied(true))
      .catch(() => setCopied(false));
  };

  const doDelete = (): void => {
    setActionBusy(true);
    setActionError(null);
    deleteSystemAgent(detail.id)
      .then(() => {
        setConfirmDelete(false);
        onDeleted();
      })
      .catch((e: unknown) => {
        setConfirmDelete(false);
        if (e instanceof SystemWriteError && e.forbidden === 'readonly') onReadonly();
        setActionError(e instanceof Error ? e.message : String(e));
      })
      .finally(() => setActionBusy(false));
  };

  const doRestore = (): void => {
    setActionBusy(true);
    setActionError(null);
    restoreSystemAgent(detail.id)
      .then(() => onMutated())
      .catch((e: unknown) => {
        if (e instanceof SystemWriteError && e.forbidden === 'readonly') onReadonly();
        setActionError(e instanceof Error ? e.message : String(e));
      })
      .finally(() => setActionBusy(false));
  };

  // 'docs' variant: the usage guide alone — the Agent Hub mounts this as its
  // own Docs tab, so the hub's tab bar is already the section switcher and no
  // header, metrics strip or tablist is repeated inside it.
  if (variant === 'docs') {
    return <DocsPanel docs={detail.docs} path={docsPath} name={detail.name} />;
  }

  // 'meta' variant: only the History + Versions sections, mounted inside the
  // agent Overview tab. The editor/header live in the Definition tab instead.
  if (variant === 'meta') {
    return (
      <div>
        {kind === 'agents' && <AgentHistoryPanel agentId={detail.id} />}
        <SectionTitle>
          <Trans>Versions</Trans>
        </SectionTitle>
        <Versions
          kind={kind}
          itemId={detail.id}
          versions={detail.versions}
          currentVersionId={detail.currentVersionId}
          writable={writable}
          diskHash={diskHash}
          onMutated={onMutated}
          onReadonly={onReadonly}
        />
      </div>
    );
  }

  return (
    <div>
      <div className="flex flex-wrap items-center gap-2">
        <LintDot severity={detail.lintMax} />
        <h1 className={`font-display text-[18px] leading-tight font-semibold ${detail.dead ? 'opacity-70' : ''}`}>
          {detail.name}
        </h1>
        {detail.model !== null && (
          <span className="font-mono text-[10px] text-ink-faint">{detail.model}</span>
        )}
        <span className="ml-auto flex items-center gap-1.5">
          <ScopeBadge
            scope={detail.scope}
            projectSlug={detail.projectSlug}
            projectName={
              detail.projectSlug !== null
                ? (projectNames?.[detail.projectSlug] ?? detail.projectSlug)
                : null
            }
          />
          <OriginBadge origin={detail.origin} pluginName={detail.pluginName} />
          {detail.deleted && (
            <span className="rounded-full border border-red/40 px-2 py-px font-mono text-[10px] text-red">
              <Trans>deleted</Trans>
            </span>
          )}
          {variant === 'full' && (
            <button
              type="button"
              onClick={onClose}
              aria-label={t`close detail`}
              className="ml-1 rounded-[7px] border border-line-strong px-2 py-px text-[12px] text-ink-faint transition-colors hover:text-ink"
            >
              ✕
            </button>
          )}
        </span>
      </div>
      <div className="mt-1 font-mono text-[10px] break-all text-ink-faint">{detail.path}</div>

      {/* usage metrics (30-day window, per the registry aggregates) */}
      <div className="mt-3 flex gap-[22px] border-b border-line pb-3 font-mono text-[11px] text-ink-dim">
        <span>
          <Trans>
            tasks 30d <b className="font-medium text-ink">{tasks30d}</b>
          </Trans>
        </span>
        <span>
          <Trans>
            last used <b className="font-medium text-ink">{lastUsed}</b>
          </Trans>
        </span>
        <span>
          <Trans>
            versions <b className="font-medium text-ink">{versionCount}</b>
          </Trans>
        </span>
      </div>

      {/* Docs | Definition. History and Versions deliberately stay BELOW the
          tablist and outside it — they describe the item in both sections. */}
      {tabbed && (
        <SectionTabs
          tabs={SECTION_TABS}
          labels={sectionLabels}
          active={activeSection}
          onSelect={selectSection}
          idPrefix={panelPrefix}
          ariaLabel={t`${itemName} sections`}
        />
      )}

      {tabbed && activeSection === 'docs' && (
        <SectionPanel idPrefix={panelPrefix} tab="docs">
          <DocsPanel docs={detail.docs} path={docsPath} name={detail.name} />
        </SectionPanel>
      )}

      {activeSection === 'definition' && (
        <DefinitionWrap tabbed={tabbed} idPrefix={panelPrefix}>
      {/* write-surface actions (step-12) — origin=plugin NEVER shows Edit */}
      {!editing && (
        <div className="mt-3 flex flex-wrap items-center gap-2">
          {writable && diskHash !== null && (
            <button
              type="button"
              onClick={openEdit}
              className="rounded-lg border border-line-strong bg-field px-3.5 py-1.5 text-[12px] font-semibold text-ink-2 transition-colors hover:bg-surface2"
            >
              <Trans>Edit</Trans>
            </button>
          )}
          {kind === 'agents' && writable && (
            <button
              type="button"
              onClick={() => setConfirmDelete(true)}
              disabled={actionBusy}
              className="rounded-lg border border-red/40 px-3 py-1.5 font-mono text-[11px] text-red transition-colors hover:bg-red/10 disabled:opacity-50"
            >
              <Trans>delete…</Trans>
            </button>
          )}
          {kind === 'agents' && detail.deleted && (
            <button
              type="button"
              onClick={doRestore}
              disabled={actionBusy}
              className="rounded-lg border border-green/40 bg-green/10 px-3 py-1.5 font-mono text-[11.5px] font-semibold text-green transition-colors hover:bg-green/20 disabled:opacity-50"
            >
              {actionBusy ? '…' : t`restore`}
            </button>
          )}
          {detail.origin === 'plugin' && (
            <span
              className="rounded-lg border border-brand/35 bg-brand/5 px-3 py-1.5 font-mono text-[11px] text-brand"
              data-tip={t`plugin items are read-only — edit them in the plugin's marketplace repo and adopt via /plugin update`}
            >
              <Trans>managed by the plugin marketplace — read-only</Trans>
            </span>
          )}
        </div>
      )}
      {actionError !== null && <ErrorBox message={actionError} />}

      {/* post-save status: new version id + ride-along lint (never blocks) */}
      {!editing && savedVersion !== null && (
        <div className="mt-3 rounded-lg border border-green/35 bg-green/5 px-3 py-2.5" role="status">
          <div className="font-mono text-[11px] text-green">
            <Trans>saved — version #{savedVersionId}</Trans>
          </div>
          <LintList lint={lint ?? []} />
        </div>
      )}

      {editing ? (
        <>
          <SectionTitle>
            <Trans>Edit — raw markdown</Trans>
          </SectionTitle>
          <div className="flex gap-0.5 border-b border-line" role="tablist">
            {(['edit', 'preview'] as const).map((tab) => (
              <button
                key={tab}
                type="button"
                role="tab"
                aria-selected={view === tab}
                onClick={() => setView(tab)}
                className={`-mb-px border-b-2 px-3 py-1.5 text-[12px] font-medium transition-colors ${
                  view === tab ? 'border-brand text-brand' : 'border-transparent text-ink-dim hover:text-ink'
                }`}
              >
                {tab === 'edit' ? t`Edit` : t`Preview`}
              </button>
            ))}
          </div>
          {view === 'edit' ? (
            <textarea
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              spellCheck={false}
              rows={Math.min(32, Math.max(14, draft.split('\n').length + 2))}
              aria-label={t`raw markdown content`}
              className="mt-2 w-full resize-y rounded-lg border border-line bg-bg px-3 py-2.5 font-mono text-[12px] leading-relaxed text-ink-2 focus:border-brand focus:outline-none"
            />
          ) : (
            <div className="mt-2 rounded-lg border border-line bg-bg px-3.5 py-3 text-[13px] leading-[1.65] text-ink-2">
              <Markdown text={bodyOf(draft)} />
            </div>
          )}

          {conflict !== null && (
            <div className="mt-2 rounded-lg border border-red/35 bg-red/5 px-3 py-2.5" role="alert">
              <div className="font-mono text-[11.5px] font-semibold text-red">
                <Trans>the file changed outside the editor — your edit is based on a stale version</Trans>
              </div>
              <div className="mt-1 font-mono text-[10.5px] text-ink-dim">
                {conflictLine(conflict.base_hash.slice(0, 8), conflict.disk_hash.slice(0, 8))}
              </div>
              <DiffBlock diff={conflict.diff} />
              <div className="mt-2 flex flex-wrap items-center gap-2">
                <button
                  type="button"
                  onClick={() => setConfirmReload(true)}
                  className="rounded-lg border border-red/40 bg-red/10 px-3 py-1.5 font-mono text-[11.5px] font-semibold text-red transition-colors hover:bg-red/20"
                >
                  <Trans>reload from disk…</Trans>
                </button>
                <button
                  type="button"
                  onClick={copyDraft}
                  className="rounded-lg border border-line bg-surface px-3 py-1.5 font-mono text-[11.5px] text-ink-2 transition-colors hover:bg-surface2"
                >
                  {copied ? t`copied ✓` : t`copy my text`}
                </button>
                <span className="font-mono text-[10.5px] text-ink-dim">
                  <Trans>
                    no force-overwrite exists — reload picks up the disk version, then re-apply your
                    edit
                  </Trans>
                </span>
              </div>
            </div>
          )}

          {saveError !== null && <ErrorBox message={saveError} />}

          <div className="mt-2 flex flex-wrap items-center gap-2">
            <input
              value={changeNote}
              onChange={(e) => setChangeNote(e.target.value)}
              placeholder={t`change note (optional)`}
              aria-label={t`change note`}
              spellCheck={false}
              className="min-w-[220px] flex-1 rounded-lg border border-line bg-bg px-3 py-1.5 font-mono text-[11.5px] text-ink-2 focus:border-brand focus:outline-none"
            />
            <button
              type="button"
              onClick={save}
              disabled={saving || conflict !== null}
              className="rounded-lg border border-green/40 bg-green/10 px-3.5 py-1.5 font-mono text-[11.5px] font-semibold text-green transition-colors enabled:hover:bg-green/20 disabled:cursor-not-allowed disabled:opacity-40"
            >
              {saving ? t`saving…` : t`Save`}
            </button>
            <button
              type="button"
              onClick={() => {
                setEditing(false);
                setConflict(null);
                setSaveError(null);
              }}
              className="rounded-lg border border-line bg-surface px-3.5 py-1.5 font-mono text-[11.5px] text-ink-2 transition-colors hover:bg-surface2"
            >
              <Trans>cancel</Trans>
            </button>
          </div>

          <ConfirmDialog
            open={confirmReload}
            title={t`Reload from disk`}
            confirmLabel={t`discard my changes`}
            danger
            busy={pendingReload}
            onConfirm={() => {
              setConfirmReload(false);
              setPendingReload(true);
              // Drop the cached copy: the re-seed effect keys on [detail,
              // pendingReload] and must run against the REFETCHED detail —
              // seeding from the cached one would capture the stale hash.
              setDetail(null);
              onMutated();
            }}
            onCancel={() => setConfirmReload(false)}
          >
            <Trans>
              The editor is re-seeded with the current on-disk content and a fresh base_hash. Your
              draft is <b>discarded</b> — use “copy my text” first if you want to keep it.
            </Trans>
          </ConfirmDialog>
        </>
      ) : (
        <>
          <SectionTitle>
            <Trans>Frontmatter</Trans>
          </SectionTitle>
          <FrontmatterTable frontmatter={detail.frontmatter} />

          <SectionTitle>
            <Trans>Body</Trans>
          </SectionTitle>
          <div className="text-[13px] leading-[1.6] text-ink-2">
            <Markdown text={detail.body} />
          </div>
        </>
      )}
        </DefinitionWrap>
      )}

      {kind === 'agents' && variant !== 'editor' && <AgentHistoryPanel agentId={detail.id} />}

      {variant !== 'editor' && (
        <>
          <SectionTitle>
            <Trans>Versions</Trans>
          </SectionTitle>
          <Versions
            kind={kind}
            itemId={detail.id}
            versions={detail.versions}
            currentVersionId={detail.currentVersionId}
            writable={writable}
            diskHash={diskHash}
            onMutated={onMutated}
            onReadonly={onReadonly}
          />
        </>
      )}

      <ConfirmDialog
        open={confirmDelete}
        title={t`Delete agent`}
        confirmLabel={t`delete`}
        danger
        busy={actionBusy}
        onConfirm={doDelete}
        onCancel={() => setConfirmDelete(false)}
      >
        <Trans>
          <b>{itemName}</b> is soft-deleted: the file is <b>moved into backups, not destroyed</b>,
          and version history stays. The agent disappears from the lists and can be restored later.
        </Trans>
      </ConfirmDialog>
    </div>
  );
}
