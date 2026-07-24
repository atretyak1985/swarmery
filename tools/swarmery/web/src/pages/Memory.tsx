// Project Memory (/p/:slug/memory — fusion phase 12): a project's durable
// memory made visible and editable in the dashboard. Left: the memory files
// grouped by their three roots (Project instructions = CLAUDE.md, Auto-memory =
// Claude Code's ~/.claude auto-memory, Serena = .serena/memories). Right: a
// markdown editor mirroring the System page's write surface — edit/preview
// toggle, a base_hash conflict guard (409 → reload prompt), an unsaved-changes
// guard on navigation away, and a read-only badge for files the daemon reports
// unwritable (readonly kill-switch or a serena note).
//
// Scope comes from the workspace (useProjectWorkspace); the endpoints resolve a
// slug OR numeric id, so the slug is the natural handle here.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { MemoryFile, MemoryFileContent, MemoryKind } from '../api/types';
import { fetchMemoryFile, fetchMemoryList, putMemoryFile } from '../api';
import { useProjectWorkspace } from '../workspace/ProjectContext';
import { Markdown } from '../lib/markdown';
import { fmtAgo } from '../lib/format';
import { ConfirmDialog, Empty, ErrorBox, Loading } from '../components/ui';

const KIND_LABELS: Record<MemoryKind, string> = {
  'claude-md': 'Project instructions',
  'auto-memory': 'Auto-memory',
  serena: 'Serena',
};

/** Render order of the three roots (matches the daemon's list sort). */
const KIND_ORDER: MemoryKind[] = ['claude-md', 'auto-memory', 'serena'];

/** Human-friendly byte size for the file list. */
function fmtBytes(n: number): string {
  if (n < 1024) return `${String(n)} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

interface FileGroup {
  kind: MemoryKind;
  files: MemoryFile[];
}

function groupByKind(files: MemoryFile[]): FileGroup[] {
  return KIND_ORDER.map((kind) => ({ kind, files: files.filter((f) => f.kind === kind) })).filter(
    (g) => g.files.length > 0,
  );
}

export function Memory(): JSX.Element {
  const { slug, projectId, loading: projLoading } = useProjectWorkspace();

  const [files, setFiles] = useState<MemoryFile[] | null>(null);
  const [listError, setListError] = useState<string | null>(null);
  const [selected, setSelected] = useState<string | null>(null);

  const loadList = useCallback((): void => {
    if (slug === '') return;
    fetchMemoryList(slug)
      .then((r) => {
        setFiles(r.files);
        setListError(null);
      })
      .catch((e: unknown) => setListError(e instanceof Error ? e.message : String(e)));
  }, [slug]);

  useEffect(() => {
    setFiles(null);
    setSelected(null);
    loadList();
  }, [loadList]);

  // Auto-select the first file (usually CLAUDE.md) once the list resolves.
  useEffect(() => {
    if (selected === null && files !== null && files.length > 0) {
      setSelected(files[0]?.path ?? null);
    }
  }, [files, selected]);

  const groups = useMemo(() => groupByKind(files ?? []), [files]);

  const wrap = (inner: JSX.Element): JSX.Element => (
    <div className="px-4 pt-5 pb-10 desk:px-8 desk:pt-7">{inner}</div>
  );

  if (projLoading && projectId === null) return wrap(<Loading label="workspace…" />);
  if (projectId === null) return wrap(<Empty>unknown project</Empty>);
  if (listError !== null) return wrap(<ErrorBox message={listError} onRetry={loadList} />);
  if (files === null) return wrap(<Loading label="memory…" />);

  return (
    <div className="flex min-h-0 flex-1 flex-col px-4 pt-5 pb-10 desk:px-8 desk:pt-7">
      <h1 className="font-display text-[22px] font-medium tracking-[-0.01em] desk:text-[26px]">
        Memory
      </h1>
      <p className="mt-1 text-[12.5px] text-ink-dim">
        Everything this project remembers — its instructions, Claude Code auto-memory, and Serena
        notes — editable with a versioned backup on every save.
      </p>

      {files.length === 0 ? (
        <Empty>
          No memory files yet — this project has no CLAUDE.md, auto-memory, or Serena notes.
        </Empty>
      ) : (
        <div className="mt-4 grid min-h-0 flex-1 grid-cols-1 gap-4 lg:grid-cols-[260px_minmax(0,1fr)]">
          <FileList
            groups={groups}
            selected={selected}
            onSelect={setSelected}
          />
          {selected !== null ? (
            <MemoryEditor key={selected} project={slug} path={selected} onSaved={loadList} />
          ) : (
            <Empty>Select a file to view or edit.</Empty>
          )}
        </div>
      )}
    </div>
  );
}

function FileList({
  groups,
  selected,
  onSelect,
}: {
  groups: FileGroup[];
  selected: string | null;
  onSelect: (path: string) => void;
}): JSX.Element {
  return (
    <nav className="flex flex-col gap-3" aria-label="Memory files">
      {groups.map((group) => (
        <div key={group.kind}>
          <div className="mb-1 px-1 font-mono text-[10px] font-medium tracking-[0.12em] text-ink-faint uppercase">
            {KIND_LABELS[group.kind]} · {group.files.length}
          </div>
          <div className="flex flex-col gap-0.5">
            {group.files.map((f) => {
              const active = f.path === selected;
              return (
                <button
                  key={f.path}
                  type="button"
                  onClick={() => onSelect(f.path)}
                  aria-current={active ? 'true' : undefined}
                  className={`flex flex-col gap-0.5 rounded-[10px] border px-2.5 py-1.5 text-left transition-colors ${
                    active
                      ? 'border-line-strong bg-surface2 text-brand'
                      : 'border-transparent text-ink-dim hover:bg-surface2/50 hover:text-ink'
                  }`}
                >
                  <span className="flex items-center gap-1.5 truncate text-[12.5px] font-medium">
                    <span className="truncate">{f.name}</span>
                    {!f.writable && (
                      <span className="shrink-0 rounded-[5px] border border-line px-1 py-[1px] font-mono text-[9px] text-ink-faint">
                        read-only
                      </span>
                    )}
                  </span>
                  <span className="font-mono text-[10px] text-ink-faint">
                    {fmtBytes(f.sizeBytes)} · {fmtAgo(f.updatedAt)}
                  </span>
                </button>
              );
            })}
          </div>
        </div>
      ))}
    </nav>
  );
}

type EditorView = 'edit' | 'preview';

function MemoryEditor({
  project,
  path,
  onSaved,
}: {
  project: string;
  path: string;
  onSaved: () => void;
}): JSX.Element {
  const [detail, setDetail] = useState<MemoryFileContent | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [view, setView] = useState<EditorView>('edit');
  const [draft, setDraft] = useState('');
  const [baseHash, setBaseHash] = useState('');
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const [confirmReload, setConfirmReload] = useState(false);

  const load = useCallback((): void => {
    fetchMemoryFile(project, path)
      .then((d) => {
        setDetail(d);
        setDraft(d.content);
        setBaseHash(d.hash);
        setError(null);
        setSaveError(null);
        setSaved(false);
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)));
  }, [project, path]);

  useEffect(() => {
    setDetail(null);
    load();
  }, [load]);

  const dirty = detail !== null && draft !== detail.content;
  const writable = detail?.writable ?? false;

  // Unsaved-changes guard: a native beforeunload prompt while the draft differs.
  const dirtyRef = useRef(dirty);
  dirtyRef.current = dirty;
  useEffect(() => {
    const onBeforeUnload = (e: BeforeUnloadEvent): void => {
      if (dirtyRef.current) {
        e.preventDefault();
        e.returnValue = '';
      }
    };
    window.addEventListener('beforeunload', onBeforeUnload);
    return () => window.removeEventListener('beforeunload', onBeforeUnload);
  }, []);

  const save = useCallback((): void => {
    if (!writable || !dirty) return;
    setSaving(true);
    setSaveError(null);
    putMemoryFile(project, path, draft, baseHash)
      .then((d) => {
        setDetail(d);
        setDraft(d.content);
        setBaseHash(d.hash);
        setSaved(true);
        onSaved(); // refresh the list's size/updated-at
      })
      .catch((e: unknown) => {
        const msg = e instanceof Error ? e.message : String(e);
        setSaveError(msg);
        // A base_hash conflict → offer to reload the on-disk version.
        if (/base_hash|changed on disk/i.test(msg)) setConfirmReload(true);
      })
      .finally(() => setSaving(false));
  }, [writable, dirty, project, path, draft, baseHash, onSaved]);

  if (error !== null) return <ErrorBox message={error} onRetry={load} />;
  if (detail === null) return <Loading label="file…" />;

  return (
    <div className="flex min-h-0 flex-col rounded-xl border border-line bg-surface">
      <div className="flex flex-wrap items-center gap-2 border-b border-line px-3 py-2">
        <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-ink-dim" title={path}>
          {path}
        </span>
        {!writable && (
          <span className="rounded-[6px] border border-amber/40 bg-amber/10 px-1.5 py-[2px] font-mono text-[10px] text-amber">
            read-only
          </span>
        )}
        <div className="flex overflow-hidden rounded-lg border border-line">
          {(['edit', 'preview'] as EditorView[]).map((v) => (
            <button
              key={v}
              type="button"
              onClick={() => setView(v)}
              aria-pressed={view === v}
              className={`px-2.5 py-1 font-mono text-[11px] transition-colors ${
                view === v ? 'bg-surface2 text-brand' : 'text-ink-dim hover:text-ink'
              }`}
            >
              {v}
            </button>
          ))}
        </div>
        <button
          type="button"
          onClick={save}
          disabled={!writable || !dirty || saving}
          className="rounded-lg border border-green/40 bg-green/10 px-3 py-1 font-mono text-[11px] font-semibold text-green transition-colors hover:bg-green/20 disabled:opacity-40"
        >
          {saving ? 'saving…' : 'save'}
        </button>
      </div>

      <div className="min-h-0 flex-1 overflow-auto p-3">
        {view === 'edit' ? (
          <textarea
            value={draft}
            onChange={(e) => {
              setDraft(e.target.value);
              setSaved(false);
            }}
            readOnly={!writable}
            spellCheck={false}
            aria-label="Memory file content"
            className="min-h-[360px] w-full resize-y rounded-lg border border-line bg-bg px-3 py-2 font-mono text-[12.5px] leading-relaxed text-ink outline-none focus:border-line-strong read-only:opacity-70"
          />
        ) : (
          <div className="rounded-lg border border-line bg-bg px-3.5 py-3 text-[13px] leading-relaxed">
            <Markdown text={draft} />
          </div>
        )}
      </div>

      <div className="flex flex-wrap items-center gap-2 border-t border-line px-3 py-1.5 font-mono text-[10.5px]">
        {saveError !== null ? (
          <span className="text-red">{saveError}</span>
        ) : dirty ? (
          <span className="text-amber">unsaved changes</span>
        ) : saved ? (
          <span className="text-green">saved ✓</span>
        ) : (
          <span className="text-ink-faint">up to date</span>
        )}
      </div>

      <ConfirmDialog
        open={confirmReload}
        title="File changed on disk"
        confirmLabel="reload from disk"
        danger
        onConfirm={() => {
          setConfirmReload(false);
          load(); // discards the draft, refetches the current content + hash
        }}
        onCancel={() => setConfirmReload(false)}
      >
        This file was modified on disk since you opened it, so the save was refused. Reloading
        replaces your unsaved edits with the current on-disk version.
      </ConfirmDialog>
    </div>
  );
}
