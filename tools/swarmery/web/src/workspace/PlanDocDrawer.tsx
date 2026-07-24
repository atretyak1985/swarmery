// Plan-doc editor drawer (fusion phase 10): opens one plan markdown doc for a
// workspace epic and lets the user (a) read it, (b) toggle acceptance
// checkboxes directly in preview — which PATCHes the exact `- [ ]`↔`- [x]` line
// so the rollup follows on the next rescan, and (c) switch to a raw editor and
// Save (PUT, which writes a timestamped backup on the daemon side). Same
// versioned-backup write idiom as the System/Memory surfaces; the daemon
// confines every path to that task's plan/ dir.

import { useCallback, useEffect, useState } from 'react';
import { fetchPlanDoc, savePlanDoc, togglePlanCheckbox } from '../api';
import { Markdown } from '../lib/markdown';
import { Loading } from '../components/ui';

type Mode = 'preview' | 'edit';

/** A checkbox found in the source: its 0-based line index + done state + label. */
interface CheckboxLine {
  line: number;
  done: boolean;
  label: string;
}

const CHECKBOX_RE = /^(\s*[-*]\s+)\[( |x|X)\]\s+(.*)$/;

/** Extract every acceptance checkbox with its source line index. */
function extractCheckboxes(content: string): CheckboxLine[] {
  const out: CheckboxLine[] = [];
  content.split('\n').forEach((raw, i) => {
    const m = CHECKBOX_RE.exec(raw);
    if (m) out.push({ line: i, done: (m[2] ?? '').toLowerCase() === 'x', label: m[3] ?? '' });
  });
  return out;
}

export function PlanDocDrawer({
  taskId,
  path,
  title,
  onClose,
  onChanged,
}: {
  taskId: number;
  path: string;
  title: string;
  onClose: () => void;
  onChanged: () => void;
}): JSX.Element {
  const [content, setContent] = useState<string | null>(null);
  const [draft, setDraft] = useState('');
  const [mode, setMode] = useState<Mode>('preview');
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [busyLine, setBusyLine] = useState<number | null>(null);
  const [savedNote, setSavedNote] = useState<string | null>(null);

  const load = useCallback((): void => {
    setContent(null);
    setError(null);
    fetchPlanDoc(taskId, path)
      .then((doc) => {
        setContent(doc.content);
        setDraft(doc.content);
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)));
  }, [taskId, path]);

  useEffect(() => {
    load();
  }, [load]);

  // Esc closes the drawer.
  useEffect(() => {
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  const save = (): void => {
    setSaving(true);
    setError(null);
    savePlanDoc(taskId, path, draft)
      .then((doc) => {
        setContent(doc.content);
        setDraft(doc.content);
        setMode('preview');
        setSavedNote(doc.backup !== undefined ? 'saved · backup written' : 'saved');
        onChanged();
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setSaving(false));
  };

  const toggle = (cb: CheckboxLine): void => {
    setBusyLine(cb.line);
    setError(null);
    togglePlanCheckbox(taskId, path, cb.line, !cb.done)
      .then((doc) => {
        setContent(doc.content);
        setDraft(doc.content);
        onChanged();
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setBusyLine(null));
  };

  const checkboxes = content !== null ? extractCheckboxes(content) : [];
  const dirty = draft !== content;

  return (
    <div className="fixed inset-0 z-40 flex justify-end bg-black/40" onClick={onClose} role="presentation">
      <div
        className="flex h-full w-full max-w-[720px] flex-col border-l border-line bg-surface shadow-2xl"
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-label={`plan doc ${title}`}
      >
        {/* Header. */}
        <div className="flex items-center justify-between gap-3 border-b border-line px-4 py-3">
          <div className="min-w-0">
            <div className="truncate text-[13px] font-semibold text-ink">{title}</div>
            <div className="truncate font-mono text-[10px] text-ink-faint">{path}</div>
          </div>
          <div className="flex shrink-0 items-center gap-1">
            {(['preview', 'edit'] as const).map((m) => (
              <button
                key={m}
                type="button"
                onClick={() => setMode(m)}
                aria-pressed={mode === m}
                className={`rounded-md border px-2 py-1 font-mono text-[10.5px] capitalize transition-colors ${
                  mode === m ? 'border-line-strong bg-surface2 text-brand' : 'border-transparent text-ink-dim hover:text-ink'
                }`}
              >
                {m}
              </button>
            ))}
            <button
              type="button"
              onClick={onClose}
              aria-label="close"
              className="ml-1 rounded-md px-2 py-1 text-ink-dim transition-colors hover:text-ink"
            >
              ×
            </button>
          </div>
        </div>

        {error !== null && (
          <div role="alert" className="border-b border-red/30 bg-red/10 px-4 py-1.5 font-mono text-[11px] text-red">
            {error}
          </div>
        )}
        {savedNote !== null && mode === 'preview' && (
          <div className="border-b border-green/30 bg-green/10 px-4 py-1.5 font-mono text-[11px] text-green">
            {savedNote}
          </div>
        )}

        {/* Body. */}
        <div className="min-h-0 flex-1 overflow-y-auto px-4 py-3">
          {content === null && error === null ? (
            <Loading label="doc…" />
          ) : mode === 'edit' ? (
            <textarea
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              spellCheck={false}
              className="h-full min-h-[400px] w-full resize-none rounded-lg border border-line bg-field px-3 py-2 font-mono text-[12px] leading-relaxed text-ink outline-none focus:border-ink-dim"
              aria-label="plan doc source"
            />
          ) : (
            <div className="space-y-4">
              {/* Interactive acceptance checkboxes (toggling PATCHes the line). */}
              {checkboxes.length > 0 && (
                <div className="rounded-lg border border-line bg-surface/40 p-3">
                  <div className="mb-2 font-mono text-[10px] tracking-[0.1em] text-ink-faint uppercase">
                    Acceptance ({checkboxes.filter((c) => c.done).length}/{checkboxes.length})
                  </div>
                  <ul className="space-y-1">
                    {checkboxes.map((cb) => (
                      <li key={cb.line}>
                        <button
                          type="button"
                          disabled={busyLine === cb.line}
                          onClick={() => toggle(cb)}
                          className="flex w-full items-start gap-2 rounded px-1 py-0.5 text-left text-[12.5px] text-ink transition-colors hover:bg-surface2/50 disabled:opacity-50"
                        >
                          <span
                            aria-hidden="true"
                            className={`mt-px inline-flex h-3.5 w-3.5 shrink-0 items-center justify-center rounded border text-[9px] ${
                              cb.done ? 'border-green bg-green/20 text-green' : 'border-line-strong text-transparent'
                            }`}
                          >
                            ✓
                          </span>
                          <span className={cb.done ? 'text-ink-dim line-through' : ''}>{cb.label}</span>
                        </button>
                      </li>
                    ))}
                  </ul>
                </div>
              )}
              {/* Rendered markdown (read-only). */}
              <div className="text-[13px] text-ink-2">{content !== null && <Markdown text={content} />}</div>
            </div>
          )}
        </div>

        {/* Footer (edit mode only). */}
        {mode === 'edit' && (
          <div className="flex items-center justify-end gap-2 border-t border-line px-4 py-2.5">
            <button
              type="button"
              onClick={() => {
                setDraft(content ?? '');
                setMode('preview');
              }}
              className="rounded-md border border-line px-2.5 py-1 font-mono text-[11px] text-ink-dim transition-colors hover:text-ink"
            >
              cancel
            </button>
            <button
              type="button"
              onClick={save}
              disabled={saving || !dirty}
              className="rounded-md border border-line-strong bg-surface2 px-2.5 py-1 font-mono text-[11px] text-brand transition-colors hover:bg-surface2/70 disabled:cursor-not-allowed disabled:text-ink-faint"
            >
              {saving ? 'saving…' : 'Save'}
            </button>
          </div>
        )}
      </div>
    </div>
  );
}
