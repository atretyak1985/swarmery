# Verification — measured, in bounded passes

One pass = static check + four browser runs. Fix everything the pass found in one batch, then run
one more full pass. Two passes is the normal budget; a third means the plan, not the page, is wrong.

## 1. Static check (no browser)

```bash
node "${CLAUDE_PLUGIN_ROOT}/skills/visual-explainer/scripts/check.mjs" <page.html>
```

Exit 0 = no errors. It reports: duplicate ids, dangling `url(#…)` / `href="#…"`, inline-script
syntax, JSON islands that do not parse or contain `</script`, `data-d` hotspots without content,
`data-t` terms missing from the glossary, theme blocks, literal colors, `hidden` vs `display`,
monospace ligatures, diagrams without `role="img"` + `aria-label`, and sections without visuals.
Fix every ERROR; read every WARN and fix it unless it is deliberate.

## 2. Browser matrix

Serve the page over HTTP (many browser tools refuse `file://`) with the skill's server:

```bash
node "${CLAUDE_PLUGIN_ROOT}/skills/visual-explainer/scripts/serve.mjs" <page.html>
# → {"url":"http://127.0.0.1:53817/page.html","pid":41207,"idle":300,"ttl":1800}
```

It binds `127.0.0.1` only, takes a free port, and answers only that one page — every other path is
404, so the directory around the page (`.env`, `.git/`, …) is never exposed, even on localhost. It
sends `Cache-Control: no-store`, so a reload always shows the current file. It returns at once and
stops itself after 5 minutes without a request or 30 minutes in total; stop it as soon as the
browser runs are done with `node …/serve.mjs --stop <pid>`. It needs only `node` — never fall back
to `python3 -m http.server`, which listens on every interface and serves the whole directory.

Run these four, in this order, and on each run evaluate `scripts/probe.js` inside the page (pass
the file's whole content as the evaluate function):

| Run | Viewport | Color scheme | Look at |
|---|---|---|---|
| 1 | 1440 × 900 | light | first screen, every figure (element screenshots), one drawer open |
| 2 | 1440 × 900 | dark | the two densest figures, the drawer |
| 3 | 400 × 860 | light | first screen, the widest figure, a table |
| 4 | 400 × 860 | dark | first screen |

`probe.js` returns `horizontalOverflow` (elements that make the page scroll sideways),
`svgLabelIssues` (labels outside the viewBox or overlapping each other), `clippedText`,
`deadHotspots`, `undefinedTerms`, and `ok`. A label "overlap" on an animated element can be
intended — confirm on the screenshot before changing it.

Browser tools drive one shared browser: issue resize, emulate, navigate, evaluate and screenshot
calls one at a time, never in parallel — parallel calls collide and the run stalls.

Interaction spot-check on run 1: open one drawer from a diagram and one from a table row, hover
one term, step the stepper to the last step, switch every scenario, press Esc. Then read the
console: zero errors except a missing favicon.

### Tools that do this correctly

| Tool | Set viewport | Set color scheme |
|---|---|---|
| Playwright MCP | `browser_resize` | `browser_emulate_media {colorScheme}` |
| Chrome extension tools | `resize_window` | not available — check dark via the page's own toggle if it has one |
| `core:browser-verification` | as above | as above |

Do **not** trust `chrome --headless --window-size=400,…` screenshots: below ~500 px the window
renders wider and crops, so overflow looks like clipping and real overflow is hidden; and the CLI
flags do not reliably switch `prefers-color-scheme` (the OS appearance wins). If no browser tool is
available, say so and report the browser matrix as unverified — the static check alone does not
cover layout.

Stop the server when done: `node "${CLAUDE_PLUGIN_ROOT}/skills/visual-explainer/scripts/serve.mjs" --stop <pid>`.

## 3. Defects these checks keep finding

| Symptom | Cause | Fix |
|---|---|---|
| Interactive block does nothing; another one misbehaves | the same `id` on a section and an SVG | rename the SVG id |
| `a->b` or `x-<y` in code shows an arrow glyph | a monospace web font with ligatures | the shell turns ligatures off; keep that rule |
| Page scrolls sideways on a phone | long `code` or URL in running text, a `min-width` on a card | `overflow-wrap:anywhere` on code; put wide things in `.tbl-wrap` / `.fig` |
| Label runs off the right edge | label placed at 100 % without `translateX(-100%)`, or estimated width too small | right-align end labels; re-estimate with `chars × size × 0.6` |
| Arrowhead missing | line drawn before the box it points into | move the line after the box in the SVG source |
| Filtered items do not hide | `hidden` attribute overridden by `display:flex` | the shell's `[hidden]{display:none!important}` — do not remove it |
| Fix "did not work" | an old tab, or a server other than `serve.mjs` caching | reload; `serve.mjs` sends `no-store` |
| A web font falls back for Cyrillic (or another script) | the face has no subset for that script | system fonts by default; a web face must cover the page language |
| Light theme fine, dark unreadable | a literal color in a component rule | move it to a token with both values |

## 4. Publishing

The local `.html` file is a full document (doctype, `<html lang>`, charset). A publishing tool that
wraps pages in its own skeleton needs the body-level form:

```bash
node "${CLAUDE_PLUGIN_ROOT}/skills/visual-explainer/scripts/to-artifact.mjs" page.html page.artifact.html
```

Publish only when the page is meant for other people and the user agreed or asked. If the page
adds web fonts, say that every viewer's browser will contact the font host; published pages
may be private by default — tell the user who can open the link. Never publish a page that carries
secrets from the source (tokens, internal hostnames the user did not intend to share).

## 5. Report back

Say what you checked and how: the static check result, the four browser runs with `ok` per run,
what you fixed between passes, and anything you could not verify (no browser tool, no dark-mode
switch). Do not call the page verified on the strength of a pass that ran before your last edit.
