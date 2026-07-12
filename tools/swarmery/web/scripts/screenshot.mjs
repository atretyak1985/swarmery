// Captures the MVP screens in mock mode against a running dev server.
//
// Usage:
//   VITE_MOCK=1 npm run dev          # terminal 1
//   node scripts/screenshot.mjs      # terminal 2 (BASE_URL to override)
//
// Uses the system Chrome via playwright-core (no browser download).

import { chromium } from 'playwright-core';
import { mkdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const base = process.env.BASE_URL ?? 'http://localhost:5173';
const outDir = join(dirname(fileURLToPath(import.meta.url)), '..', 'screenshots');
mkdirSync(outDir, { recursive: true });

const browser = await chromium.launch({ channel: 'chrome', headless: true });

async function settle(page) {
  await page.waitForLoadState('networkidle');
  await page.evaluate(() => document.fonts.ready);
  await page.waitForTimeout(600); // mock latency + transitions
}

async function assertNoHorizontalScroll(page, path) {
  // The app frame scrolls inside <main>, so check both the document and main.
  const { scrollWidth, clientWidth, mainScrollWidth, mainClientWidth } = await page.evaluate(
    () => {
      const main = document.querySelector('main');
      return {
        scrollWidth: document.documentElement.scrollWidth,
        clientWidth: document.documentElement.clientWidth,
        mainScrollWidth: main?.scrollWidth ?? 0,
        mainClientWidth: main?.clientWidth ?? 0,
      };
    },
  );
  if (scrollWidth > clientWidth) {
    throw new Error(`horizontal overflow on ${path}: ${scrollWidth} > ${clientWidth}`);
  }
  if (mainScrollWidth > mainClientWidth) {
    throw new Error(`horizontal overflow in <main> on ${path}: ${mainScrollWidth} > ${mainClientWidth}`);
  }
  console.log(`✓ no horizontal scroll on ${path} (${scrollWidth} <= ${clientWidth})`);
}

async function shot(page, path, name, opts = {}) {
  await page.goto(base + path);
  await settle(page);
  if (opts.waitMs) await page.waitForTimeout(opts.waitMs);
  await page.screenshot({ path: join(outDir, name), fullPage: opts.fullPage ?? false });
  await assertNoHorizontalScroll(page, path);
  console.log(`✓ ${name}`);
}

// The approvals mock scenario injects a permission_requested ~3 s after load —
// wait it out so the WS-pushed pending card is in frame.
const APPROVALS_WAIT_MS = 3200;

// Scrolls the session-detail tab panel (its own scroller — the header stays pinned).
async function scrollTabPanel(page, px) {
  await page.locator('[role="tabpanel"]').evaluate((el, y) => {
    el.scrollTop = y;
  }, px);
  await page.waitForTimeout(200);
}

// Mobile-first: the owner's viewport (390×844).
const mobile = await browser.newPage({
  viewport: { width: 390, height: 844 },
  deviceScaleFactor: 2,
});
await shot(mobile, '/', 'overview.png');
await shot(mobile, '/docs', 'docs-mobile.png');
await shot(mobile, '/sessions', 'sessions.png');
// Approvals (phase 2): pending cards + history, incl. the WS-injected request.
await shot(mobile, '/approvals', 'approvals.png', { waitMs: APPROVALS_WAIT_MS });
// Session 1 is the subagent fixture. Chat is the default tab now.
await shot(mobile, '/sessions/1', 'session-detail-chat.png');
// Timeline via ?tab= deep-link; the panel scrolls under the pinned header.
await shot(mobile, '/sessions/1?tab=timeline', 'session-detail-chips.png');
await scrollTabPanel(mobile, 400);
await mobile.screenshot({ path: join(outDir, 'session-detail-timeline.png') });
console.log('✓ session-detail-timeline.png');
await shot(mobile, '/sessions/1?tab=diffs', 'session-detail-diffs.png');
await mobile.close();

// Desktop (≥1280px): full-width header bar, sidebar below, right rails.
const desktop = await browser.newPage({ viewport: { width: 1440, height: 900 } });
await shot(desktop, '/', 'overview-desktop.png');
// Sessions table (≥900px): dropdown + status-count chips + aligned columns.
await shot(desktop, '/sessions', 'sessions-desktop.png');
await shot(desktop, '/approvals', 'approvals-desktop.png', { waitMs: APPROVALS_WAIT_MS });
// Detail with the timeline scrolled — header block and rail stay pinned.
await desktop.goto(`${base}/sessions/1?tab=timeline`);
await settle(desktop);
await scrollTabPanel(desktop, 600);
await desktop.screenshot({ path: join(outDir, 'session-detail-desktop.png') });
await assertNoHorizontalScroll(desktop, '/sessions/1?tab=timeline');
console.log('✓ session-detail-desktop.png');
await shot(desktop, '/docs/neutrality', 'docs.png');
await desktop.close();

await browser.close();
