// Captures the three MVP screens in mock mode against a running dev server.
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

async function shot(page, path, name, opts = {}) {
  await page.goto(base + path);
  await settle(page);
  await page.screenshot({ path: join(outDir, name), fullPage: opts.fullPage ?? false });
  console.log(`✓ ${name}`);
}

// Mobile-first: the owner's viewport (390×844).
const mobile = await browser.newPage({
  viewport: { width: 390, height: 844 },
  deviceScaleFactor: 2,
});
await shot(mobile, '/', 'overview.png');
await shot(mobile, '/sessions', 'sessions.png');
// Session 1 is the subagent fixture — full page so the nested track is visible.
await shot(mobile, '/sessions/1', 'session-detail-timeline.png', { fullPage: true });
await mobile.getByRole('tab', { name: /Diffs/ }).click();
await mobile.waitForTimeout(300);
await mobile.screenshot({ path: join(outDir, 'session-detail-diffs.png'), fullPage: true });
console.log('✓ session-detail-diffs.png');
await mobile.close();

// Desktop (≥900px): sidebar navigation.
const desktop = await browser.newPage({ viewport: { width: 1280, height: 800 } });
await shot(desktop, '/', 'overview-desktop.png');
await desktop.close();

await browser.close();
