// Docs screen (Canvas): left DOCUMENTATION rail (mono eyebrow + title/FILE.md
// buttons, active item amber-tinted with a raised fill), right pane rendering
// the doc's markdown under a serif H1 and a "swarmery/docs/<FILE>" mono
// subline. Routes: /docs (first doc) and /docs/{slug}. The markdown body's
// own leading H1 is stripped — the pane title comes from the doc meta.

import { useEffect, useMemo, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import type { DocDetail, DocMeta } from '../api/types';
import { fetchDoc, fetchDocs } from '../api';
import { Markdown } from '../lib/markdown';
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

export function Docs(): JSX.Element {
  const { slug } = useParams<{ slug: string }>();
  const [docs, setDocs] = useState<DocMeta[] | null>(null);
  const [listError, setListError] = useState<string | null>(null);
  const [doc, setDoc] = useState<DocDetail | null>(null);
  const [docError, setDocError] = useState<string | null>(null);

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

  const rendered = useMemo(() => (doc === null ? null : stripLeadingH1(doc.markdown)), [doc]);

  if (listError !== null) return <ErrorBox message={listError} />;
  if (docs === null) return <Loading label="docs…" />;
  if (docs.length === 0) return <Empty>no docs published by the daemon</Empty>;

  return (
    <div className="min-w-0 px-4 pt-6 pb-10 desk:px-10 desk:pt-[34px] desk:pb-[60px]">
      <div className="desk:grid desk:grid-cols-[220px_minmax(0,1fr)] desk:items-start desk:gap-7">
        {/* Sticky offset is relative to the <main> scroller (frame layout). */}
        <div className="min-w-0 desk:sticky desk:top-[85px]">
          <div className="mb-2.5 font-mono text-[10.5px] tracking-[0.14em] text-ink-faint uppercase">
            Documentation
          </div>
          <nav className="flex flex-col gap-0.5" aria-label="Documentation pages">
            {docs.map((d) => {
              const active = d.slug === activeSlug;
              return (
                <Link
                  key={d.slug}
                  to={`/docs/${d.slug}`}
                  aria-current={active ? 'page' : undefined}
                  className={`min-h-[44px] rounded-lg px-3 py-[9px] transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand/60 ${
                    active ? 'bg-surface2' : 'hover:bg-surface2/50'
                  }`}
                >
                  <span
                    className={`block text-[13px] font-semibold ${active ? 'text-brand' : 'text-ink'}`}
                  >
                    {d.title}
                  </span>
                  <span className="mt-px block font-mono text-[10px] text-ink-faint">{d.file}</span>
                </Link>
              );
            })}
          </nav>
        </div>

        <div className="mt-6 min-w-0 desk:mt-0">
          {docError !== null && <ErrorBox message={docError} />}
          {doc === null && docError === null && <Loading label="doc…" />}
          {doc !== null && rendered !== null && (
            <>
              <h1 className="font-display text-[22px] font-medium tracking-[-0.01em] desk:text-[26px]">
                {rendered.title ?? doc.title}
              </h1>
              <div className="mt-1 font-mono text-[10.5px] text-ink-faint">
                swarmery/docs/{doc.file}
              </div>
              <div className="mt-5 text-[14px] leading-[1.75] text-ink-2">
                <Markdown text={rendered.body} />
              </div>
            </>
          )}
        </div>
      </div>
    </div>
  );
}
