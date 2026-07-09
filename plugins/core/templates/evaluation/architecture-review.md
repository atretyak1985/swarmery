# Architecture Review

**Feature**: [feature name]  
**Architect**: @[username]  
**Reviewer**: @architecture-designer  
**Date**: [YYYY-MM-DD]

---

## Summary

[Brief description of architecture - 2-3 sentences]

**Scope**:
- **Modules**: [number] modules
- **Services**: [number] services
- **Databases**: [list databases]
- **External APIs**: [list external APIs]

---

## Architecture Diagram

```
[ASCII diagram or link to diagram]

Example:
┌─────────────┐
│   Client    │
└──────┬──────┘
       │
       ▼
┌─────────────┐     ┌─────────────┐
│  API Layer  │────▶│  Database   │
└─────────────┘     └─────────────┘
```

---

## Components

### Component 1: [Name]

**Purpose**: [What it does]  
**Responsibilities**:
- [Responsibility 1]
- [Responsibility 2]

**Dependencies**:
- [Dependency 1]
- [Dependency 2]

**Files**:
- `[file1.ts]`
- `[file2.ts]`

---

### Component 2: [Name]

**Purpose**: [What it does]  
**Responsibilities**:
- [Responsibility 1]
- [Responsibility 2]

**Dependencies**:
- [Dependency 1]
- [Dependency 2]

**Files**:
- `[file1.ts]`
- `[file2.ts]`

---

## LLM Evaluation

**Overall Score**: [X.X]/5.0

### Scores

| Criterion | Score | Status | Notes |
|-----------|-------|--------|-------|
| Separation of Concerns | [X]/5 | ✅/⚠️/❌ | [reason] |
| Dependency Management | [X]/5 | ✅/⚠️/❌ | [reason] |
| Scalability | [X]/5 | ✅/⚠️/❌ | [reason] |
| Consistency | [X]/5 | ✅/⚠️/❌ | [reason] |
| Documentation | [X]/5 | ✅/⚠️/❌ | [reason] |

**Pass Threshold**: ≥ 4.0 average

---

## Detailed Analysis

### Separation of Concerns

**Score**: [X]/5

**Strengths**:
- [Strength 1]
- [Strength 2]

**Concerns**:
- [Concern 1]
- [Concern 2]

---

### Dependency Management

**Score**: [X]/5

**Strengths**:
- [Strength 1]
- [Strength 2]

**Concerns**:
- [Concern 1]
- [Concern 2]

---

### Scalability

**Score**: [X]/5

**Strengths**:
- [Strength 1]
- [Strength 2]

**Concerns**:
- [Concern 1]
- [Concern 2]

---

### Consistency

**Score**: [X]/5

**Strengths**:
- [Strength 1]
- [Strength 2]

**Concerns**:
- [Concern 1]
- [Concern 2]

---

### Documentation

**Score**: [X]/5

**Strengths**:
- [Strength 1]
- [Strength 2]

**Concerns**:
- [Concern 1]
- [Concern 2]

---

## Recommendations

### High Priority

1. **[Recommendation 1]**
   - **Issue**: [Description]
   - **Impact**: [High/Medium/Low]
   - **Effort**: [XS/S/M/L/XL]
   - **Suggested Approach**: [How to address]

2. **[Recommendation 2]**
   - **Issue**: [Description]
   - **Impact**: [High/Medium/Low]
   - **Effort**: [XS/S/M/L/XL]
   - **Suggested Approach**: [How to address]

### Medium Priority

3. **[Recommendation 3]**
   - **Issue**: [Description]
   - **Impact**: [High/Medium/Low]
   - **Effort**: [XS/S/M/L/XL]
   - **Suggested Approach**: [How to address]

---

## Decision

- [ ] ✅ **APPROVE** - Architecture is sound and ready for implementation
- [ ] ⚠️ **APPROVE WITH CHANGES** - Minor improvements needed
- [ ] ❌ **REQUEST REDESIGN** - Critical architectural issues must be addressed

**Rationale**: [Explain decision]

---

## Next Steps

**If APPROVED**:
1. [ ] Create ADR (Architecture Decision Record)
2. [ ] Begin implementation
3. [ ] Schedule architecture review checkpoint

**If APPROVED WITH CHANGES**:
1. [ ] Address recommended improvements
2. [ ] Update architecture diagram
3. [ ] Re-submit for review

**If REQUEST REDESIGN**:
1. [ ] Address critical issues
2. [ ] Redesign affected components
3. [ ] Re-submit for review

---

**Reviewed by**: @quality-checker  
**Review Date**: [YYYY-MM-DD]  
**Review Duration**: [X] minutes

