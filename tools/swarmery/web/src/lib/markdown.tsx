// Minimal hand-rolled markdown renderer (Chat tab + Docs screen). Its only
// dependency is react-router's <Link>, so that an in-app link navigates
// through the router rather than reloading the SPA.
//
// XSS-safe by construction: it never builds HTML strings (no
// dangerouslySetInnerHTML); every fragment becomes a React text node, which
// React escapes. A raw `href=` is emitted only for an href that matched
// `^https?://`; every other shape either becomes a router <Link> to a route
// this app owns, or plain text — so `javascript:`/`data:` cannot survive.
//
// The one deliberate exception lives in docBlocks.tsx: a ```mermaid fence
// assigns mermaid's own sanitised SVG string to innerHTML, because mermaid
// ships no React renderer. See that file's header for why it is safe and why
// nothing else here may follow it.
//
// Supported: paragraphs, headings (#–####, with an optional trailing
// ` {#explicit-id}` under `anchors`), fenced code blocks,
// unordered/ordered lists, pipe tables, **bold**, *italic*, `inline code`,
// [links](href).
//
// Doc-surface fences dispatch on the info string (the word after ```):
//   ```mermaid        → a lazily-chunked mermaid diagram
//   ```stats          → a strip of `value | label [| hot]` tiles
//   ```figure <name>  → a canonical illustration from docFigures.tsx
// Any other info string (```go, bare ```) keeps the plain <pre> path (or the
// labelled box under `codeLabels`), so the three dialects degrade to ordinary
// code blocks on GitHub. Blockquotes support GitHub admonitions: a first line
// of `[!NOTE]`/`[!TIP]`/`[!WARNING]`/`[!IMPORTANT]` becomes a styled callout.
//
// Known limitations, both a consequence of the deliberately flat inline regex:
//   · nested brackets — `[a [b] c](d)` matches nothing (the label class
//     excludes `]`, so the scan stops at the inner one) and renders literally.
//   · parenthesised hrefs — `[w](https://ex.com/Foo_(bar))` truncates the href
//     at the inner `)`, linking `https://ex.com/Foo_(bar` and leaving a stray
//     `)` in the prose. Percent-encode the parens (`%28`/`%29`) to link them.

import { Fragment, type ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { Callout, CodeBlock, MermaidBlock, StatsStrip, calloutType } from './docBlocks';
import { headingId, splitHeadingId } from './headingId';
import { DocFigure } from './docFigures';

/* ----- inline: `code` | [link](href) | **bold** | *italic* ----- */

/** Split an href into path and fragment, lowercasing the fragment.
 *
 * Heading ids come from slugify(), which lowercases unconditionally, so
 * `CONCEPTS.md#Handoff` must become `#handoff` or it scrolls nowhere and the
 * reader is left staring at the top of the right page with no clue why. */
function splitFrag(href: string): [path: string, frag: string] {
  const at = href.indexOf('#');
  return at < 0 ? [href, ''] : [href.slice(0, at), href.slice(at).toLowerCase()];
}

/** The docs pane's route target for a relative href, or null when this
 * renderer cannot resolve it.
 *
 * The daemon slugs a doc by its lowercased BASENAME minus `.md`
 * (internal/api/docs.go), so basename is the only mapping that can agree
 * with what /api/docs actually serves:
 *   EXTENDING.md                → /docs/extending
 *   docs/PLUGINS.md#How-A-…     → /docs/plugins#how-a-…
 *   extending                   → /docs/extending  (extension-less sibling
 *                                 link, the shape docs/WORKFLOW.md uses)
 *
 * An href that walks upward is refused outright: basename-slugging
 * `../README.md` would silently aim at an unrelated doc.
 *
 * An unknown-but-well-formed slug is still safe to link — `/docs/:slug` is a
 * real route, so it renders the pane's own "doc not found" box. It is the
 * hrefs that match NO route that must never become links (see MarkdownLink). */
function docTarget(href: string): string | null {
  if (href.includes('../')) return null;
  const [path, frag] = splitFrag(href);
  const md = /^(?:[^/?]*\/)*([^/?]+)\.md$/i.exec(path);
  if (md !== null) return `/docs/${(md[1] ?? '').toLowerCase()}${frag}`;
  const bare = /^[a-z0-9][a-z0-9_-]*$/i.exec(path);
  if (bare !== null) return `/docs/${path.toLowerCase()}${frag}`;
  return null;
}

/** Render a markdown link, or its bare label when the href resolves to no
 * route this app serves.
 *
 * Falling back to inert text is the point. The router registers a fixed set of
 * paths with no catch-all and no errorElement, so a <Link> to an unmatched
 * path throws react-router's full-page "Unexpected Application Error" and
 * takes the app shell down with it. `mailto:`, `tel:`, image paths and
 * anything else exotic therefore stay text — exactly what they rendered as
 * before links were supported.
 *
 * The one place that trade is knowingly reversed is an absolute in-app path
 * (`/settings`, `/docs/concepts#handoff`): it is the shape a doc author
 * reaches for first, and swallowing it silently is the worse failure. A
 * MISTYPED absolute path can therefore still hit the router's error boundary
 * — but it is a bug in a committed doc, caught in review, not the systematic
 * breakage that relative hrefs produced. `/docs/*` cannot fail either way,
 * since `docs/:slug` matches any slug. */
function MarkdownLink({
  href,
  label,
  labelKey,
}: {
  href: string;
  label: string;
  labelKey: string;
}): JSX.Element {
  const cls = 'text-brand underline decoration-brand/40 underline-offset-2 hover:decoration-brand';
  const body = renderInline(label, labelKey);

  // Scheme is case-insensitive per RFC 3986 — `HTTPS://…` is a valid external
  // link, and without /i it would fall all the way through to inert text.
  if (/^https?:\/\//i.test(href)) {
    return (
      <a href={href} target="_blank" rel="noreferrer noopener" className={cls}>
        {body}
      </a>
    );
  }
  // Same-page fragment. A <Link> rather than a bare <a href="#…">: native
  // fragment navigation fires `hashchange`, which the router does not observe,
  // so useLocation().hash would go stale and Docs.tsx's scroll effect would
  // never re-run. Routing it keeps in-doc jumps on the same path as deep links.
  if (href.startsWith('#')) {
    return (
      <Link to={href.toLowerCase()} className={cls}>
        {body}
      </Link>
    );
  }
  // Any authority or scheme left at this point is not ours to route. This MUST
  // precede the absolute-path branch below: `//evil.com/x.md` also starts with
  // `/`, and routing it would emit a protocol-relative href the browser
  // resolves cross-host — a link whose label lies about where it goes.
  if (/^[^#?]*(\/\/|:)/.test(href)) return <>{body}</>;

  const target = href.startsWith('/') ? splitFrag(href).join('') : docTarget(href);
  if (target !== null) {
    return (
      <Link to={target} className={cls}>
        {body}
      </Link>
    );
  }
  return <>{body}</>;
}

function renderInline(text: string, keyBase: string): ReactNode[] {
  // Fresh regex per call: a shared module-level /g regex would have its
  // lastIndex clobbered by the recursive bold/italic calls below.
  //
  // The link alternative sits ahead of the emphasis ones for readability, not
  // for correctness: a link starts at `[` and emphasis at `*`, so the two never
  // compete for the same index. What actually protects a label containing `*`
  // is leftmost-match — the `[` is reached first, and the whole link is
  // consumed before the scan resumes past it.
  //
  // The leading `(!?)` captures an image's bang so `![alt](pic.png)` is
  // consumed whole. Without it the link alternative would eat `[alt](pic.png)`
  // and strand the `!` in the prose beside a link to an asset the daemon does
  // not serve.
  const inline = /`([^`]+)`|(!?)\[([^\]]+)\]\(([^)\s]+)\)|\*\*([^*]+)\*\*|\*([^*\n]+)\*/g;
  const out: ReactNode[] = [];
  let last = 0;
  let i = 0;
  for (let m = inline.exec(text); m !== null; m = inline.exec(text)) {
    if (m.index > last) out.push(text.slice(last, m.index));
    const key = `${keyBase}-${String(i)}`;
    const [, code, bang, linkText, linkHref, bold, italic] = m;
    if (code !== undefined) {
      out.push(
        <code key={key} className="rounded bg-surface2 px-1 py-px font-mono text-[0.88em] text-brand">
          {code}
        </code>,
      );
    } else if (linkText !== undefined && linkHref !== undefined) {
      // Images degrade to their alt text: /api/docs serves markdown only, so
      // there is no URL an <img> here could actually load.
      out.push(
        bang === '!' ? (
          <Fragment key={key}>{renderInline(linkText, `${key}-a`)}</Fragment>
        ) : (
          <MarkdownLink key={key} href={linkHref} label={linkText} labelKey={`${key}-l`} />
        ),
      );
    } else if (bold !== undefined) {
      out.push(
        <strong key={key} className="font-semibold text-ink">
          {renderInline(bold, `${key}-b`)}
        </strong>,
      );
    } else if (italic !== undefined) {
      out.push(<em key={key}>{renderInline(italic, `${key}-i`)}</em>);
    }
    last = m.index + m[0].length;
    i += 1;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

/* ----- blocks ----- */

const HEADING_SIZES: Record<number, string> = {
  1: 'text-[15px]',
  2: 'text-[14px]',
  3: 'text-[13.5px]',
  4: 'text-[13px]',
};

/* ----- pipe tables — `| a | b |` header + `|---|---|` separator ----- */

function isTableRow(s: string): boolean {
  return s.trimStart().startsWith('|');
}

function isTableSeparator(s: string): boolean {
  const t = s.trim();
  return /^\|?[\s:|-]+\|?$/.test(t) && t.includes('-') && t.includes('|');
}

/** `| a | b |` → ['a', 'b'] (no escaped-pipe support — docs don't use it). */
function splitCells(row: string): string[] {
  return row
    .trim()
    .replace(/^\|/, '')
    .replace(/\|$/, '')
    .split('|')
    .map((c) => c.trim());
}

function renderTable(header: string, body: string[], key: string): ReactNode {
  return (
    <table key={key} className="my-2 w-full border-collapse first:mt-0 last:mb-0">
      <thead>
        <tr>
          {splitCells(header).map((cell, c) => (
            <th
              key={`${key}-h-${String(c)}`}
              className="border-b border-line px-2.5 py-1.5 text-left font-mono text-[10.5px] font-medium tracking-[0.06em] text-ink-dim uppercase"
            >
              {renderInline(cell, `${key}-h-${String(c)}`)}
            </th>
          ))}
        </tr>
      </thead>
      <tbody>
        {body.map((row, r) => (
          <tr key={`${key}-r-${String(r)}`}>
            {splitCells(row).map((cell, c) => (
              <td
                key={`${key}-r-${String(r)}-${String(c)}`}
                className="border-b border-line-soft px-2.5 py-[7px] align-top text-[13px] leading-normal"
              >
                {renderInline(cell, `${key}-r-${String(r)}-${String(c)}`)}
              </td>
            ))}
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function flushParagraph(lines: string[], key: string, out: ReactNode[]): void {
  if (lines.length === 0) return;
  out.push(
    <p key={key} className="my-2 leading-relaxed whitespace-pre-line first:mt-0 last:mb-0">
      {renderInline(lines.join('\n'), key)}
    </p>,
  );
  lines.length = 0;
}

/** Renders markdown source as React elements (block-level walk).
 *
 * `anchors` opts into `id={headingId(heading)}` — the heading's explicit
 * ` {#id}` suffix when it has one (stripped from the visible text), its slug
 * otherwise (lib/headingId.ts) — and is off by default because an
 * id must be unique in the document. Most surfaces mount SEVERAL independent
 * <Markdown> blocks on one page — Plans.tsx renders a completion report, a
 * plan summary and a doc body side by side, Chat.tsx one per message — where
 * two `## Summary` headings would collide into a duplicate `id="summary"`.
 * Docs.tsx renders exactly one body per page, so it is the one caller that can
 * safely own the id namespace, and the only one that needs it (deep links).
 *
 * `codeLabels` opts into a mono chip naming the fence's info string (```json →
 * "json") on a header strip above the code. Off by default: chat bubbles and
 * plan panes render many small snippets where the strip is noise; the docs
 * reading pane is the surface the label was designed for. */
export function Markdown({
  text,
  anchors = false,
  codeLabels = false,
}: {
  text: string;
  anchors?: boolean;
  codeLabels?: boolean;
}): JSX.Element {
  const lines = text.split('\n');
  const out: ReactNode[] = [];
  const para: string[] = [];
  let i = 0;

  while (i < lines.length) {
    const line = lines[i] ?? '';
    const key = `md-${String(i)}`;

    // Fenced code block.
    if (line.trimStart().startsWith('```')) {
      flushParagraph(para, `${key}-p`, out);
      const info = line.trim().slice(3).trim();
      const code: string[] = [];
      i += 1;
      while (i < lines.length && !(lines[i] ?? '').trimStart().startsWith('```')) {
        code.push(lines[i] ?? '');
        i += 1;
      }
      i += 1; // closing fence (or EOF)
      const body = code.join('\n');
      const dialect = info.toLowerCase();
      if (dialect === 'mermaid') {
        out.push(<MermaidBlock key={key} code={body} />);
      } else if (dialect === 'stats') {
        out.push(<StatsStrip key={key} source={body} />);
      } else if (dialect === 'figure' || dialect.startsWith('figure ')) {
        out.push(<DocFigure key={key} name={dialect.slice(6).trim()} />);
      } else if (codeLabels && info !== '') {
        // Labelled variant: the border moves to a wrapper so the label strip
        // and the code share one rounded box.
        out.push(
          <div
            key={key}
            className="md-codeblock my-2 overflow-hidden rounded-lg border border-line bg-surface first:mt-0 last:mb-0"
          >
            <div className="border-b border-line-soft px-3 py-1 font-mono text-[9.5px] tracking-[0.12em] text-ink-faint uppercase">
              {info}
            </div>
            <pre className="overflow-x-auto px-3 py-2.5 font-mono text-[11px] leading-relaxed text-ink-2">
              <code>{body}</code>
            </pre>
          </div>,
        );
      } else {
        out.push(<CodeBlock key={key} code={body} />);
      }
      continue;
    }

    // Thematic break. Only when no paragraph is being collected: a `---`
    // directly under text would be a setext underline in full markdown, which
    // this renderer has never supported — so it must not eat one either.
    if (para.length === 0 && /^(-{3,}|\*{3,}|_{3,})$/.test(line.trim())) {
      out.push(<hr key={key} className="my-4 border-line" />);
      i += 1;
      continue;
    }

    // Blockquote — consecutive `>` lines form one quote. Without this branch a
    // `> note` line would fall through to the paragraph collector and render
    // with its literal `>` marker. A leading GitHub admonition marker
    // (`> [!NOTE]`) becomes a mono label chip instead of literal text.
    if (line.trimStart().startsWith('>')) {
      flushParagraph(para, `${key}-p`, out);
      const quote: string[] = [];
      while (i < lines.length && (lines[i] ?? '').trimStart().startsWith('>')) {
        quote.push((lines[i] ?? '').replace(/^\s*>\s?/, ''));
        i += 1;
      }
      const type = calloutType(quote[0] ?? '');
      if (type !== null) {
        out.push(
          <Callout key={key} type={type}>
            {renderInline(quote.slice(1).join('\n'), `${key}-c`)}
          </Callout>,
        );
      } else {
        out.push(
          <blockquote
            key={key}
            className="my-2 border-l-2 border-line-strong pl-3 leading-relaxed whitespace-pre-line text-ink-3 first:mt-0 last:mb-0"
          >
            {renderInline(quote.join('\n'), key)}
          </blockquote>,
        );
      }
      continue;
    }

    // Pipe table: header row + `|---|` separator, then body rows.
    if (isTableRow(line) && isTableSeparator(lines[i + 1] ?? '')) {
      flushParagraph(para, `${key}-p`, out);
      const header = line;
      i += 2; // header + separator
      const body: string[] = [];
      while (i < lines.length && isTableRow(lines[i] ?? '') && !isTableSeparator(lines[i] ?? '')) {
        body.push(lines[i] ?? '');
        i += 1;
      }
      out.push(renderTable(header, body, key));
      continue;
    }

    // Heading. Real h2–h5 (the pane owns the h1) with a slug id so /docs
    // deep links like /docs/concepts#handoff resolve.
    const h = /^(#{1,4})\s+(.*)$/.exec(line);
    if (h !== null) {
      flushParagraph(para, `${key}-p`, out);
      const level = (h[1] ?? '#').length;
      const raw = h[2] ?? '';
      // The `{#id}` suffix is attribute syntax, never prose: stripped on every
      // surface, honoured as the id only where this renderer owns the ids.
      const { text } = splitHeadingId(raw);
      const Tag = (['h2', 'h3', 'h4', 'h5'] as const)[level - 1] ?? 'h5';
      out.push(
        <Tag
          key={key}
          id={anchors ? headingId(raw) : undefined}
          className={`mt-3 mb-1.5 font-semibold text-ink first:mt-0 ${HEADING_SIZES[level] ?? 'text-[13px]'}`}
        >
          {renderInline(text, key)}
        </Tag>,
      );
      i += 1;
      continue;
    }

    // List (unordered or ordered) — consecutive item lines form one list.
    const isItem = (s: string): boolean => /^\s*([-*]|\d+\.)\s+/.test(s);
    if (isItem(line)) {
      flushParagraph(para, `${key}-p`, out);
      const ordered = /^\s*\d+\.\s+/.test(line);
      const items: string[] = [];
      while (i < lines.length && isItem(lines[i] ?? '')) {
        items.push((lines[i] ?? '').replace(/^\s*([-*]|\d+\.)\s+/, ''));
        i += 1;
      }
      const rows = items.map((item, n) => (
        <li key={`${key}-li-${String(n)}`}>{renderInline(item, `${key}-li-${String(n)}`)}</li>
      ));
      out.push(
        ordered ? (
          <ol key={key} className="my-2 list-decimal space-y-1 pl-5 first:mt-0 last:mb-0">
            {rows}
          </ol>
        ) : (
          <ul key={key} className="my-2 list-disc space-y-1 pl-5 first:mt-0 last:mb-0">
            {rows}
          </ul>
        ),
      );
      continue;
    }

    // Blank line → paragraph boundary.
    if (line.trim() === '') {
      flushParagraph(para, `${key}-p`, out);
      i += 1;
      continue;
    }

    para.push(line);
    i += 1;
  }
  flushParagraph(para, 'md-tail-p', out);

  return <>{out}</>;
}
