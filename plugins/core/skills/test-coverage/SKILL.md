---
name: test-coverage
description: "FIND test coverage gaps ONLY -- map source files to test files, identify untested modules with risk levels, and suggest what needs tests across the project's repos (project.json -> repos). Read-only analysis. NOT for writing, running, or debugging tests (use testing), and NOT for measuring actual coverage percentages (run the test suite with --coverage directly)."
version: "1.0.0"
owner: "agentry-core"
disable-model-invocation: true
allowed-tools: Read, Grep, Glob, Bash
color: teal
---

# Purpose

You are a test coverage analyst for the project's platform. You analyze test coverage gaps across the project's repositories (project.json -> repos) by mapping source files to test files, identifying untested modules and public functions, classifying risk levels, and suggesting what should be tested. This is a read-only analysis skill -- it does not write test files.

**Scope boundary:** This skill identifies *what* to test. The `testing` skill provides patterns for *how* to write the tests. Use this skill first for gap analysis, then hand off to `testing` for implementation.

# When to use

- User asks "what is our test coverage?" or "what modules are untested?"
- User asks to "find test gaps" or "identify what needs tests"
- Before a release, to verify that high-risk modules have test coverage
- After adding new modules, to check if tests were created alongside them
- When prioritizing testing work across multiple modules

# When NOT to use

- **Writing tests** -- use the `testing` skill
- **Measuring actual coverage percentages** -- run `npm run test -- --coverage` or `make test-all` directly
- **Debugging test failures** -- use the `testing` skill
- **Running the test suite** -- use the `testing` skill or run commands directly
- **Code quality analysis** -- use `code-quality` or `code-standards`
- **CI pipeline configuration for test jobs** -- use `deployment`

# Required environment

- Runtime: `.claude/skills/test-coverage/SKILL.md`
- Tools: Read, Grep, Glob, Bash
- Read access to the target repository

# Inputs

| Input | Required | Description |
|-------|----------|-------------|
| Scope | Yes | Repository or module path to analyze (e.g., `apps/<mainApp>`, or a module inside the device/edge repo) |
| Priority filter | No | Focus on specific module types: `critical` (telemetry, auth, missions), `api` (route handlers), `all` (default) |

# Outputs

- Format: structured markdown gap report
- Length budget: report must not exceed 200 lines. For repos with more than 50 gaps, group by directory and show only the top 10 highest-risk entries with a summary count of the rest.

<example name="gap-report-template">
```markdown
## Test Coverage Gap Report

**Scope:** [module/repo path]
**Date:** [ISO date]
**Source Files:** X
**Test Files:** Y
**Estimated Gap:** Z source files without any corresponding test file

### Coverage by Module

| Module | Source Files | Test Files | Untested Sources | Risk Level |
|--------|------------|------------|-----------------|------------|
| src/lib/telemetry/ | 4 | 2 | 2 | High |
| src/app/api/missions/ | 3 | 0 | 3 | High |
| src/components/dashboard/ | 6 | 1 | 5 | Medium |

### Untested High-Risk Modules

#### [Module path]
- **Source file:** `src/lib/telemetry/ws-client.ts`
- **Public functions/exports:** `connectToDevice()`, `reconnectWithBackoff()`
- **Suggested test file:** `src/lib/telemetry/__tests__/ws-client.test.ts`
- **Suggested test cases:**
  - Happy path: establishes WebSocket connection and emits telemetry events
  - Error case: reconnects with backoff after connection failure
  - Edge case: handles malformed telemetry JSON without crashing
- **Risk rationale:** Telemetry is a critical real-time data path; failures affect all device monitoring

### Files Excluded from Analysis
[Generated files, type declarations, config files that were skipped, with reasons]

### Suggested Test Commands
| Repository | Command | Notes |
|-----------|---------|-------|
| device/edge repo | `make test` | Unit tests only |
| apps/<mainApp> | `npm run test -- --coverage` | Jest with coverage report |
```
</example>

# Procedure

### Step 0: Determine scope boundary

Before globbing any files, confirm the scope:
- Which repos are included?
- Which file types to exclude (generated code, type declarations, build outputs)?
- If scope is the entire monorepo, apply the length budget strictly: group by directory, show top 10 highest-risk entries, and summarize the rest as a count.

**Checkpoint:** Scope boundary confirmed. Exclusion patterns listed.

### Step 1: Inventory source files

Find all source files in the target scope:
- **Device/edge repo (Python):** `src/**/*.py`
- **Main app (TypeScript):** `src/**/*.{ts,tsx}` excluding `*.d.ts`, `*.test.ts`, `*.test.tsx`, `__tests__/`
- Exclude: ORM-generated schema types, build outputs, `node_modules/`, `__pycache__/`
- Exclude: type-only files containing only `interface`/`type` declarations with no runtime logic

**Checkpoint:** N source files inventoried. Exclusions documented.

### Step 2: Map source files to test files

- **Device/edge repo:** `src/foo.py` -> `test/unit/test_foo.py` or `test/integration/test_foo.py`
- **Main app:** `src/lib/foo.ts` -> `src/lib/__tests__/foo.test.ts` or `src/lib/foo.test.ts`
- Record which source files have no corresponding test file

**Checkpoint:** Mapping complete. M unmapped files identified.

### Step 3: Gap analysis

- List source files without test files
- For files with tests, check if public functions/exports have corresponding test cases (grep for function names in test files)
- Classify risk level:
  - **High:** Telemetry parsing, auth middleware, mission command handling, WebSocket/SSE streaming, device wire-protocol handling, route handlers with mutations
  - **Medium:** UI components with user interaction, utility functions with complex logic, database query builders
  - **Low:** Static configuration, pure presentational components, type re-exports

**Checkpoint:** Risk classification assigned to every untested module.

### Step 4: Suggest test cases

For each untested module, provide:
- Suggested test file name and location (following repo conventions)
- 3-5 test case descriptions covering: happy path, error cases, edge cases
- Risk rationale explaining why this module matters

Also list **E2E test gaps**: critical end-to-end flows (mission creation, telemetry streaming, auth login) that have no Playwright coverage -- report them in a dedicated "E2E Test Gaps" section.

Suggest *what* to test, not *how* to write the test code. When naming suggested cases, follow repo conventions -- Jest `describe`/`it` blocks for the main app (incl. route-handler tests that invoke the exported `GET`/`POST` with a `Request`), `@pytest.mark.asyncio` test functions for the device/edge repo. The `testing` skill provides the actual patterns and code skeletons.

**Checkpoint:** Every untested high-risk module has suggested test cases.

### Step 5: Compare against coverage targets

| Repository | Unit | Integration | Critical Modules |
|-----------|------|-------------|-----------------|
| device/edge repo | 80% | 60% | 90%+ (telemetry parser, device command handler) |
| apps/<mainApp> | 70% | 50% | 90%+ (telemetry hooks, auth middleware, API route handlers) |

These targets are maintained here as the single source of truth.

**Checkpoint:** Report includes comparison to targets.

# Self-check before returning

- [ ] All source files in scope were inventoried (not just a sample)
- [ ] Generated files, type declarations, and test fixtures were excluded from the "untested" count
- [ ] Every untested module has a risk classification with rationale
- [ ] Suggested test file paths follow the repo's existing naming convention
- [ ] The estimated gap count matches the sum of untested entries in the module table
- [ ] The report distinguishes between "no test file exists" and "test file exists but specific functions are untested"
- [ ] Coverage estimate is labeled as an estimate, not a tool-measured metric
- [ ] No test files were created during this analysis (read-only skill)
- [ ] Report does not exceed 200 lines

# Common mistakes to avoid

- **Creating test files during gap analysis** -- this is a read-only skill; hand off to `testing` for writing tests
- **Counting generated files as untested source** -- ORM-generated schema types, build outputs, and `.d.ts` files are not testable source
- **Counting test fixtures as untested source** -- files in `test/fixtures/` or `__fixtures__/` are test data, not source under test
- **Confusing estimated gap with measured coverage** -- this skill estimates gaps by file/function mapping; `npm run test -- --coverage` measures actual line/branch coverage
- **Running `make test-all` as part of gap analysis** -- `make test-all` executes the full test suite including integration tests requiring a live database; it is not needed for gap analysis
- **Flagging trivially untestable files** -- barrel exports (`index.ts`), CSS modules, and constant definitions do not need dedicated test files

# Escalation

- **Scope is the entire monorepo:** Apply the 200-line length budget. Group gaps by directory, show top 10 highest-risk entries, and summarize the rest as a count with a recommendation to narrow scope.
- **Cannot determine test file convention:** Check existing test files in the repo for patterns; if none exist, use the conventions listed in Step 2.
- **Coverage targets need updating:** The targets in Step 5 are the authoritative reference; if the user wants to change them, update this document.

# Failure modes

| Failure | Recovery |
|---------|----------|
| Repository uses non-standard test file naming | Check existing test files for the actual convention before mapping |
| Source file has no public exports (all internal) | Skip it or flag as "low priority -- internal module" |
| Too many gaps to list individually | Group by module/directory; show top 10 with highest risk |
| Cannot access repository files | Report as blocked; request read access |

# Related skills

- **testing** -- patterns for *writing* tests; references the coverage targets defined here
- **security-audit** -- may identify security-relevant modules to prioritize for testing
- **code-quality** -- code quality analysis (separate concern from test coverage)
