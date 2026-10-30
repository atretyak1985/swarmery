// Docs (v4, handoff §3, §9): the frame every place shares. The list is the accordion
// of GUIDES / REFERENCE / FORMATS / PROTOCOLS (the rule lives in ./docsRail.ts) with
// the doc's FILE.md as its summary; the detail is the eyebrow, the title, the
// markdown body with an "On this page" rail built from its own `##` headings, and
// the prev/next footer. Routes: /docs (first doc) and /docs/{slug}; every doc switch
// is a router navigation, never local state, and the list and the open doc re-fetch
// on a language switch. The markdown body's own leading H1 is stripped — the page
// title comes from the doc meta.
//
// The frame owns the scroll: the detail pane from `desk` up, the whole place below
// it (PlaceLayout), so nothing here sets a scroller of its own. Body element metrics
// (paragraph rhythm, section headings, code blocks, heading scroll-margin) live in
// the scoped `.docs-article` block in index.css, so the shared <Markdown> renderer
// stays untouched.

import { Trans, useLingui } from '@lingui/react/macro';
import { useEffect, useMemo, useRef, useState } from 'react';
import { Link, useLocation, useNavigate, useParams } from 'react-router-dom';
import type { DocDetail, DocMeta } from '../api/types';
import { fetchDoc, fetchDocs } from '../api';
import { headingId, splitHeadingId } from '../lib/headingId';
import { Markdown } from '../lib/markdown';
import { GROUP_LABEL, GROUP_ORDER, groupOf } from './docsRail';
import { PageDetail, PagePlace, type PlaceGroup } from '../components/place/PageDetail';
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
    // The id comes from the RAW text through the same headingId() the renderer
    // stamps with (explicit ` {#id}` first, slug otherwise); the label drops
    // that suffix and inline code/emphasis markers, which read as noise here.
    out.push({ id: headingId(raw), label: splitHeadingId(raw).text.replace(/[`*]/g, '') });
  }
  return out;
}

/** The nearest ancestor that scrolls: the detail pane from `desk` up, the place below it. */
function scrollParent(el: HTMLElement | null): HTMLElement | null {
  for (let node = el?.parentElement ?? null; node !== null; node = node.parentElement) {
    const overflow = getComputedStyle(node).overflowY;
    if (overflow === 'auto' || overflow === 'scroll') return node;
  }
  return null;
}

export function Docs(): JSX.Element {
  const { t, i18n } = useLingui();
  // The active UI language. Both fetches depend on it: switching the language
  // in the header re-fetches the list (titles) and the open doc in place — the
  // API client reads the same locale to pick `?lang=` (fetchDocs/fetchDoc).
  const locale = i18n.locale;
  const { slug } = useParams<{ slug: string }>();
  const [docs, setDocs] = useState<DocMeta[] | null>(null);
  const [listError, setListError] = useState<string | null>(null);
  const [doc, setDoc] = useState<DocDetail | null>(null);
  const [docError, setDocError] = useState<string | null>(null);
  const navigate = useNavigate();
  const articleRef = useRef<HTMLDivElement>(null);

  // `locale` is the re-fetch trigger: fetchDocs reads it through currentLocale().
  useEffect(() => {
    fetchDocs()
      .then((list) => {
        setDocs(list);
        setListError(null);
      })
      .catch((e: unknown) => setListError(String(e)));
  }, [locale]);

  const activeSlug = slug ?? docs?.[0]?.slug ?? null;

  // `locale` is a re-fetch trigger too: fetchDoc reads it through currentLocale().
  useEffect(() => {
    if (activeSlug === null) return;
    setDoc(null);
    setDocError(null);
    fetchDoc(activeSlug)
      .then(setDoc)
      .catch((e: unknown) => setDocError(String(e)));
  }, [activeSlug, locale]);

  const { hash, key } = useLocation();

  // A new doc starts at its top, exactly as picking one from the rail implies.
  // Skipped when the URL carries an anchor: that navigation asked for a
  // specific heading, and the effect below is about to scroll there.
  useEffect(() => {
    if (hash !== '') return;
    const pane = scrollParent(articleRef.current);
    if (pane !== null) pane.scrollTop = 0;
  }, [activeSlug, hash]);

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
    // scrollIntoView walks up to the nearest scrollable ancestor, the frame's pane.
    // The scroll-margin-top `.docs-article` puts on every heading is measured from
    // that edge, so `block:'start'` lands the heading just below the pane's top.
    const el = document.getElementById(id);
    if (el !== null) el.scrollIntoView({ block: 'start' });
  }, [doc, hash, key]);

  const rendered = useMemo(() => (doc === null ? null : stripLeadingH1(doc.markdown)), [doc]);
  const toc = useMemo(() => (rendered === null ? [] : tocOf(rendered.body)), [rendered]);

  const groups: PlaceGroup[] = useMemo(
    () =>
      GROUP_ORDER.map((name) => ({
        key: name,
        label: i18n._(GROUP_LABEL[name]),
        items: (docs ?? [])
          .filter((d) => groupOf(d.file) === name)
          .map((d) => ({ key: d.slug, glyph: '§', title: d.title, summary: d.file })),
      })).filter((group) => group.items.length > 0),
    [docs, i18n],
  );

  const activeIdx = docs === null ? -1 : docs.findIndex((d) => d.slug === activeSlug);
  const prev = activeIdx > 0 ? (docs?.[activeIdx - 1] ?? null) : null;
  const next = activeIdx >= 0 ? (docs?.[activeIdx + 1] ?? null) : null;

  if (listError !== null) return <ErrorBox message={listError} />;
  if (docs === null) return <Loading label={t`docs…`} />;
  if (docs.length === 0) {
    return (
      <Empty>
        <Trans>no docs published by the daemon</Trans>
      </Empty>
    );
  }

  const activeDoc = docs.find((d) => d.slug === activeSlug);
  const openGroup = activeDoc === undefined ? (groups[0]?.key ?? '') : groupOf(activeDoc.file);

  const detail = (): JSX.Element => {
    if (docError !== null) return <ErrorBox message={docError} />;
    if (doc === null || rendered === null) return <Loading label={t`doc…`} />;
    return (
      <PageDetail
        kind={t`docs`}
        meta={`${i18n._(GROUP_LABEL[groupOf(doc.file)])} · swarmery/docs/${doc.file}`}
        title={rendered.title ?? doc.title}
      >
        {/* A Ukrainian interface asked for `?lang=uk`, and the daemon had no
            translation of this doc (or an older build ignored the parameter): say
            so instead of passing English off silently. */}
        {locale === 'uk' && doc.lang !== 'uk' && (
          <p className="mt-3 max-w-[70ch] rounded-lg border border-line bg-surface2 px-3 py-2 text-[12px] text-ink-dim">
            <Trans>This document is not translated yet; the English original is shown.</Trans>
          </p>
        )}
        <div className="grid grid-cols-1 gap-8 desk:grid-cols-[minmax(0,1fr)_168px]">
          <div className="min-w-0">
            {/* The one surface that renders a single body per page, so it owns the
                heading-id namespace the deep links resolve against. */}
            <div className="docs-article mt-[18px] max-w-[70ch] text-[13.5px] leading-[1.65] text-ink-2">
              <Markdown text={rendered.body} anchors codeLabels />
            </div>
            {(prev !== null || next !== null) && (
              <div className="mt-9 flex max-w-[70ch] justify-between gap-3.5 border-t border-line pt-[18px]">
                {prev !== null && (
                  <Link to={`/docs/${prev.slug}`} className="min-w-0 transition-opacity hover:opacity-80">
                    <div className="font-mono text-[10px] tracking-[0.12em] text-ink-faint uppercase">
                      <Trans>← previous</Trans>
                    </div>
                    <div className="mt-[3px] text-[13.5px] font-semibold text-brand">{prev.title}</div>
                  </Link>
                )}
                {next !== null && (
                  <Link to={`/docs/${next.slug}`} className="ml-auto min-w-0 text-right transition-opacity hover:opacity-80">
                    <div className="font-mono text-[10px] tracking-[0.12em] text-ink-faint uppercase">
                      <Trans>next →</Trans>
                    </div>
                    <div className="mt-[3px] text-[13.5px] font-semibold text-brand">{next.title}</div>
                  </Link>
                )}
              </div>
            )}
          </div>
          {toc.length > 0 && (
            <nav
              aria-label={t`On this page`}
              className="hidden desk:sticky desk:top-4 desk:mt-[18px] desk:block desk:max-h-[80vh] desk:self-start desk:overflow-y-auto"
            >
              <div className="mb-2 font-mono text-[10px] tracking-[0.12em] text-ink-faint uppercase">
                <Trans>On this page</Trans>
              </div>
              <div className="flex flex-col gap-0.5 border-l border-line">
                {/* The `§ NN` prefixes mirror the CSS counters `.docs-article h3` puts
                    on the section headings (index.css): same numbers, so the rail
                    doubles as a section index. */}
                {toc.map((entry, n) => (
                  <button
                    key={entry.id}
                    type="button"
                    onClick={() => document.getElementById(entry.id)?.scrollIntoView({ block: 'start' })}
                    className="flex cursor-pointer items-baseline gap-2 py-1 pl-3 text-left text-[12px] text-ink-dim transition-colors hover:text-ink"
                  >
                    <span aria-hidden="true" className="font-mono text-[9.5px] text-ink-faint">
                      {String(n + 1).padStart(2, '0')}
                    </span>
                    <span className="min-w-0">{entry.label}</span>
                  </button>
                ))}
              </div>
            </nav>
          )}
        </div>
      </PageDetail>
    );
  };

  const go = (docSlug: string): void => {
    void navigate(`/docs/${docSlug}`);
  };

  return (
    <PagePlace
      title={t`Docs`}
      sub={t`how swarmery works`}
      searchHint={t`search docs`}
      groups={groups}
      openGroup={openGroup}
      selected={activeSlug}
      onOpenGroup={(key) => {
        const first = groups.find((group) => group.key === key)?.items[0];
        if (first !== undefined) go(first.key);
      }}
      onSelect={go}
      detail={<div ref={articleRef}>{detail()}</div>}
    />
  );
}
