---
name: summary-templates
version: "1.0.0"
owner: "agentry-core"
description: "Use this skill when a task involves summarizing completed work, writing a feature summary, documenting what changed, or creating a completion report for a task, feature, bug fix, or refactoring. Don't use it for writing documentation, changelogs, or postmortems."
disable-model-invocation: true
color: teal
---

# Purpose

Provide standardized templates for creating structured summaries of technical work. Covers four work types: tasks, features, bug fixes, and refactorings. Templates ensure summaries include quantified metrics, role-specific instructions, and actionable next steps.

# When to use this skill

- User asks to "summarize this task," "write a feature summary," or "document what changed"
- User asks to "create a completion report" for a task, feature, bug fix, or refactoring
- An agent completes implementation work and needs to produce a structured summary
- User asks "what template should I use for this summary?"

# When NOT to use this skill

- **Writing documentation for an API or module** -- that is technical documentation, not a work summary
- **Creating a changelog entry** -- changelogs have a different format and audience
- **Writing release notes** -- release notes summarize a version, not a single work item
- **Writing a postmortem for an outage** -- use the troubleshooting skill's postmortem template instead
- **Writing code, tests, or configuration** -- this skill produces markdown/HTML summary documents only
- **Generating metrics from code** -- this skill formats metrics you provide; it does not measure them

# Required environment (Runtime: .claude/skills/summary-templates/SKILL.md)

No special tooling needed. If HTML output is desired, the skill produces self-contained HTML inline.

# Inputs

| Input | Required | Description |
|-------|----------|-------------|
| Work type | Yes | One of: task, feature, bug-fix, refactoring |
| Work details | Yes | What was done, files changed, metrics available |
| Output format | No | `markdown` (default) or `html` (for summaries with >3 sections or shared outside terminal) |

If the work type is ambiguous (e.g., a refactor that also fixes a bug), reason about it in Step 1 before selecting a template.

# Outputs

**Format:** A structured summary document in markdown or HTML.

**Length budget:** Max 200 lines for markdown output. Max 300 lines for HTML output. If sections exceed the budget, prioritize: summary, metrics, next steps, known issues. Omit passing checks unless the user requests a full compliance report.

All four templates share these common elements:

- Status header
- Items created, modified, deleted
- Quantified metrics (files, lines, duration, coverage)
- Role-specific "How to Use" instructions (developers, QA, PM)
- Actionable next steps with owners and timeframes
- Known issues and limitations

## Template structures

### Task Summary
Status -> Removed items -> Created items -> Modified items -> Details -> Metrics -> How to Use -> Key Changes -> Next Steps -> Known Issues -> Recommendations -> Footer

### Feature Summary
Overview -> User Stories -> Architecture (backend, frontend, integration) -> Files Created/Modified -> Metrics -> How to Use -> Key Features -> Security -> Testing -> Deployment Checklist -> Next Steps -> Known Issues -> Future Improvements

### Bug Fix Summary
Overview (ID, severity, environment) -> Impact -> Root Cause Analysis -> The Fix -> Files Modified -> Testing (reproduction, verification, regression) -> Metrics -> How to Verify -> Prevention Measures -> Related Issues -> Deployment -> Known Limitations -> Lessons Learned

### Refactoring Summary
Overview -> Problems Solved (before/after) -> Architecture Changes -> Files Created/Modified/Deleted -> Metrics (quality, performance, maintainability) -> How to Verify -> Improvements -> Migration Guide -> Testing -> Documentation Updates -> Next Steps -> Future Opportunities -> Known Limitations -> Lessons Learned

## Template file references

Full templates are maintained at:
- `.claude/templates/task-summary-template.md`
- `.claude/templates/feature-summary-template.md`
- `.claude/templates/bug-fix-summary-template.md`
- `.claude/templates/refactoring-summary-template.md`

**Verification:** Before using a template, confirm the file exists at the referenced path. If the file is missing, use the structure described in this skill document as the authoritative fallback.

# Procedure

<procedure>

### 1. Reason about work type

Before selecting a template, determine the primary nature of the work:
- Does the work introduce wholly new functionality? -> Feature Summary
- Does the work fix a specific defect with a root cause? -> Bug Fix Summary
- Does the work restructure existing code without changing behavior? -> Refactoring Summary
- Does the work fall outside the above categories (docs, config, tooling)? -> Task Summary

If the work spans two categories (e.g., a refactor that also fixes a bug), select the template matching the primary intent. If still ambiguous, ask the user.

Checkpoint: Work type determined; template selected with rationale.

### 2. Gather data
Collect from the user or from the completed work:
- Files created, modified, deleted (with counts)
- Measurable metrics (response time improvements, coverage delta, line counts)
- Known issues or limitations
- Next steps with owners

Checkpoint: Data collected; any gaps noted as N/A.

### 3. Fill the template
- Fill all sections. Mark sections without data as `N/A` rather than omitting them.
- **Do not invent metrics.** If data is unavailable, write `N/A -- measure post-deploy` instead of fabricating numbers.
- **Do not include sensitive data** (credentials, PII, internal API keys) in summary sections.
- Use specific numbers: "Updated 12 files: 5 created, 7 modified" not "Updated many files."

Checkpoint: All sections filled or marked N/A.

### 4. Choose output format
- **Markdown:** Default for inline use or when the caller specifies plain text.
- **HTML:** Use when the summary has >3 sections OR will be shared outside the terminal. HTML mapping:

| Markdown section | HTML equivalent |
|-----------------|-----------------|
| Status header | `<h1>` + status badge |
| Metrics table | `<table class="metrics">` |
| "How to Use" per role | `<details>` collapsible per role |
| Next Steps list | `<ul>` with `<input type="checkbox">` |
| Known Issues | `<div class="card" style="border-color:#7f1d1d">` |
| Positive findings | `<div class="card" style="border-color:#065f46">` |

Checkpoint: Format selected; output rendered.

### 5. Add project domain context
When summarizing platform work, use the project's domain terminology (see `.claude/project.json` -> `domainTerms` and the project's `CLAUDE.md`):
- Use the project's device noun (not a generic "device" or "node") when the project defines one
- Use the project's workflow nouns (e.g., "Mission", not "job" or "task", if that is the domain term)
- "Telemetry" (not "metrics") when referring to device data streams
- Use the project's name for the edge/gateway component (project.json -> `device`)
- Reference actual repo names from project.json -> `repos`

Checkpoint: Domain terminology applied.

### 6. Verify length budget
Count the output lines. If the output exceeds the length budget (200 lines markdown, 300 lines HTML), trim lower-priority sections (passing checks, verbose sub-items) until within budget.

Checkpoint: Output within length budget.

</procedure>

# Self-check before returning

- [ ] Correct template selected for the work type (with reasoning documented in Step 1)
- [ ] All sections filled out or marked N/A
- [ ] Metrics are quantified with numbers, not vague descriptions
- [ ] "How to Use" section exists with role-specific instructions
- [ ] Next steps are actionable with owners assigned
- [ ] Known issues documented (or explicitly stated as none)
- [ ] No metrics were fabricated -- all numbers come from actual data
- [ ] No sensitive data (credentials, PII) included
- [ ] Date is current
- [ ] Template file existence was verified before loading (if using external template files)
- [ ] Output stays within length budget (200 lines markdown / 300 lines HTML)

# Common mistakes to avoid

- **Inventing metrics** -- "Improved performance by 60%" with no measurement is worse than "N/A -- measure post-deploy"
- **Filling "TBD" placeholders with fabricated numbers** -- leave them as TBD or N/A
- **Including sensitive data** -- API keys, database passwords, PII must never appear in summaries
- **Vague next steps** -- "Test the feature" is not actionable; "QA testing of waypoint editing happy path and boundary cases -- [QA Team] -- by 2026-05-26" is actionable
- **Modifying source files while generating a summary** -- this skill produces summary documents only
- **Skipping template selection reasoning** -- always document why a template was chosen, especially for ambiguous work types

# What to surface to the user

- The selected template and why it was chosen
- The output format (markdown or HTML)
- Any sections marked N/A due to missing data, so the user can fill them in
- If the work type was ambiguous, state which template was chosen and why

# Escalation

- **Ambiguous work type** (e.g., refactor + bug fix): Reason about primary intent in Step 1; if still unclear, ask the user
- **Missing metrics data:** Mark as N/A with a note to measure post-deploy; do not block summary creation
- **External template file missing:** Fall back to the structure described in this SKILL.md
- **Summary requires cross-repo scope** (changes spanning multiple repos from project.json -> `repos`): Use the Task Summary template with a per-repo breakdown section

# Examples

<example title="Feature summary for mission management">

**Scenario:** Implemented mission CRUD with REST route handlers, a React UI, and PostgreSQL storage via the project's ORM.

**Step 1 reasoning:** This introduces wholly new functionality (mission CRUD). Template: Feature Summary.

**Key sections:**
- User Stories: "As an operator, I can create a new patrol mission with waypoints"
- Architecture: Route handlers at `src/app/api/missions/`, ORM schema extension, MissionCard component
- Security: Auth.js session checks, Zod input validation for mission parameters
- Testing: 15 unit tests, 5 integration tests, 3 E2E tests

</example>

<example title="Bug fix summary for telemetry reconnection">

**Scenario:** Fixed telemetry SSE stream not reconnecting after a device-gateway pod restart.

**Step 1 reasoning:** This fixes a specific defect (SSE reconnection failure) with a root cause (missing retry logic). Template: Bug Fix Summary.

**Key sections:**
- Severity: High
- Root Cause: EventSource `onerror` handler was closing the connection without scheduling a reconnect
- The Fix: Added exponential backoff retry logic in `useTelemetry` hook
- Prevention: Added Playwright E2E test simulating device-gateway disconnect/reconnect

</example>

<example title="Refactoring summary for auth middleware">

**Scenario:** Extracted auth checks from individual route handlers into shared middleware.

**Step 1 reasoning:** This restructures existing code (auth check extraction) without changing behavior. Template: Refactoring Summary.

**Key sections:**
- Problems Solved: Duplicated `await auth()` + session check in 14 route handlers
- Architecture: Before (inline checks) -> After (shared `withAuth()` wrapper)
- Metrics: Reduced auth-related code from 14 locations to 1 shared module
- Migration Guide: Replace `const session = await auth()` pattern with `export const GET = withAuth(handler)`

</example>

# Failure modes

| Failure | Recovery |
|---------|----------|
| User provides no metrics | Mark all metric fields as N/A; note in the summary that metrics should be measured post-deploy |
| Template file at `.claude/templates/` is missing | Use the structure documented in this SKILL.md as authoritative fallback |
| Work type does not fit any template | Use Task Summary as the catch-all default |
| Summary is requested for work not yet completed | Ask the user to confirm scope; fill completed sections and mark remaining as "In Progress" |

# Related skills

- **troubleshooting** -- contains the incident postmortem template (different from a work summary)
- **testing** -- for writing tests referenced in summary testing sections
- **code-standards** -- for code review findings that may feed into summary recommendations
