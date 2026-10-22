// safeHref — the one gate for a link target that comes from data (an API field,
// a stored note, a URL another process reported) rather than from this code.
//
// A data-driven `href` is a navigation the user triggers by clicking, and a
// `javascript:` or `data:` URL in it runs or renders content on the dashboard's
// own origin. So every such href goes through here: an absolute URL survives
// only when its scheme is on an explicit allow-list (http/https by default), and
// anything else — a script scheme, a relative path, a string that does not
// parse — comes back null, which the caller renders as no link at all.

/** Schemes a data-driven link may use when the caller names none. */
const WEB_SCHEMES: readonly string[] = ['http:', 'https:'];

/**
 * The href itself when it is an absolute URL whose scheme is in `schemes`
 * (lowercase with the trailing colon, e.g. `'https:'`); null otherwise.
 *
 * Callers that legitimately link to an app deep link name that scheme
 * explicitly — `safeHref(focusUrl, ['warp:'])` — and inherit nothing else.
 */
export function safeHref(
  raw: string | null | undefined,
  schemes: readonly string[] = WEB_SCHEMES,
): string | null {
  if (raw === null || raw === undefined) return null;
  const value = raw.trim();
  if (value === '') return null;
  let url: URL;
  try {
    // No base: a relative or scheme-less string throws instead of resolving
    // against the dashboard's own origin.
    url = new URL(value);
  } catch {
    return null;
  }
  return schemes.includes(url.protocol) ? value : null;
}
