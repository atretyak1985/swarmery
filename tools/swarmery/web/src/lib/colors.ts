// Stable per-project accent dots (Redesign rails/rows): a small palette
// hashed by slug so a project keeps its color across screens and reloads.

// No red in the palette — a red dot would read as "failing project".
// Canvas project hues (Canvas.dc.html PC map).
const PROJECT_PALETTE = [
  '#e8a13a', // amber
  '#6fb4f0', // blue
  '#58c08a', // green
  '#c58be0', // purple (Canvas fourth-project tone)
] as const;

export function projectColor(slug: string): string {
  let hash = 0;
  for (let i = 0; i < slug.length; i += 1) {
    hash = (hash * 31 + slug.charCodeAt(i)) >>> 0;
  }
  return PROJECT_PALETTE[hash % PROJECT_PALETTE.length] ?? '#8b8f99';
}
