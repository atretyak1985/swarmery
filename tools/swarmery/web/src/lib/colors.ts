// Stable per-project accent color (Redesign rails/rows): hashed by slug so a
// project keeps its color across screens and reloads.
//
// A tiny fixed palette collided constantly — once a workspace had more than a
// handful of projects, most of them landed on the same hue (see the pile of
// identical pinks in a busy project switcher). Instead we spread hues across
// the whole wheel, which gives hundreds of visually distinct colors.
//
// Reserved hue bands stay out so a project color never collides with a status
// meaning: no red (reads as "failing"), no green (reads as "active/working").
// Saturation and lightness are pinned so nothing lands on gray/white (reserved
// for neutral/done text) and every color reads on the dark background.
const ALLOWED_HUE_RANGES: ReadonlyArray<readonly [number, number]> = [
  [20, 90], // orange → amber → yellow
  [175, 345], // cyan → blue → indigo → violet → purple → magenta → pink
] as const;

const HUE_SPAN = ALLOWED_HUE_RANGES.reduce((sum, [lo, hi]) => sum + (hi - lo), 0);

// Avalanche hash (xmur3-style). The old `hash * 31 + char` mixed poorly, so
// unrelated slugs clustered into the same narrow hue band (most projects came
// out purple). This spreads slugs evenly across the wheel.
function hashSlug(slug: string): number {
  let hash = 1779033703 ^ slug.length;
  for (let i = 0; i < slug.length; i += 1) {
    hash = Math.imul(hash ^ slug.charCodeAt(i), 3432918353);
    hash = (hash << 13) | (hash >>> 19);
  }
  hash = Math.imul(hash ^ (hash >>> 16), 2246822507);
  hash = Math.imul(hash ^ (hash >>> 13), 3266489909);
  return (hash ^ (hash >>> 16)) >>> 0;
}

// Walk the reserved-safe ranges to turn a 0..HUE_SPAN offset into a real hue.
function hueFromOffset(offset: number): number {
  let remaining = offset % HUE_SPAN;
  for (const [lo, hi] of ALLOWED_HUE_RANGES) {
    const span = hi - lo;
    if (remaining < span) return lo + remaining;
    remaining -= span;
  }
  return 20; // unreachable: remaining < HUE_SPAN, but keeps the return total
}

function hslColor(hue: number, lightnessTier: number, saturationTier: number): string {
  const lightness = 62 + (((lightnessTier % 3) + 3) % 3) * 6; // 62 / 68 / 74
  const saturation = 52 + (((saturationTier % 3) + 3) % 3) * 12; // 52 / 64 / 76
  return `hsl(${hue}, ${saturation}%, ${lightness}%)`;
}

// Stand-alone color for a single slug (used where the full project list isn't
// available). Stable per slug, but two unrelated slugs can still land nearby.
export function projectColor(slug: string): string {
  const hash = hashSlug(slug);
  return hslColor(hueFromOffset(hash), Math.floor(hash / HUE_SPAN), Math.floor(hash / (HUE_SPAN * 3)));
}

// Guaranteed-distinct colors for a *known* set of slugs: hues are spread evenly
// across the wheel by index, so no two projects share (or nearly share) a color.
// Slugs are sorted first, so a project's color is stable for a given set and
// only shifts when the set's membership changes. Prefer this wherever the whole
// project list is in hand (e.g. the project switcher).
export function projectColorMap(slugs: readonly string[]): Map<string, string> {
  const ordered = [...new Set(slugs)].sort((a, b) => a.localeCompare(b));
  const count = Math.max(ordered.length, 1);
  const map = new Map<string, string>();
  ordered.forEach((slug, i) => {
    const hue = hueFromOffset(Math.round((i / count) * HUE_SPAN));
    // Rotate lightness/saturation tiers on a different period than the hue step
    // so neighbouring entries separate on brightness as well as hue.
    map.set(slug, hslColor(hue, i, i * 2 + 1));
  });
  return map;
}
