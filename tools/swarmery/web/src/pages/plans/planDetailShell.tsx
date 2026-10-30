// What the phase and plan detail panels share: the shell, the tab bar, the doc editor
// and the parse of a doc's acceptance criteria.

import { Trans, useLingui } from '@lingui/react/macro';
import { type ReactNode, useCallback, useEffect, useRef, useState } from 'react';
import { Link, type BlockerFunction, useBlocker } from 'react-router-dom';
import { fetchPlanDoc, savePlanDoc } from '../../api';
import { Loading } from '../../components/ui';

/** One acceptance checkbox: its 0-based SOURCE line (what the PATCH endpoint
 * addresses), tick state and label. */
export interface Check {
  line: number;
  text: string;
  done: boolean;
}

/** Acceptance-criteria checkbox lines of a markdown doc, in order. The line
 * index is what makes the list toggleable — togglePlanCheckbox rewrites that
 * exact `- [ ]`↔`- [x]` line. */
export function extractChecks(md: string): Check[] {
  const out: Check[] = [];
  md.split('\n').forEach((raw, i) => {
    const m = /^\s*[-*]\s+\[( |x)\]\s+(.*)$/i.exec(raw);
    if (m !== null) out.push({ line: i, done: (m[1] ?? '').toLowerCase() === 'x', text: m[2] ?? '' });
  });
  return out;
}


/** Shell of the inline detail panel: an identity header, an optional tab bar
 * and the body — full column width (the parent column owns the scroll), so
 * plan docs and completion reports read as prose instead of a 360px rail. The
 * back control lives outside, above the panel (see BackToPhases). */
export function DetailShell({
  ariaLabel,
  header,
  tabBar,
  children,
}: {
  ariaLabel: string;
  header: ReactNode;
  tabBar?: ReactNode;
  children: ReactNode;
}): JSX.Element {
  return (
    <section
      aria-label={ariaLabel}
      className="overflow-hidden rounded-xl border border-line bg-surface"
    >
      <div className="border-b border-line px-4 pt-3 pb-3">{header}</div>
      {tabBar}
      <div className="px-4 py-3 text-[13px] leading-relaxed">{children}</div>
    </section>
  );
}

/** The shared tab bar of both detail panels. With `hrefFor` each tab is a real
 * link (its click navigates, `onTab` is not called as well). */
export function DetailTabs<T extends string>({
  label,
  tabs,
  active,
  onTab,
  hrefFor,
}: {
  label: string;
  tabs: { id: T; label: string }[];
  active: T;
  onTab: (tab: T) => void;
  hrefFor?: ((tab: T) => string) | undefined;
}): JSX.Element {
  return (
    <div
      className="flex shrink-0 gap-1 overflow-x-auto border-b border-line px-4"
      role="tablist"
      aria-label={label}
    >
      {tabs.map((tab) => {
        const className = `-mb-px shrink-0 border-b-2 px-3 py-[8px] text-[12.5px] font-medium whitespace-nowrap transition-colors ${
          active === tab.id ? 'border-brand text-brand' : 'border-transparent text-ink-dim hover:text-ink'
        }`;
        return hrefFor !== undefined ? (
          <Link key={tab.id} to={hrefFor(tab.id)} role="tab" aria-selected={active === tab.id} className={className}>
            {tab.label}
          </Link>
        ) : (
          <button
            key={tab.id}
            type="button"
            role="tab"
            aria-selected={active === tab.id}
            onClick={() => onTab(tab.id)}
            className={className}
          >
            {tab.label}
          </button>
        );
      })}
    </div>
  );
}

/** Inline raw-markdown editor — the Edit tab of both detail panels. Loads the
 * doc itself, PUTs on Save (the daemon writes a timestamped backup), and tells
 * the page to refetch so the rollup follows. Replaces the doc modal. */
export function DocEditor({
  taskId,
  path,
  version,
  onSaved,
}: {
  taskId: number;
  path: string;
  /** Bumps to re-fetch when the doc changed server-side. */
  version: string | null;
  onSaved: () => void;
}): JSX.Element {
  const { t } = useLingui();
  const [content, setContent] = useState<string | null>(null);
  const [draft, setDraft] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [note, setNote] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    setContent(null);
    setError(null);
    setNote(null);
    fetchPlanDoc(taskId, path)
      .then((d) => {
        if (!alive) return;
        setContent(d.content);
        setDraft(d.content);
      })
      .catch((e: unknown) => {
        if (alive) setError(e instanceof Error ? e.message : String(e));
      });
    return () => {
      alive = false;
    };
  }, [taskId, path, version]);

  const save = (): void => {
    setSaving(true);
    setError(null);
    savePlanDoc(taskId, path, draft)
      .then((doc) => {
        setContent(doc.content);
        setDraft(doc.content);
        setNote(doc.backup !== undefined ? t`saved · backup written` : t`saved`);
        onSaved();
      })
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setSaving(false));
  };

  // Unsaved work cannot be lost without a prompt (SC-12). In-app navigation —
  // Back, a tab, a plan link — that would leave this location is blocked and
  // confirmed; a reload or a closed tab gets the native beforeunload prompt
  // (the pages/Memory.tsx idiom).
  const unsaved = content !== null && draft !== content;
  const shouldBlock = useCallback<BlockerFunction>(
    ({ currentLocation, nextLocation }) =>
      unsaved &&
      `${currentLocation.pathname}${currentLocation.search}` !== `${nextLocation.pathname}${nextLocation.search}`,
    [unsaved],
  );
  const blocker = useBlocker(shouldBlock);
  useEffect(() => {
    if (blocker.state !== 'blocked') return;
    if (window.confirm(t`Discard unsaved changes to ${path}?`)) blocker.proceed();
    else blocker.reset();
  }, [blocker, path, t]);
  const unsavedRef = useRef(unsaved);
  unsavedRef.current = unsaved;
  useEffect(() => {
    const onBeforeUnload = (e: BeforeUnloadEvent): void => {
      if (unsavedRef.current) {
        e.preventDefault();
        e.returnValue = '';
      }
    };
    window.addEventListener('beforeunload', onBeforeUnload);
    return () => window.removeEventListener('beforeunload', onBeforeUnload);
  }, []);

  if (content === null && error === null) return <Loading label={t`doc…`} />;
  const dirty = draft !== content;

  return (
    <div className="space-y-2">
      <div className="font-mono text-[10px] text-ink-faint">{path}</div>
      {error !== null && (
        <div
          role="alert"
          className="rounded-md border border-red/40 bg-red/10 px-2.5 py-1.5 font-mono text-[11px] text-red"
        >
          {error}
        </div>
      )}
      {note !== null && !dirty && (
        <div className="rounded-md border border-green/30 bg-green/10 px-2.5 py-1.5 font-mono text-[11px] text-green">
          {note}
        </div>
      )}
      <textarea
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        spellCheck={false}
        aria-label={t`plan doc source`}
        className="min-h-[460px] w-full resize-y rounded-lg border border-line bg-field px-3.5 py-3 font-mono text-[12.5px] leading-relaxed text-ink outline-none focus:border-ink-dim"
      />
      <div className="flex items-center justify-end gap-2">
        <button
          type="button"
          disabled={!dirty || saving}
          onClick={() => setDraft(content ?? '')}
          className="rounded-md border border-line px-3 py-1.5 font-mono text-[11px] text-ink-dim transition-colors hover:text-ink disabled:cursor-not-allowed disabled:text-ink-faint focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-brand"
        >
          <Trans>revert</Trans>
        </button>
        <button
          type="button"
          disabled={!dirty || saving}
          onClick={save}
          className="rounded-md border border-line-strong bg-surface2 px-3 py-1.5 font-mono text-[11px] text-brand transition-colors hover:bg-surface2/70 disabled:cursor-not-allowed disabled:text-ink-faint focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-brand"
        >
          {saving ? t`saving…` : t`Save`}
        </button>
      </div>
    </div>
  );
}
