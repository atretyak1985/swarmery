---
name: browser-verification
description: "Use this skill when an agent needs to verify UI behavior in a live browser via Playwright MCP tools (browser_navigate, browser_snapshot, screenshots, console/network capture) against localdev or the project's staging environment (project.json -> cloud.envAlias). Covers the target-confirmation step, the observe/interact loop, and safety guardrails. NOT for full mission E2E flows (use mission-creation) and never against production."
version: "1.0.0"
owner: "agentry-core"
color: cyan
---

# Purpose

Canonical procedure for verifying UI behavior in a real browser through the Playwright MCP tools (`mcp__plugin_playwright_playwright__browser_*`). Extracted 2026-06-10 from duplicated sections in @tech-lead, @react-specialist, @verification-agent, and @quality-checker — those agents now reference this skill and keep only their role-specific invariants.

# Step 0 — confirm a live target

The main app's dev server (project.json -> `mainApp`) typically runs at `http://localhost:3000` (`npm run dev`); a locally deployed cluster stack has its own ingress hostname (e.g., `https://d16.local`); post-deploy checks use the staging environment's URL (project.json -> `cloud.envAlias`). Never assume a URL is up — `browser_navigate` first, then verify the response.

# Core loop (interactive verification)

1. `browser_navigate` to the page under test.
2. `browser_snapshot` — capture the accessibility tree and act on the element refs it returns (more reliable than guessing CSS selectors; prefer `data-testid`).
3. Drive the flow as needed: `browser_click`, `browser_type`, `browser_fill_form`, `browser_select_option`, `browser_press_key`, `browser_hover`.
4. Capture evidence: `browser_take_screenshot` (visual state), `browser_console_messages` (runtime/hydration errors the build won't catch), `browser_network_requests` (failed/slow calls). Use `browser_resize` to check responsive breakpoints.

# Observation-only variant (report-only agents)

Read-only verifiers (@verification-agent, @quality-checker) restrict themselves to navigate + snapshot + screenshot + console/network capture, with at most the minimal `browser_click`/`browser_type` required to reach the state under test. Browser findings are supplementary, warning-level signal — they never flip a deterministic PASS/FAIL verdict.

# Guardrails (apply to every agent)

- Snapshot before acting — never act on assumed DOM state.
- Use throwaway/seed data; never mutate real records.
- `browser_run_code_unsafe` / `browser_evaluate` run arbitrary JS in the page — authorized local/staging targets only, **never a production origin**.
- Always `browser_close` when finished to release the browser session.
- A browser check confirms behavior; it does not replace the automated test suite or the Phase 5 quality gate.

# Mission flows

For creating/starting/verifying a mission through the UI, do NOT improvise with the core loop — load the `mission-creation` skill (canonical wizard + two-step start FSM + cleanup). Default target localdev only.
