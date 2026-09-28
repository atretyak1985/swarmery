// Docs screen (Canvas v2, vertical-rail variant): a left sidebar with a mono
// filter box on top and the docs grouped under GUIDES / REFERENCE / FORMATS /
// PROTOCOLS mono section labels (the rule lives in ./docsRail.ts) — active doc
// amber-tinted, each row showing title over its FILE.md. Beside it the
// reading grid: the article (mono group eyebrow · serif H1 ·
// "swarmery/docs/<FILE>" subline · markdown body · prev/next footer) and an "On this page" rail built from the doc's own `##` headings. Routes:
// /docs (first doc) and /docs/{slug}; every doc switch is a router navigation,
// never local state. The markdown body's own leading H1 is stripped — the page
// title comes from the doc meta.
//
// Fill route (`handle: { fill: true }`, src/main.tsx): the shell has handed the
// vertical scroll over, so this screen owns it. Each column is its own scroller
// inside the grid row instead of a `sticky` block capped against a viewport-unit
// height — the row IS the leftover height, so nothing here has to know the
// viewport, and the header/padding subtractions that cap used to hardcode go
// with it (they were a second copy of the shell's geometry, drifting on it). The
// vertical padding lives INSIDE each scrollport, so a scrollbar runs the whole
// column instead of stopping short at an outer inset. Below `desk` the three
// columns stack and the article keeps the scroll: the rail is capped at a
// fraction of the pane so a long doc set can never squeeze the article out.
//
// Body element metrics (paragraph rhythm, serif section headings, code blocks,
// heading scroll-margin) live in the scoped `.docs-article` block in index.css,
// so the shared <Markdown> renderer stays untouched.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link, useLocation, useParams } from 'react-router-dom';
import type { DocDetail, DocMeta } from '../api/types';
import { fetchDoc, fetchDocs } from '../api';
import { Markdown } from '../lib/markdown';
import { GROUP_ORDER, groupOf } from './docsRail';
import { Empty, ErrorBox, Loading } from '../components/ui';

/** Drop a leading `# Title` line — the pane renders its own heading. */
function stripLeadingH1(markdown: string): { title: string | null; body: string } {
  const lines = markdown.split('\n');
  let i = 0;
  while (i < lines.length && (lines[i] ?? '').trim() === '') i += 1;
  const m = /^#\s+(.*)$/.exec(lines[i] ?? '');
  if (m === null) return { title: null, body: markdown };
  return { title: m[1] ?? null, body: lines.slice(i + 1).join('\n') };
}

/* ----- table of contents ----- */

/** Heading id — MUST stay byte-identical to slugify() in lib/markdown.tsx,
 * which is what actually stamps the ids these entries scroll to. Duplicated
 * rather than exported because the renderer is a shared surface and this screen
 * is the only caller that owns the id namespace (Markdown `anchors`). */
function slugify(text: string): string {
  return text
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '');
}

interface TocEntry {
  id: string;
  label: string;
}

/** Section headings of the body — the `##` level, which the renderer emits as
 * <h3> (the pane owns h1, so `#`→h2 and `##`→h3). Fenced blocks are skipped so
 * a `## ` line inside a shell snippet never becomes a rail entry. */
function tocOf(markdown: string): TocEntry[] {
  const out: TocEntry[] = [];
  let fenced = false;
  for (const line of markdown.split('\n')) {
    if (line.trimStart().startsWith('```')) {
      fenced = !fenced;
      continue;
    }
    if (fenced) continue;
    const m = /^##\s+(.*)$/.exec(line);
    if (m === null) continue;
    const raw = (m[1] ?? '').trim();
    if (raw === '') continue;
    // The id comes from the RAW text (that is what the renderer slugifies);
    // the label drops inline code/emphasis markers, which read as noise here.
    out.push({ id: slugify(raw), label: raw.replace(/[`*]/g, '') });
  }
  return out;
}

export function Docs(): JSX.Element {
  const { slug } = useParams<{ slug: string }>();
  const [docs, setDocs] = useState<DocMeta[] | null>(null);
  const [listError, setListError] = useState<string | null>(null);
  const [doc, setDoc] = useState<DocDetail | null>(null);
  const [docError, setDocError] = useState<string | null>(null);
  const [query, setQuery] = useState('');
  const articleRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    fetchDocs()
      .then((list) => {
        setDocs(list);
        setListError(null);
      })
      .catch((e: unknown) => setListError(String(e)));
  }, []);

  const activeSlug = slug ?? docs?.[0]?.slug ?? null;

  useEffect(() => {
    if (activeSlug === null) return;
    setDoc(null);
    setDocError(null);
    fetchDoc(activeSlug)
      .then(setDoc)
      .catch((e: unknown) => setDocError(String(e)));
  }, [activeSlug]);

  const { hash, key } = useLocation();

  /** The scrollport every scroll on this screen has to go through. On a fill
   * route <main> has stopped scrolling (App.tsx), so it is no longer the answer
   * — the article column is, at every breakpoint. Resolved through the ref
   * rather than `closest('main')` so the target is the element that actually
   * owns the overflow, not whichever ancestor used to. */
  const scroller = useCallback((): HTMLElement | null => articleRef.current, []);

  // A new doc starts at its top, exactly as picking one from the rail implies.
  // Skipped when the URL carries an anchor: that navigation asked for a
  // specific heading, and the effect below is about to scroll there.
  useEffect(() => {
    if (hash !== '') return;
    scroller()?.scrollTo({ top: 0 });
  }, [activeSlug, hash, scroller]);

  // Deep links from <Explain>'s "Read more →" carry a heading anchor. The doc
  // body arrives asynchronously, so scroll after `doc` lands, not on mount.
  //
  // `key` is in the deps, not just `hash`: clicking an in-doc link to the
  // section you already came from is a same-URL navigation, so `hash` does not
  // change and a hash-only effect would silently do nothing. The router mints a
  // fresh key for every navigation, which is the signal that a jump was asked
  // for — this is what a native fragment anchor gives you for free and what we
  // give up by routing hash links (so that useLocation stays in sync at all).
  useEffect(() => {
    if (doc === null || hash === '') return;
    const raw = hash.slice(1);
    // A hand-typed or truncated hash can be invalid percent-encoding (`#%`),
    // and decodeURIComponent throws URIError on it. Thrown from inside an
    // effect that is a full-page crash, so a malformed anchor falls back to
    // the literal text — which simply matches no id.
    let id = raw;
    try {
      id = decodeURIComponent(raw);
    } catch {
      /* keep raw */
    }
    // scrollIntoView walks up to the nearest scrollable ancestor, which is now
    // the article column — the same scrollport scroller() returns. The
    // scroll-margin-top `.docs-article` puts on every heading is measured from
    // that scrollport's edge, so `block:'start'` lands the heading just below
    // the top of the article area rather than flush against it.
    const el = document.getElementById(id);
    if (el !== null) el.scrollIntoView({ block: 'start' });
  }, [doc, hash, key]);

  const rendered = useMemo(() => (doc === null ? null : stripLeadingH1(doc.markdown)), [doc]);
  const toc = useMemo(() => (rendered === null ? [] : tocOf(rendered.body)), [rendered]);

  const q = query.trim().toLowerCase();
  const groups = useMemo(() => {
    const all = docs ?? [];
    return GROUP_ORDER.map((name) => {
      const members = all.filter((d) => groupOf(d.file) === name);
      const items = members.filter(
        (d) =>
          q === '' || d.title.toLowerCase().includes(q) || d.file.toLowerCase().includes(q),
      );
      return { name, members, items };
    }).filter((g) => g.members.length > 0 && (q === '' || g.items.length > 0));
  }, [docs, q]);

  const noMatches = groups.every((g) => g.items.length === 0);

  const activeIdx = docs === null ? -1 : docs.findIndex((d) => d.slug === activeSlug);
  const prev = activeIdx > 0 ? (docs?.[activeIdx - 1] ?? null) : null;
  const next = activeIdx >= 0 ? (docs?.[activeIdx + 1] ?? null) : null;

  if (listError !== null) return <ErrorBox message={listError} />;
  if (docs === null) return <Loading label="docs…" />;
  if (docs.length === 0) return <Empty>no docs published by the daemon</Empty>;

  return (
    // `leading-[normal]` undoes the app-wide body leading of 1.5 for this screen:
    // the chrome (rail rows, filter box, eyebrow, H1, TOC) runs on the font's
    // natural line box, and 1.5 inflates each element by 2–8px, which compounds
    // into a visible downward drift. The article body re-declares its own 1.75.
    <div className="flex h-full min-h-0 min-w-0 max-w-[1280px] flex-col px-4 leading-[normal] desk:px-10">
      {/* One grid row that spends the leftover height: `grid-rows-[minmax(0,1fr)]`
          is what lets the row — and with it every column — be SHORTER than its
          content, which is the whole point of handing them their own scrollbars.
          Below `desk` it degrades to a flex column so the rail can be capped
          against the pane while the article takes the rest; grid `auto` rows
          would simply overflow instead. */}
      <div className="flex min-h-0 flex-1 flex-col gap-11 desk:grid desk:grid-cols-[220px_minmax(0,1fr)_168px] desk:grid-rows-[minmax(0,1fr)]">
        {/* Its own scrollport, so the last entry of a long doc set is always
            reachable without the page moving. The top padding sits inside it,
            level with the article's and the TOC's, so all three columns still
            open on the same mono line. Below `desk` the cap is what keeps a long
            doc set from squeezing the article out. It carries no bottom inset
            there: the column gap lives OUTSIDE this scrollport, so an inset
            would not buy breathing room at the scrolled-to-end edge — it would
            only double-space the two blocks. A long list therefore ends flush
            against the clip, which also reads as "there is more above". */}
        <nav
          aria-label="Documentation"
          className="max-h-[40%] min-h-0 min-w-0 overflow-y-auto pt-6 desk:h-full desk:max-h-none desk:pt-[26px] desk:pb-[60px]"
        >
          {/* The label row lines the rail up with the article's group eyebrow
              and the TOC's "On this page" — all three columns open with the
              same mono small-caps line, so their tops read as one line. */}
          <div className="mb-2 font-mono text-[10px] tracking-[0.14em] text-ink-faint uppercase">
            Documentation
          </div>
          <input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="filter docs…"
            aria-label="Filter docs"
            className="w-full rounded-lg border border-line-strong bg-field px-[11px] py-1.5 font-mono text-[11px] text-ink outline-none focus:border-brand"
          />
          {noMatches && <div className="mt-3 text-[12px] text-ink-faint">no docs match</div>}
          {groups.map((g) => (
            <div key={g.name} className="mt-5">
              <div className="mb-2 font-mono text-[10px] tracking-[0.14em] text-ink-faint uppercase">
                {g.name}
              </div>
              <div className="flex flex-col gap-0.5">
                {g.items.map((d) => {
                  const active = d.slug === activeSlug;
                  return (
                    <Link
                      key={d.slug}
                      to={`/docs/${d.slug}`}
                      aria-current={active ? 'page' : undefined}
                      className={`rounded-[7px] px-3 py-[7px] transition-colors hover:bg-surface2 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand/60 ${
                        active ? 'bg-brand/[0.08]' : ''
                      }`}
                    >
                      <span
                        className={`block text-[13px] font-semibold ${active ? 'text-brand' : 'text-ink'}`}
                      >
                        {d.title}
                      </span>
                      <span className="mt-px block font-mono text-[10px] text-ink-faint">
                        {d.file}
                      </span>
                    </Link>
                  );
                })}
              </div>
            </div>
          ))}
        </nav>

        {/* THE scroller of this screen — what scroller() returns and what the
            slug-change reset, the `#hash` jump and the TOC clicks all move.
            `flex-1` only bites below `desk` (grid items ignore it), where the
            article takes whatever the capped rail leaves. No top inset there:
            the column gap sits OUTSIDE the scrollport, so it is permanent
            whitespace the body never scrolls under, and a `pt` on top of it
            would just push the article down. */}
        <div
          ref={articleRef}
          className="min-h-0 min-w-0 max-w-[720px] flex-1 overflow-y-auto pb-10 [-webkit-overflow-scrolling:touch] desk:h-full desk:pt-[26px] desk:pb-[60px]"
        >
          {docError !== null && <ErrorBox message={docError} />}
          {doc === null && docError === null && <Loading label="doc…" />}
          {doc !== null && rendered !== null && (
            <>
              <div className="font-mono text-[10.5px] tracking-[0.14em] text-ink-faint uppercase">
                {groupOf(doc.file)}
              </div>
              <h1 className="mt-1.5 font-display text-[22px] font-medium tracking-[-0.01em] desk:text-[28px]">
                {rendered.title ?? doc.title}
              </h1>
              <div className="mt-[5px] font-mono text-[10.5px] text-ink-faint">
                swarmery/docs/{doc.file}
              </div>
              {/* The one surface that renders a single body per page, so it
                  owns the heading-id namespace the deep links resolve against.
                  `.docs-article` carries the body element metrics (index.css). */}
              <div className="docs-article mt-[22px] text-[14px] leading-[1.75] text-ink-2">
                <Markdown text={rendered.body} anchors codeLabels />
              </div>
              {(prev !== null || next !== null) && (
                <div className="mt-9 flex justify-between gap-3.5 border-t border-line pt-[18px]">
                  {prev !== null && (
                    <Link to={`/docs/${prev.slug}`} className="min-w-0 transition-opacity hover:opacity-80">
                      <div className="font-mono text-[10px] tracking-[0.12em] text-ink-faint uppercase">
                        ← previous
                      </div>
                      <div className="mt-[3px] text-[13.5px] font-semibold text-brand">
                        {prev.title}
                      </div>
                    </Link>
                  )}
                  {next !== null && (
                    <Link
                      to={`/docs/${next.slug}`}
                      className="ml-auto min-w-0 text-right transition-opacity hover:opacity-80"
                    >
                      <div className="font-mono text-[10px] tracking-[0.12em] text-ink-faint uppercase">
                        next →
                      </div>
                      <div className="mt-[3px] text-[13.5px] font-semibold text-brand">
                        {next.title}
                      </div>
                    </Link>
                  )}
                </div>
              )}
            </>
          )}
        </div>

        {toc.length > 0 && (
          <nav
            aria-label="On this page"
            className="hidden desk:block desk:h-full desk:min-h-0 desk:overflow-y-auto desk:pt-[26px] desk:pb-[60px]"
          >
            <div className="mb-2 font-mono text-[10px] tracking-[0.14em] text-ink-faint uppercase">
              On this page
            </div>
            <div className="flex flex-col gap-0.5 border-l border-line">
              {/* The `§ NN` prefixes mirror the CSS counters `.docs-article h3`
                  puts on the section headings (index.css) — same numbers, so
                  the rail doubles as a section index. */}
              {toc.map((t, n) => (
                <button
                  key={t.id}
                  type="button"
                  onClick={() => document.getElementById(t.id)?.scrollIntoView({ block: 'start' })}
                  className="flex items-baseline gap-2 py-1 pl-3 text-left text-[12px] text-ink-dim transition-colors hover:text-ink"
                >
                  <span aria-hidden="true" className="font-mono text-[9.5px] text-ink-faint">
                    {String(n + 1).padStart(2, '0')}
                  </span>
                  <span className="min-w-0">{t.label}</span>
                </button>
              ))}
            </div>
          </nav>
        )}
      </div>
    </div>
  );
}
