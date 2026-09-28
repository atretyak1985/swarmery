// Grouping rule for the /docs rail: illustrated Guides first, then the
// Reference docs, then the wire Formats and Protocols. Pure, and deliberately
// separate from Docs.tsx so it can be unit-tested without mounting the page.
//
// The split is CLIENT-SIDE, on the file name, because /api/docs response
// shapes are frozen by the parity contract ({slug,title,file} — internal/api/
// docs.go). No group field is added server-side; the daemon only pins the
// order (docOrder), and `guide-` is the prefix the Makefile's flattening
// preserves.

/** The file-name prefix that marks a doc as an illustrated guide. */
export const GUIDE_PREFIX = 'guide-';

export type DocGroupName = 'Guides' | 'Reference' | 'Formats' | 'Protocols';

/** Section order in the rail. A group with no docs at all is dropped before
 * render — most doc sets leave Formats and Protocols empty. */
export const GROUP_ORDER: readonly DocGroupName[] = ['Guides', 'Reference', 'Formats', 'Protocols'];

/** The rail group a doc belongs to, from its file name. */
export function groupOf(file: string): DocGroupName {
  const f = file.toLowerCase();
  if (f.startsWith(GUIDE_PREFIX)) return 'Guides';
  if (f.includes('protocol')) return 'Protocols';
  if (f.includes('format') || f.includes('config')) return 'Formats';
  return 'Reference';
}
