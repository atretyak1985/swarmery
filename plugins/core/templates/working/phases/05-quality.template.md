# Phase 5: Quality Check

**Task**: {{TASK_NAME}}
**Agents**: @quality-checker + @verification-agent (parallel)
**Started**: {{PHASE_START}}
**Completed**: {{PHASE_END}}
**Duration**: {{PHASE_DURATION}}

---

## Quality Checks

### 1. Code Formatting (npm run code:fix)

**Status**: {{FORMAT_STATUS}}
**Output**:
```
{{FORMAT_OUTPUT}}
```

### 2. Type Checking (npm run typecheck)

**Status**: {{TYPECHECK_STATUS}}
**Output**:
```
{{TYPECHECK_OUTPUT}}
```

### 3. Linting (npm run lint)

**Status**: {{LINT_STATUS}}
**Output**:
```
{{LINT_OUTPUT}}
```

### 4. Build (npm run build)

**Status**: {{BUILD_STATUS}}
**Output**:
```
{{BUILD_OUTPUT}}
```

### 5. Tests (npm run test)

**Status**: {{TEST_STATUS}}
**Coverage**: {{TEST_COVERAGE}}%
**Output**:
```
{{TEST_OUTPUT}}
```

---

## Security Review (if applicable)

**Agent**: @security-auditor
**Status**: {{SECURITY_STATUS}}

| Check | Status | Notes |
|-------|--------|-------|
| Authorization | {{AUTH_STATUS}} | {{AUTH_NOTES}} |
| Input Validation | {{VALIDATION_STATUS}} | {{VALIDATION_NOTES}} |
| Rate Limiting | {{RATE_LIMIT_STATUS}} | {{RATE_LIMIT_NOTES}} |
| Audit Logging | {{AUDIT_STATUS}} | {{AUDIT_NOTES}} |

---

## Issues Found

### Issue 1: {{QUALITY_ISSUE_1}}

**Severity**: {{ISSUE_1_SEVERITY}}
**Fix**: {{ISSUE_1_FIX}}
**Status**: {{ISSUE_1_STATUS}}

---

## Verification (@verification-agent)

**Status**: {{VERIFICATION_STATUS}}

| Check | Status |
|-------|--------|
| Tests pass | {{VERIFICATION_TESTS}} |
| Build succeeds | {{VERIFICATION_BUILD}} |
| Lint clean | {{VERIFICATION_LINT}} |
| Type check | {{VERIFICATION_TYPES}} |

---

## Quality Summary

| Metric | Before | After |
|--------|--------|-------|
| Type Errors | {{TYPE_ERRORS_BEFORE}} | {{TYPE_ERRORS_AFTER}} |
| Lint Errors | {{LINT_ERRORS_BEFORE}} | {{LINT_ERRORS_AFTER}} |
| Test Coverage | {{COVERAGE_BEFORE}}% | {{COVERAGE_AFTER}}% |

---

## Next Phase

→ Phase 6: Downstream Changes (@downstream-analyzer)

