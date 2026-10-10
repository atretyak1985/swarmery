// Heading ids for the docs pane's in-page anchors — one module so the renderer
// (lib/markdown.tsx, which stamps the ids) and the "On this page" rail
// (pages/Docs.tsx, which scrolls to them) can never disagree.
//
// Mirrored in Go by slugify()/headingID() in internal/docsfs: the glossary
// drift test checks the English doc.anchor values against slugify, and the uk
// parity test checks that a translation still defines every anchor the English
// docs link to.

/** Heading id: lowercase, every run of characters other than Latin a–z, digits
 * and Cyrillic letters collapsed to one dash, ends trimmed. The Cyrillic range
 * is what gives a translated heading (`## Як плагін потрапляє…`) an id of its
 * own; without it every such heading slugified to "" — duplicate ids, a rail
 * whose entries scroll nowhere. An English heading slugifies exactly as before. */
export function slugify(text: string): string {
  return text
    .toLowerCase()
    .replace(/[^a-z0-9Ѐ-ӿ]+/g, '-')
    .replace(/^-+|-+$/g, '');
}

/** ` {#forecast}` at the very end of a heading line — an explicit id. */
const EXPLICIT_ID = /\s+\{#([A-Za-z0-9_-]+)\}\s*$/;

/** Split a heading's text into what the reader sees and its explicit id, if it
 * ends in ` {#some-id}` (the Pandoc / kramdown attribute form). A translated
 * heading keeps the English anchor the other docs and the glossary link to
 * this way: `## Прогноз {#forecast}`. */
export function splitHeadingId(raw: string): { text: string; id: string | null } {
  const m = EXPLICIT_ID.exec(raw);
  if (m === null) return { text: raw, id: null };
  return { text: raw.slice(0, m.index), id: m[1] ?? null };
}

/** The id a heading renders with: its explicit `{#id}` when it has one,
 * otherwise the slug of its visible text. */
export function headingId(raw: string): string {
  const { text, id } = splitHeadingId(raw);
  return id ?? slugify(text);
}
