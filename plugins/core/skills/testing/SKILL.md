---
name: testing
description: "WRITE, RUN, or DEBUG tests for the project's code -- pytest (Python), Jest/RTL (TypeScript), Playwright (E2E), and deployment config testing. NOT for finding coverage gaps or untested modules -- that read-only gap analysis belongs to test-coverage. NOT for CI pipeline configuration (use deployment)."
version: "1.0.0"
owner: "agentry-core"
allowed-tools: Read, Write, Bash, Grep, Glob
color: teal
---

# Purpose

You are a test engineer for the project's platform. You write tests, run test suites, and debug test failures across the project's repositories (see `.claude/project.json` → `repos`). A typical layout: a device/edge repo `<device>` (Python/pytest), the main app `apps/<mainApp>` (TypeScript/Jest + Playwright), and an infrastructure repo (deployment config testing). Placeholders `<mainApp>` and `<device>` come from `project.json → mainApp` / `device`.

# When to use

- User asks to "write a test," "add tests for," or "create test coverage for" a module
- User asks to "run tests," "run the test suite," or "check if tests pass"
- User asks to "debug a failing test," "fix this test failure," or "why is this test flaky?"
- User asks "how should I test this?" or "what testing pattern should I use?"
- When implementing a feature and need to add accompanying tests

# When NOT to use

- **Coverage gap analysis** (finding what needs tests) -- use `test-coverage` first, then return here to write the tests
- **CI pipeline configuration** (adding test jobs to `.gitlab-ci.yml`) -- use `deployment`
- **Code quality review** (linting, type checking) -- use `code-standards` or `code-quality`
- **Security vulnerability scanning** -- use `security-audit`
- **Measuring coverage percentages** -- run `npm run test -- --coverage` or `make test-all` directly

# Required environment

- Runtime: `.claude/skills/testing/SKILL.md`
- Tools: Read, Write, Bash, Grep, Glob

| Repository | Framework | Command | Notes |
|-----------|-----------|---------|-------|
| `<device>` (device/edge repo) | pytest | `make test` (unit), `make test-all` (full) | Hardware protocol libraries may be missing on macOS; use `--ignore` flag |
| `apps/<mainApp>` | Jest | `npm run test` | |
| `apps/<mainApp>` | Playwright | `npm run test:e2e` | Requires running app instance |
| infrastructure repo | chart tooling | template render + `--dry-run` install | |

# Inputs

| Input | Required | Description |
|-------|----------|-------------|
| Goal | Yes | One of: `write` (new tests), `run` (existing suite), `debug` (failing test) |
| Target | Yes | Module path, test file, or test name to work on |
| Repository | No | Inferred from path; specify if ambiguous |

# Outputs

- **write:** New test file(s) at the correct location, following repo conventions. Length budget: a single test file must not exceed 200 lines; split into multiple files by feature area if larger.
- **run:** Test execution output with pass/fail summary.
- **debug:** Root cause of failure identified, fix applied or recommended.

# Procedure

## Testing philosophy

- **Test behavior, not implementation** -- assert what the code does, not how
- **TDD for new functions** -- when writing a brand-new function, write the test stub before the implementation
- **Read-first for existing functions** -- when adding tests to existing code, read the source module first, identify edge cases, then write the test
- **One assertion per test** when practical -- keep tests focused
- **Fast feedback** -- unit tests should complete in milliseconds
- **No flaky tests** -- if a test is intermittent, fix it or delete it
- **AAA pattern** -- Arrange, Act, Assert in every test

## Testing pyramid

```
       /\
      /  \     E2E Tests (10%)
     /____\    - dashboard flows, critical user journeys
    /      \   Integration Tests (30%)
   /________\  - API route handlers, DB queries, WebSocket
  /          \ Unit Tests (60%)
 /____________\- Pure functions, telemetry parsing, formatters
```

---

## Goal: Write new tests

### Step 1: Identify the repository and test framework

| Repository | Framework | Test location | Naming convention |
|-----------|-----------|--------------|-------------------|
| `<device>` | pytest | `test/unit/test_*.py`, `test/integration/test_*.py` | `test_<module>.py` |
| `apps/<mainApp>` | Jest + RTL | `src/**/__tests__/*.test.{ts,tsx}` | `<module>.test.ts` |
| `apps/<mainApp>` | Playwright | `tests/e2e/*.spec.ts` | `<feature>.spec.ts` |
| infrastructure repo | template render / dry-run | N/A (inline commands) | N/A |

**Checkpoint:** Repository and framework confirmed.

### Step 2: Verify source module exists

Use Glob or Read to confirm the source module path exists. If the module is not found, STOP and ask the user to confirm the path. Do not write tests for a module that cannot be located.

**Checkpoint:** Source module file exists and has been read.

### Step 3: Write the test using appropriate patterns

<example name="device-repo-unit-test">
```python
# test/unit/test_telemetry_parser.py
import pytest
from src.telemetry_parser import TelemetryParser

class TestTelemetryParser:
    @pytest.fixture
    def parser(self):
        return TelemetryParser()

    def test_parse_position_message(self, parser):
        raw = {"type": "POSITION", "lat": 505000000, "lon": 305000000}
        result = parser.parse(raw)
        assert result["LATITUDE"] == 505000000
        assert result["LONGITUDE"] == 305000000

    def test_parse_invalid_message_raises(self, parser):
        with pytest.raises(ValueError, match="Unknown message type"):
            parser.parse({"type": "UNKNOWN"})
```
</example>

<example name="main-app-jest-unit-test">
```typescript
// src/lib/utils/__tests__/formatCoordinates.test.ts
import { formatCoordinates } from '../formatCoordinates';

describe('formatCoordinates', () => {
  it('should format raw telemetry lat/lon to degrees', () => {
    const result = formatCoordinates(505000000, 305000000);
    expect(result).toEqual({ lat: 50.5, lon: 30.5 });
  });

  it('should handle zero coordinates', () => {
    const result = formatCoordinates(0, 0);
    expect(result).toEqual({ lat: 0, lon: 0 });
  });
});
```
</example>

<example name="main-app-rtl-component-test">
```typescript
// src/components/__tests__/DeviceCard.test.tsx
import { render, screen } from '@testing-library/react';
import { DeviceCard } from '../DeviceCard';

describe('DeviceCard', () => {
  const mockDevice = { id: 1, identifier: 'd1', active: true };

  it('renders device identifier', () => {
    render(<DeviceCard device={mockDevice} />);
    expect(screen.getByText('d1')).toBeInTheDocument();
  });

  it('shows active status badge for active device', () => {
    render(<DeviceCard device={mockDevice} />);
    expect(screen.getByText('Active')).toBeInTheDocument();
  });
});
```
</example>

<example name="playwright-e2e-test">
```typescript
// tests/e2e/device-dashboard.spec.ts
test.describe('Device Dashboard', () => {
  test('should display device list', async ({ page }) => {
    await page.goto('/dashboard');
    await expect(page.locator('[data-testid="device-card"]')).toHaveCount(9);
  });

  test('should show telemetry for selected device', async ({ page }) => {
    await page.goto('/dashboard');
    await page.click('[data-testid="device-card"]:first-child');
    await expect(page.locator('[data-testid="telemetry-panel"]')).toBeVisible();
  });
});
```
</example>

<example name="deployment-config-testing">
```bash
cd <infrastructure-repo>
npm run build charts/<mainApp>/ -f charts/<mainApp>/values.localdev.yaml
gcloud run deploy --dry-run <mainApp> charts/<mainApp>/ \
  -f charts/<mainApp>/values.localdev.yaml \
  --set ingress.enabled=true
helm upgrade --install <mainApp> charts/<mainApp>/ \
  -f charts/<mainApp>/values.localdev.yaml \
  -n <mainApp> --dry-run
```
</example>

**Checkpoint:** Test file written and syntax-valid.

### Step 4: Run the test

After writing a test, always run it to confirm it passes:

```bash
# device repo
pytest test/unit/test_<module>.py -v

# main app
npx jest src/lib/<module>/__tests__/<module>.test.ts

# Skip hardware-protocol tests on macOS
pytest --ignore=test/test_get_telemetry.py
```

**Checkpoint:** Test passes. If it fails, debug before returning.

---

## Goal: Run existing test suite

```bash
# device repo
cd <device>
make test              # Unit tests only (fast)
make test-all          # Full suite (requires DB for integration tests)
pytest test/unit/test_telemetry_parser.py -v  # Single file

# main app
cd apps/<mainApp>
npm run test           # Jest unit tests
npm run test:e2e       # Playwright E2E (requires running app)
npx jest <path> -v     # Single file

# deployment config
cd <infrastructure-repo>
npm run build charts/<mainApp>/ -f charts/<mainApp>/values.localdev.yaml
```

**Warning:** `make test-all` runs integration tests that may require a live database and hardware connections. Use `make test` for fast unit-only feedback.

---

## Goal: Debug a failing test

### Step 1: Get the failure output

```bash
# Python
pytest test/unit/test_<module>.py -v -s --tb=long
# TypeScript
npx jest <path> --verbose --no-cache
```

### Step 2: Check environment prerequisites

- **Database required?** Integration tests may need a running PostgreSQL instance
- **Hardware required?** Device-repo tests involving the hardware protocol need real device hardware or `MOCK_MODE=true`
- **App running?** Playwright E2E tests need the app server running

### Step 3: Identify root cause

- **Assertion failure:** Compare expected vs. actual values; check if the source code behavior changed
- **Import error:** Verify module paths; check if the module under test was renamed or moved
- **Timeout:** Check if the test depends on an external service not available
- **Flaky test:** Run 5 times in sequence; if it passes intermittently, check for race conditions or shared state

### Step 4: Fix and re-run

Apply the fix, then run the test again. Also run the full suite to check for regressions.

**Checkpoint:** Test passes. Full suite passes.

# Self-check before returning

- [ ] Test file is in the correct location following repo naming conventions
- [ ] Test uses AAA pattern (Arrange, Act, Assert)
- [ ] Test assertions verify behavior, not implementation details
- [ ] Test runs successfully (executed after writing)
- [ ] No vacuous assertions (`expect(true).toBe(true)`)
- [ ] Test does not depend on execution order (can run independently)
- [ ] Mock/fixture data is realistic (uses the project's domain types -- see the consumer project's `CLAUDE.md` and schema)
- [ ] For integration tests: prerequisite services are documented in test comments
- [ ] For E2E tests: `data-testid` attributes are used for selectors (not CSS classes)

# Common mistakes to avoid

- **Vacuous assertions** -- `expect(true).toBe(true)` or `assert True` prove nothing
- **Testing implementation details** -- asserting that a specific internal method was called N times; assert on the output instead
- **Missing error case tests** -- every function that can fail should have a test for the failure path
- **Hardcoded test data that drifts** -- use fixtures/factories; update when schema changes
- **Running integration tests without checking prerequisites** -- `make test-all` requires a database; `npm run test:e2e` requires a running app
- **Not running the test after writing it** -- always execute the test to verify it passes

# Escalation

- **Test requires infrastructure not available locally** (live database, hardware, cluster): Document the prerequisite and recommend running in CI instead
- **Flaky test cannot be stabilized after three attempts:** Flag for manual investigation; do not delete without user approval
- **Test framework not installed:** Provide installation command but do not run `npm install` or `pip install` without user confirmation

# Failure modes

| Failure | Recovery |
|---------|----------|
| Hardware protocol library import error on macOS | Use `--ignore` for the hardware-dependent test files; these tests require device hardware |
| Jest cannot find module | Check import paths; verify `tsconfig.json` path aliases match |
| Playwright timeout | Verify app server is running; increase timeout for slow CI |
| Template render fails | Check template syntax; verify values file exists and is valid YAML |
| Test passes locally but fails in CI | Check env differences (NODE_ENV, database URL, timezone) |

# Related skills

- **test-coverage** -- identifies *what* needs tests (gap analysis); this skill provides *how* to write them. Coverage targets are defined in the test-coverage skill as the single source of truth.
- **code-standards** -- testing naming conventions and code quality standards
- **troubleshooting** -- for debugging live application issues (vs. test failures)

## Coverage targets (reference)

See `test-coverage` skill for the authoritative coverage target table. This skill is responsible for writing tests to meet those targets, not defining them.
