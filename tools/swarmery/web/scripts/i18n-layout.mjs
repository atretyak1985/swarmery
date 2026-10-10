// Layout gate for the Ukrainian locale (plan dashboard-uk-locale, phase 4):
// Ukrainian runs ~20-40 % longer than English, so every label that fits in
// English is a candidate to overflow its chip, button or table header in uk.
// This walks the twelve places of the sidebar in mock mode with the locale set
// to `uk`, at the two desktop widths the dashboard is used at, and fails on any
// horizontal overflow.
//
// Usage:
//   node scripts/i18n-layout.mjs                     # starts `vite` in mock mode itself
//   node scripts/i18n-layout.mjs --url http://localhost:5173   # an already running dev server (VITE_MOCK=1)
//   node scripts/i18n-layout.mjs --out <dir>         # also save one PNG per route × width
//   node scripts/i18n-layout.mjs --locale en         # same pass in English (a control run)
//   I18N_LAYOUT_LIST_ELLIPSIS=1 node scripts/i18n-layout.mjs   # also list the ellipsized labels
//
// What counts as an overflow:
//   - the document or <main> scrolls horizontally (scrollWidth > clientWidth);
//   - any visible label box (MEASURED below) whose content is wider than its
//     box (scrollWidth > clientWidth + 1). An element that truncates on
//     purpose — its own computed `text-overflow: ellipsis` with a clipped
//     overflow — is the fix, not the bug, and does not count.
//     The app has no `.chip` class: its chips are ad-hoc `rounded-full`
//     `whitespace-nowrap` spans, so those class fragments are measured too,
//     along with tabs, links and selects — otherwise the chip leg would pass
//     vacuously;
//   - a label box that fits itself but sticks out of its container (a row of
//     chips too long for a narrow card): the nearest ancestor that clips or
//     scrolls horizontally, or draws a border on both sides, or <main>;
//   - two label boxes drawn on top of each other (a row too long for its
//     column whose shrink-0 children run under the next one). Overlays and
//     sticky headers are exempt.
// It also asserts every page really rendered Ukrainian — <html lang="uk"> and
// Cyrillic in both the sidebar and <main> — so the run cannot pass vacuously
// in English. (Not the page headings: the place names Today … Settings stay
// English by plan decision D6, so a Settings <h1> reads "Settings" in uk too.)
//
// Exit code: 0 when every route × width is clean, 1 otherwise.
// Uses the system Chrome via playwright-core (no browser download), like
// scripts/screenshot.mjs.

import { spawn } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from 'playwright-core';

const WEB_DIR = join(dirname(fileURLToPath(import.meta.url)), '..');

// The twelve places of src/lib/nav.ts (Docs aside, plus one session and one
// plan detail). Plans and Knowledge are project-only places, so they are
// visited under the mock project that owns the mock epics (`swarmery`).
const ROUTES = [
  { name: 'Today', path: '/' },
  { name: 'Inbox', path: '/inbox' },
  { name: 'Needs you', path: '/needs-you' },
  { name: 'Sessions', path: '/sessions' },
  { name: 'Session detail', path: '/sessions/1' },
  { name: 'Plans', path: '/p/swarmery/plans' },
  { name: 'Plan detail', path: '/p/swarmery/plans/2026-07-24-fusion-orchestration' },
  { name: 'Health', path: '/health' },
  { name: 'Learning', path: '/learning' },
  { name: 'Knowledge', path: '/p/swarmery/knowledge' },
  { name: 'System', path: '/system' },
  { name: 'Settings', path: '/settings' },
];
const WIDTHS = [1024, 1440];
const HEIGHT = 900;

function parseArgs(argv) {
  const args = { url: null, out: null, locale: 'uk', port: 5181 };
  for (let i = 0; i < argv.length; i++) {
    const flag = argv[i];
    const value = argv[i + 1];
    if (flag === '--url') args.url = value;
    else if (flag === '--out') args.out = value;
    else if (flag === '--locale') args.locale = value;
    else if (flag === '--port') args.port = Number(value);
    else throw new Error(`unknown argument: ${flag}`);
    i++;
  }
  return args;
}

async function waitForServer(url, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const res = await fetch(url);
      if (res.ok) return;
    } catch {
      // not up yet
    }
    await new Promise((r) => setTimeout(r, 300));
  }
  throw new Error(`dev server did not answer at ${url} within ${timeoutMs} ms`);
}

/** `npm run dev` in mock mode (it compiles the catalogs first); returns a stop(). */
async function startDevServer(port) {
  const child = spawn('npm', ['run', 'dev', '--', '--port', String(port), '--strictPort'], {
    cwd: WEB_DIR,
    env: { ...process.env, VITE_MOCK: '1' },
    stdio: ['ignore', 'pipe', 'pipe'],
    detached: true,
  });
  let log = '';
  child.stdout.on('data', (d) => (log += d));
  child.stderr.on('data', (d) => (log += d));
  const stop = () => {
    try {
      process.kill(-child.pid, 'SIGTERM');
    } catch {
      // already gone
    }
  };
  try {
    await waitForServer(`http://localhost:${port}/`, 60_000);
  } catch (err) {
    stop();
    throw new Error(`${err.message}\n--- dev server log ---\n${log}`);
  }
  return stop;
}

async function settle(page) {
  await page.waitForLoadState('networkidle', { timeout: 15_000 }).catch(() => {});
  await page.evaluate(() => document.fonts.ready);
  await page.waitForTimeout(700); // mock latency + transitions
}

/** Runs in the page: overflow measurements plus the "is it Ukrainian" probe. */
function measure() {
  const describe = (el) => {
    // A <select>'s innerText is every option; what is drawn is the chosen one.
    const shown = el.tagName === 'SELECT' ? (el.selectedOptions[0]?.text ?? '') : el.innerText;
    const text = (shown || el.getAttribute('aria-label') || el.getAttribute('title') || '')
      .replace(/\s+/g, ' ')
      .trim()
      .slice(0, 60);
    return `${el.tagName.toLowerCase()} "${text}" (${el.scrollWidth}>${el.clientWidth})`;
  };
  const doc = document.documentElement;
  const main = document.querySelector('main');
  const offenders = [];
  const truncated = [];
  if (document.getElementById('i18n-layout-measure') === null) {
    const style = document.createElement('style');
    style.id = 'i18n-layout-measure';
    style.textContent =
      '[data-i18n-layout-measure]::before,[data-i18n-layout-measure]::after{display:none!important}';
    document.head.appendChild(style);
  }
  if (doc.scrollWidth > doc.clientWidth) offenders.push(`document (${doc.scrollWidth}>${doc.clientWidth})`);
  if (main !== null && main.scrollWidth > main.clientWidth) {
    offenders.push(`main (${main.scrollWidth}>${main.clientWidth})`);
  }
  const MEASURED = [
    '.chip',
    '[data-chip]',
    'th',
    'button',
    'select',
    '[role="tab"]',
    'a',
    '[class*="rounded-full"]',
    '[class*="whitespace-nowrap"]',
    '[data-tip]',
  ].join(', ');

  // Own overflow: the label is wider than its own box.
  const ownOverflow = (el, style) => {
    if (el.clientWidth === 0 || el.scrollWidth <= el.clientWidth + 1) return null;
    // Deliberate truncation: the element clips and draws an ellipsis itself.
    // Not a failure, but listed: a label ellipsized in uk and whole in en is
    // something the proofread should look at.
    if (style.textOverflow === 'ellipsis' && style.overflowX !== 'visible') return 'ellipsis';
    // An absolutely positioned ::before/::after (Explain's `before:-inset-1.5`
    // hit-target halo) adds scrollable overflow without drawing any content
    // outside the box. It is out of flow, so hiding it for one measurement
    // changes no layout: if the overflow goes away with it, it was the halo.
    const absPseudo = ['::before', '::after'].some((p) => {
      const pos = getComputedStyle(el, p).position;
      return pos === 'absolute' || pos === 'fixed';
    });
    if (absPseudo) {
      el.setAttribute('data-i18n-layout-measure', '');
      const overflows = el.scrollWidth > el.clientWidth + 1;
      el.removeAttribute('data-i18n-layout-measure');
      if (!overflows) return null;
    }
    return 'overflow';
  };

  // Spill: the label fits its own box, but the box sticks out of the card or
  // clipping container it sits in — a row of chips too long for a narrow
  // column. The container is the nearest ancestor that clips or scrolls
  // horizontally, or draws a border on both sides (a card). A scroller may
  // hold content past its right edge (that is what scrolling is for), never
  // past its left one. Overlays (absolute/fixed on the way up) are skipped.
  const spillOf = (el, rect) => {
    for (let a = el.parentElement; a !== null && a !== document.body; a = a.parentElement) {
      const cs = getComputedStyle(a);
      if (cs.position === 'absolute' || cs.position === 'fixed') return null;
      const scrolls = cs.overflowX === 'auto' || cs.overflowX === 'scroll';
      const clips = cs.overflowX === 'hidden' || cs.overflowX === 'clip';
      const card =
        parseFloat(cs.borderLeftWidth) > 0 &&
        parseFloat(cs.borderRightWidth) > 0 &&
        cs.display !== 'inline' &&
        cs.display !== 'contents';
      if (!scrolls && !clips && !card && a !== main) continue;
      const box = a.getBoundingClientRect();
      const left = box.left + parseFloat(cs.borderLeftWidth);
      const right = box.right - parseFloat(cs.borderRightWidth);
      const kind = scrolls ? 'scroller' : clips ? 'clipping box' : a === main ? '<main>' : 'card';
      const where = `${kind} <${a.tagName.toLowerCase()}${a.classList.length > 0 ? `.${a.classList[0]}` : ''}>`;
      if (scrolls) return rect.left + a.scrollLeft < left - 1 ? `${Math.round(left - rect.left)}px left of its ${where}` : null;
      if (rect.left < left - 1) return `${Math.round(left - rect.left)}px left of its ${where}`;
      if (rect.right > right + 1) return `${Math.round(rect.right - right)}px right of its ${where}`;
      return null;
    }
    return null;
  };

  // In an overlay (popover, tooltip, a badge pinned to a corner) or a sticky
  // header: boxes there may legitimately sit on top of others.
  const layered = (el) => {
    for (let a = el; a !== null && a !== document.body; a = a.parentElement) {
      const pos = getComputedStyle(a).position;
      if (pos === 'absolute' || pos === 'fixed' || pos === 'sticky') return true;
    }
    return false;
  };

  // The nearest ancestor that clips or scrolls: a box scrolled out of its pane
  // can sit "under" a box of another pane without anyone seeing it, so the
  // collision pass compares boxes of the same pane only.
  const paneOf = (el) => {
    for (let a = el.parentElement; a !== null; a = a.parentElement) {
      const cs = getComputedStyle(a);
      if (cs.overflowX !== 'visible' || cs.overflowY !== 'visible') return a;
    }
    return document.documentElement;
  };

  const boxes = []; // in-flow, non-inline label boxes, for the collision pass
  for (const el of document.querySelectorAll(MEASURED)) {
    const style = getComputedStyle(el);
    if (style.display === 'none' || style.visibility === 'hidden') continue;
    if (style.position === 'absolute' || style.position === 'fixed') continue;
    const rect = el.getBoundingClientRect();
    // Unrendered or visually-hidden (sr-only, 1px) boxes.
    if (rect.width <= 1 || rect.height <= 1) continue;
    if (el.closest('[aria-hidden="true"], .sr-only') !== null) continue;
    const own = ownOverflow(el, style);
    if (own === 'ellipsis') truncated.push(describe(el));
    else if (own === 'overflow') offenders.push(describe(el));
    else {
      const spill = spillOf(el, rect);
      if (spill !== null) offenders.push(`${describe(el)} spills ${spill}`);
    }
    // An inline box that wraps has a bounding rect wider than any of its lines.
    if (style.display !== 'inline' && !layered(el)) boxes.push({ el, rect, pane: paneOf(el) });
  }

  // Collision: two label boxes drawn on top of each other — a row that fits
  // nowhere and lets its shrink-0 children run under the next column. Neither
  // box sticks out of a card, so neither check above sees it.
  for (let i = 0; i < boxes.length; i++) {
    const a = boxes[i];
    for (let j = i + 1; j < boxes.length; j++) {
      const b = boxes[j];
      if (a.pane !== b.pane || a.el.contains(b.el) || b.el.contains(a.el)) continue;
      const w = Math.min(a.rect.right, b.rect.right) - Math.max(a.rect.left, b.rect.left);
      const h = Math.min(a.rect.bottom, b.rect.bottom) - Math.max(a.rect.top, b.rect.top);
      if (w > 1 && h > 1) offenders.push(`${describe(a.el)} overlaps ${describe(b.el)}`);
    }
  }
  const sidebar = document.querySelector('aside, nav');
  const cyrillic = (el) => el !== null && /[\u0400-\u04FF]/.test(el.innerText);
  return {
    offenders,
    truncated,
    lang: doc.lang,
    sidebarCyrillic: cyrillic(sidebar),
    mainCyrillic: cyrillic(main),
  };
}

const args = parseArgs(process.argv.slice(2));
if (args.out !== null) mkdirSync(args.out, { recursive: true });

const stopServer = args.url === null ? await startDevServer(args.port) : () => {};
const base = (args.url ?? `http://localhost:${args.port}`).replace(/\/$/, '');

const results = []; // { route, width, offenders, truncated }
const languageFailures = [];
const browser = await chromium.launch({ channel: 'chrome', headless: true });
try {
  for (const width of WIDTHS) {
    const context = await browser.newContext({ viewport: { width, height: HEIGHT } });
    // Before the first navigation: the app reads the locale at boot (src/i18n/locale.ts).
    await context.addInitScript((locale) => {
      try {
        localStorage.setItem('swarmery.locale', locale);
      } catch {
        // storage blocked: the language probe below will fail the run
      }
    }, args.locale);
    const page = await context.newPage();
    for (const route of ROUTES) {
      await page.goto(base + route.path);
      await settle(page);
      const m = await page.evaluate(measure);
      results.push({ route, width, offenders: m.offenders, truncated: m.truncated });
      if (args.locale === 'uk') {
        if (m.lang !== 'uk') languageFailures.push(`${route.path} @${width}: <html lang="${m.lang}">, want uk`);
        if (!m.sidebarCyrillic) languageFailures.push(`${route.path} @${width}: no Cyrillic in the sidebar`);
        if (!m.mainCyrillic) languageFailures.push(`${route.path} @${width}: no Cyrillic in <main>`);
      }
      if (args.out !== null) {
        const slug = route.name.toLowerCase().replace(/[^a-z0-9]+/g, '-');
        await page.screenshot({ path: join(args.out, `${args.locale}-${slug}-${width}.png`) });
      }
    }
    await context.close();
  }
} finally {
  await browser.close();
  stopServer();
}

// Report: route × width overflow counts (with the informational count of
// deliberately ellipsized labels after the slash), then every offending element.
const pad = (s, n) => String(s).padEnd(n);
const nameWidth = Math.max(...ROUTES.map((r) => `${r.name} (${r.path})`.length));
console.log(`\ni18n layout — locale ${args.locale}, ${base}\n`);
const CELL = 'overflow/ellipsis'.length;
console.log(
  `| ${pad('Route', nameWidth)} | ${WIDTHS.map((w) => pad(`${w}px overflow/ellipsis`, CELL + 7)).join(' | ')} |`,
);
console.log(`|${'-'.repeat(nameWidth + 2)}|${WIDTHS.map(() => '-'.repeat(CELL + 9)).join('|')}|`);
for (const route of ROUTES) {
  const cells = WIDTHS.map((w) => {
    const r = results.find((x) => x.route === route && x.width === w);
    return pad(`${r.offenders.length} / ${r.truncated.length}`, CELL + 7);
  });
  console.log(`| ${pad(`${route.name} (${route.path})`, nameWidth)} | ${cells.join(' | ')} |`);
}
const total = results.reduce((n, r) => n + r.offenders.length, 0);
console.log(`\nTotal overflows: ${total}`);
for (const r of results) {
  if (r.offenders.length === 0) continue;
  console.log(`\n${r.route.path} @ ${r.width}px:`);
  for (const o of r.offenders) console.log(`  - ${o}`);
}
if (process.env.I18N_LAYOUT_LIST_ELLIPSIS === '1') {
  for (const r of results) {
    if (r.truncated.length === 0) continue;
    console.log(`\n${r.route.path} @ ${r.width}px (ellipsized, informational):`);
    for (const o of r.truncated) console.log(`  · ${o}`);
  }
}
if (languageFailures.length > 0) {
  console.log('\nLanguage check FAILED (the page is not Ukrainian):');
  for (const f of languageFailures) console.log(`  - ${f}`);
} else if (args.locale === 'uk') {
  console.log('Language check: <html lang="uk"> and Cyrillic in the sidebar and <main> on every route.');
}
if (args.out !== null) console.log(`Screenshots: ${args.out}`);
process.exit(total > 0 || languageFailures.length > 0 ? 1 : 0);
