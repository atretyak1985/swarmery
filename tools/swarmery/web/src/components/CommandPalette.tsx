// Cmd+K command palette: global search overlay over GET /api/search plus a
// static client-side Navigation section. 150 ms debounced queries; results
// grouped Sessions / Messages / Files / Projects / Navigation; ↑/↓ + Enter
// navigates, Escape closes. Selecting a FILE result drills in place into the
// sessions that touched it (GET /api/files/sessions) — Escape or Backspace on
// an empty input backs out. No dedicated /files page by design (YAGNI): the
// palette IS the reverse-lookup surface.
//
// Snippets arrive with non-HTML ⟦…⟧ markers; <Snippet> splits on them and
// renders amber marks — transcript prose never touches innerHTML.

import type { I18n, MessageDescriptor } from '@lingui/core';
import { msg } from '@lingui/core/macro';
import { Plural, useLingui } from '@lingui/react/macro';
import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
} from 'react';
import { useNavigate } from 'react-router-dom';
import type {
  FileSession,
  SearchFile,
  SearchProject,
  SearchResponse,
  SearchSession,
  SearchTurn,
} from '../api/types';
import { fetchFileSessions, fetchSearch } from '../api';
import { useScope } from '../lib/scope';

interface NavEntry {
  label: MessageDescriptor;
  to: string;
}

/** Static client-side navigation targets — always available, no fetch. */
const NAV_ENTRIES: NavEntry[] = [
  { label: msg`Overview`, to: '/' },
  { label: msg`Sessions`, to: '/sessions' },
  { label: msg`Analytics`, to: '/analytics' },
  { label: msg`Approvals`, to: '/approvals' },
  { label: msg`Projects`, to: '/projects' },
  { label: msg`System`, to: '/system' },
  { label: msg`Docs`, to: '/docs' },
];

type PaletteItem =
  | { kind: 'session'; session: SearchSession }
  | { kind: 'turn'; turn: SearchTurn }
  | { kind: 'file'; file: SearchFile }
  | { kind: 'project'; project: SearchProject }
  | { kind: 'nav'; nav: NavEntry }
  | { kind: 'fileSession'; fs: FileSession };

interface Section {
  title: string;
  items: PaletteItem[];
}

function sectionsFor(query: string, results: SearchResponse | null, i18n: I18n): Section[] {
  const q = query.trim().toLowerCase();
  const sections: Section[] = [];
  if (results !== null) {
    if (results.sessions.length > 0) {
      sections.push({
        title: i18n._(msg`Sessions`),
        items: results.sessions.map((session) => ({ kind: 'session' as const, session })),
      });
    }
    if (results.turns.length > 0) {
      sections.push({
        title: i18n._(msg`Messages`),
        items: results.turns.map((turn) => ({ kind: 'turn' as const, turn })),
      });
    }
    if (results.files.length > 0) {
      sections.push({
        title: i18n._(msg`Files`),
        items: results.files.map((file) => ({ kind: 'file' as const, file })),
      });
    }
    if (results.projects.length > 0) {
      sections.push({
        title: i18n._(msg`Projects`),
        items: results.projects.map((project) => ({ kind: 'project' as const, project })),
      });
    }
  }
  // Match what the operator reads: the label in the active locale.
  const nav = NAV_ENTRIES.filter((n) => q === '' || i18n._(n.label).toLowerCase().includes(q));
  if (nav.length > 0) {
    sections.push({
      title: i18n._(msg`Navigation`),
      items: nav.map((n) => ({ kind: 'nav' as const, nav: n })),
    });
  }
  return sections;
}

export function CommandPalette({ onClose }: { onClose: () => void }): JSX.Element {
  const navigate = useNavigate();
  const { t, i18n } = useLingui();
  // Global project scope: search + file drill-in respect it like every page
  // does; the static Navigation section stays scope-independent.
  const { scope, scopeName } = useScope();
  const [query, setQuery] = useState('');
  const [results, setResults] = useState<SearchResponse | null>(null);
  // File drill-in: non-null while listing the sessions that touched one path.
  const [filePath, setFilePath] = useState<string | null>(null);
  const [fileSessions, setFileSessions] = useState<FileSession[]>([]);
  const [active, setActive] = useState(0);
  const listRef = useRef<HTMLDivElement>(null);
  const reqSeq = useRef(0);

  // Debounced search (150 ms); stale responses dropped by sequence number.
  // Suspended while drilled into a file: the drill-in list is not query-driven,
  // so firing here would only waste requests and reset the arrow position.
  useEffect(() => {
    if (filePath !== null) return undefined;
    const q = query.trim();
    if (q === '') {
      setResults(null);
      setActive(0);
      return undefined;
    }
    const seq = ++reqSeq.current;
    const timer = setTimeout(() => {
      fetchSearch(q, scope ?? undefined)
        .then((r) => {
          if (reqSeq.current === seq) {
            setResults(r);
            setActive(0);
          }
        })
        .catch(() => {
          if (reqSeq.current === seq) setResults(null);
        });
    }, 150);
    return () => clearTimeout(timer);
  }, [query, filePath, scope]);

  const sections = useMemo<Section[]>(() => {
    if (filePath !== null) {
      if (fileSessions.length === 0) return [];
      return [
        {
          title: t`Sessions touching file`,
          items: fileSessions.map((fs) => ({ kind: 'fileSession' as const, fs })),
        },
      ];
    }
    return sectionsFor(query, results, i18n);
  }, [filePath, fileSessions, query, results, i18n, t]);
  const flat = useMemo(() => sections.flatMap((s) => s.items), [sections]);

  function go(to: string): void {
    navigate(to);
    onClose();
  }

  function drillIntoFile(path: string): void {
    setFilePath(path);
    setFileSessions([]);
    setActive(0);
    // Same sequence counter as the debounced search: bumping it also
    // invalidates any in-flight search, and a rapid second drill-in (or
    // backing out and searching again) drops this response as stale.
    const seq = ++reqSeq.current;
    fetchFileSessions(path, scope ?? undefined)
      .then((r) => {
        if (reqSeq.current === seq) setFileSessions(r.sessions);
      })
      .catch(() => {
        if (reqSeq.current === seq) setFileSessions([]);
      });
  }

  function select(item: PaletteItem): void {
    switch (item.kind) {
      case 'session':
        go(`/sessions/${String(item.session.id)}`);
        break;
      case 'turn':
        go(`/sessions/${String(item.turn.sessionId)}`);
        break;
      case 'file':
        drillIntoFile(item.file.path);
        break;
      case 'project':
        go(`/projects/${String(item.project.id)}`);
        break;
      case 'nav':
        go(item.nav.to);
        break;
      case 'fileSession':
        go(`/sessions/${String(item.fs.sessionId)}`);
        break;
    }
  }

  function onKeyDown(e: ReactKeyboardEvent<HTMLInputElement>): void {
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      setActive((i) => (flat.length === 0 ? 0 : (i + 1) % flat.length));
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      setActive((i) => (flat.length === 0 ? 0 : (i - 1 + flat.length) % flat.length));
    } else if (e.key === 'Enter') {
      e.preventDefault();
      const item = flat[active];
      if (item !== undefined) select(item);
    } else if (e.key === 'Escape') {
      e.preventDefault();
      if (filePath !== null) setFilePath(null);
      else onClose();
    } else if (e.key === 'Backspace' && filePath !== null && query === '') {
      e.preventDefault();
      setFilePath(null);
    }
  }

  // Keep the active row visible while arrowing through a long list.
  useEffect(() => {
    listRef.current
      ?.querySelector(`[data-idx="${String(active)}"]`)
      ?.scrollIntoView({ block: 'nearest' });
  }, [active]);

  const scopeLabel = scopeName ?? scope ?? '';
  let idx = -1;
  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center bg-bg/70 p-4 pt-[12vh]"
      role="dialog"
      aria-modal="true"
      aria-label={t`Search`}
      onClick={onClose}
    >
      <div
        className="w-full max-w-xl overflow-hidden rounded-xl border border-line bg-surface"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center gap-2 border-b border-line-soft px-3.5 py-2.5">
          {scope !== null && (
            <span
              className="max-w-[30%] shrink-0 truncate rounded-[6px] border border-line-strong bg-surface2 px-1.5 py-0.5 font-mono text-[10.5px] text-ink-dim"
              data-tip={t`results scoped to ${scopeLabel}`}
            >
              {scopeName ?? scope}
            </span>
          )}
          {filePath !== null && (
            <span className="max-w-[40%] shrink-0 truncate rounded-[6px] border border-line-strong bg-surface2 px-1.5 py-0.5 font-mono text-[10.5px] text-brand">
              {filePath}
            </span>
          )}
          <input
            autoFocus
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={onKeyDown}
            placeholder={
              filePath !== null
                ? t`sessions touching this file…`
                : t`search sessions, messages, files…`
            }
            className="min-w-0 flex-1 bg-transparent font-mono text-[13px] text-ink outline-none"
          />
          {/* i18n-ignore — the key's name, as printed on the keyboard */}
          <span className="shrink-0 font-mono text-[10px] text-ink-faint">esc</span>
        </div>

        <div ref={listRef} className="max-h-[52vh] overflow-y-auto py-1.5">
          {sections.length === 0 && (
            <div className="px-3.5 py-3 font-mono text-[11.5px] text-ink-faint">
              {filePath !== null ? t`no sessions touched this file` : t`no matches`}
            </div>
          )}
          {sections.map((section) => (
            <div key={section.title}>
              <div className="px-3.5 pt-2 pb-1 font-mono text-[10px] tracking-[0.12em] text-ink-faint uppercase">
                {section.title}
              </div>
              {section.items.map((item) => {
                idx += 1;
                const i = idx;
                return (
                  <button
                    key={`${section.title}-${String(i)}`}
                    type="button"
                    data-idx={i}
                    onClick={() => select(item)}
                    onMouseMove={() => setActive(i)}
                    className={`flex w-full items-baseline gap-2 px-3.5 py-1.5 text-left ${
                      i === active ? 'bg-surface2' : ''
                    }`}
                  >
                    <ItemRow item={item} />
                  </button>
                );
              })}
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}

function ItemRow({ item }: { item: PaletteItem }): JSX.Element {
  const { t, i18n } = useLingui();
  switch (item.kind) {
    case 'session': {
      const sessionId = item.session.id;
      return (
        <>
          <span className="truncate text-[13px] text-ink">
            {item.session.title ?? item.session.gitBranch ?? t`session #${sessionId}`}
          </span>
          <span className="ml-auto shrink-0 font-mono text-[10.5px] text-ink-faint">
            {item.session.projectName ?? item.session.projectSlug} ·{' '}
            {item.session.startedAt.slice(0, 10)}
          </span>
        </>
      );
    }
    case 'turn':
      return (
        <>
          <span className="shrink-0 font-mono text-[10px] text-ink-faint uppercase">
            {item.turn.agentName ?? item.turn.role}
          </span>
          <span className="truncate text-[12.5px] text-ink-2">
            <Snippet text={item.turn.snippet} />
          </span>
          <span className="ml-auto shrink-0 font-mono text-[10.5px] text-ink-faint">
            {item.turn.sessionTitle ?? item.turn.projectName ?? item.turn.projectSlug}
          </span>
        </>
      );
    case 'file': {
      const sessions = item.file.sessions;
      return (
        <>
          <span className="truncate font-mono text-[12px] text-ink">{item.file.path}</span>
          <span className="ml-auto shrink-0 font-mono text-[10.5px] text-ink-faint">
            <Plural
              value={sessions}
              one="# session ❯"
              few="# sessions ❯"
              many="# sessions ❯"
              other="# sessions ❯"
            />
          </span>
        </>
      );
    }
    case 'project':
      return (
        <>
          <span className="truncate text-[13px] text-ink">
            {item.project.name ?? item.project.slug}
          </span>
          <span className="ml-auto shrink-0 font-mono text-[10.5px] text-ink-faint">{t`project`}</span>
        </>
      );
    case 'nav':
      return (
        <>
          <span className="truncate text-[13px] text-ink">{i18n._(item.nav.label)}</span>
          <span className="ml-auto shrink-0 font-mono text-[10.5px] text-ink-faint">
            {item.nav.to}
          </span>
        </>
      );
    case 'fileSession': {
      const sessionId = item.fs.sessionId;
      const changes = item.fs.changes;
      return (
        <>
          <span className="truncate text-[13px] text-ink">
            {item.fs.title ?? t`session #${sessionId}`}
          </span>
          <span className="ml-auto shrink-0 font-mono text-[10.5px] text-ink-faint">
            <Plural
              value={changes}
              one="# change"
              few="# changes"
              many="# changes"
              other="# changes"
            />{' '}
            ·{' '}
            {item.fs.lastTouched.slice(0, 10)}
          </span>
        </>
      );
    }
  }
}

/** Renders a server snippet, mapping ⟦…⟧ highlight markers to amber marks. */
function Snippet({ text }: { text: string }): JSX.Element {
  const parts = text.split(/⟦(.*?)⟧/g);
  return (
    <>
      {parts.map((part, i) =>
        i % 2 === 1 ? (
          <span key={i} className="rounded-[3px] bg-amber/20 px-0.5 text-brand">
            {part}
          </span>
        ) : (
          <span key={i}>{part}</span>
        ),
      )}
    </>
  );
}
