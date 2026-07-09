# Code Review Evaluation

**PR**: #[number]  
**Author**: @[username]  
**Reviewer**: @code-auditor  
**Date**: [YYYY-MM-DD]

---

## Summary

[Brief description of changes - 2-3 sentences]

**Files Changed**: [number] files  
**Lines Added**: +[number]  
**Lines Removed**: -[number]

---

## Deterministic Checks

### Syntax & Lint

- [ ] ✅ Syntax: PASS
- [ ] ✅ Lint: PASS
- [ ] ✅ Prettier: PASS

**Command**: `npm run code:fix`

### Type Checking

- [ ] ✅ TypeScript: PASS
- [ ] ✅ Type Coverage: [XX]% (target: 90%)

**Command**: `npm run typecheck`

### Tests

- [ ] ✅ Unit Tests: PASS ([X,XXX]/[X,XXX])
- [ ] ✅ Integration Tests: PASS ([XXX]/[XXX])
- [ ] ✅ E2E Tests: PASS ([XX]/[XX])

**Command**: `npm test`

### Build

- [ ] ✅ Build: PASS
- [ ] ✅ Bundle Size: [XXX]KB (budget: [XXX]KB)

**Command**: `npm run build`

---

## LLM Evaluation

**Overall Score**: [X.X]/5.0

### Scores

| Criterion | Score | Status | Notes |
|-----------|-------|--------|-------|
| Readability | [X]/5 | ✅/⚠️/❌ | [reason] |
| Maintainability | [X]/5 | ✅/⚠️/❌ | [reason] |
| Testability | [X]/5 | ✅/⚠️/❌ | [reason] |
| Error Handling | [X]/5 | ✅/⚠️/❌ | [reason] |
| Performance | [X]/5 | ✅/⚠️/❌ | [reason] |

**Pass Threshold**: ≥ 3.5 average

---

## Detailed Analysis

### Readability

**Score**: [X]/5

**Strengths**:
- [Strength 1]
- [Strength 2]

**Improvements**:
- [Improvement 1]
- [Improvement 2]

---

### Maintainability

**Score**: [X]/5

**Strengths**:
- [Strength 1]
- [Strength 2]

**Improvements**:
- [Improvement 1]
- [Improvement 2]

---

### Testability

**Score**: [X]/5

**Strengths**:
- [Strength 1]
- [Strength 2]

**Improvements**:
- [Improvement 1]
- [Improvement 2]

---

### Error Handling

**Score**: [X]/5

**Strengths**:
- [Strength 1]
- [Strength 2]

**Improvements**:
- [Improvement 1]
- [Improvement 2]

---

### Performance

**Score**: [X]/5

**Strengths**:
- [Strength 1]
- [Strength 2]

**Improvements**:
- [Improvement 1]
- [Improvement 2]

---

## Recommendations

### High Priority

1. **[Recommendation 1]**
   - **Issue**: [Description]
   - **Impact**: [High/Medium/Low]
   - **Effort**: [XS/S/M/L/XL]
   - **Suggested Fix**: [How to fix]

2. **[Recommendation 2]**
   - **Issue**: [Description]
   - **Impact**: [High/Medium/Low]
   - **Effort**: [XS/S/M/L/XL]
   - **Suggested Fix**: [How to fix]

### Medium Priority

3. **[Recommendation 3]**
   - **Issue**: [Description]
   - **Impact**: [High/Medium/Low]
   - **Effort**: [XS/S/M/L/XL]
   - **Suggested Fix**: [How to fix]

### Low Priority (Nice to Have)

4. **[Recommendation 4]**
   - **Issue**: [Description]
   - **Impact**: [High/Medium/Low]
   - **Effort**: [XS/S/M/L/XL]
   - **Suggested Fix**: [How to fix]

---

## Decision

- [ ] ✅ **APPROVE** - Code meets all quality standards
- [ ] ⚠️ **APPROVE WITH CHANGES** - Minor improvements needed (can be done later)
- [ ] ❌ **REQUEST CHANGES** - Critical issues must be fixed before merge

**Rationale**: [Explain decision]

---

## Next Steps

**If APPROVED**:
1. [ ] Merge PR
2. [ ] Deploy to staging
3. [ ] Monitor for issues

**If APPROVED WITH CHANGES**:
1. [ ] Create follow-up issues for improvements
2. [ ] Merge PR
3. [ ] Address improvements in next sprint

**If REQUEST CHANGES**:
1. [ ] Author fixes critical issues
2. [ ] Re-run quality checks
3. [ ] Re-submit for review

---

**Reviewed by**: @quality-checker  
**Review Date**: [YYYY-MM-DD]  
**Review Duration**: [X] minutes

