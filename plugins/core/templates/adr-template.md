# Architecture Decision Record (ADR) Template

**Purpose**: Document important architectural decisions

**When to Use**: When making significant architectural or design decisions

---

## Template

```markdown
# ADR-[NUMBER]: [Title]

**Status**: [Proposed / Accepted / Deprecated / Superseded]  
**Date**: [YYYY-MM-DD]  
**Authors**: [Names]  
**Reviewers**: [Names]  
**Supersedes**: [ADR-XXX] (if applicable)  
**Superseded by**: [ADR-XXX] (if applicable)

---

## Context

[Describe the context and background of the decision]

**Problem Statement**:
[What problem are we trying to solve?]

**Current Situation**:
[What is the current state? What are the pain points?]

**Constraints**:
- [Constraint 1]
- [Constraint 2]
- [Constraint 3]

**Requirements**:
- [Requirement 1]
- [Requirement 2]
- [Requirement 3]

---

## Decision

[What is the decision we're making?]

**Summary**:
[One-paragraph summary of the decision]

**Details**:
[Detailed explanation of the decision]

---

## Options Considered

### Option 1: [Name]

**Description**:
[Detailed description of this option]

**Pros**:
- ✅ [Pro 1]
- ✅ [Pro 2]
- ✅ [Pro 3]

**Cons**:
- ❌ [Con 1]
- ❌ [Con 2]
- ❌ [Con 3]

**Estimated Effort**: [X days/weeks]

---

### Option 2: [Name]

**Description**:
[Detailed description of this option]

**Pros**:
- ✅ [Pro 1]
- ✅ [Pro 2]
- ✅ [Pro 3]

**Cons**:
- ❌ [Con 1]
- ❌ [Con 2]
- ❌ [Con 3]

**Estimated Effort**: [X days/weeks]

---

### Option 3: [Name]

**Description**:
[Detailed description of this option]

**Pros**:
- ✅ [Pro 1]
- ✅ [Pro 2]
- ✅ [Pro 3]

**Cons**:
- ❌ [Con 1]
- ❌ [Con 2]
- ❌ [Con 3]

**Estimated Effort**: [X days/weeks]

---

## Comparison Matrix

| Criteria | Weight | Option 1 | Option 2 | Option 3 |
|----------|--------|----------|----------|----------|
| Performance | High | ⭐⭐⭐ | ⭐⭐ | ⭐⭐⭐⭐ |
| Maintainability | High | ⭐⭐⭐⭐ | ⭐⭐⭐ | ⭐⭐ |
| Scalability | Medium | ⭐⭐⭐ | ⭐⭐⭐⭐ | ⭐⭐⭐ |
| Cost | Medium | ⭐⭐⭐⭐ | ⭐⭐ | ⭐⭐⭐ |
| Time to Implement | Low | ⭐⭐⭐ | ⭐⭐⭐⭐ | ⭐⭐ |
| **Total Score** | | **X** | **Y** | **Z** |

---

## Chosen Option

**Selected**: Option [X] - [Name]

**Rationale**:
[Why was this option chosen over the others?]

**Key Factors**:
- [Factor 1]
- [Factor 2]
- [Factor 3]

---

## Consequences

### Positive Consequences

- ✅ [Positive consequence 1]
- ✅ [Positive consequence 2]
- ✅ [Positive consequence 3]

### Negative Consequences

- ⚠️ [Negative consequence 1] - [Mitigation strategy]
- ⚠️ [Negative consequence 2] - [Mitigation strategy]

### Risks

- 🔴 **High Risk**: [Risk description] - [Mitigation plan]
- 🟡 **Medium Risk**: [Risk description] - [Mitigation plan]
- 🟢 **Low Risk**: [Risk description] - [Mitigation plan]

---

## Implementation Plan

### Phase 1: [Name] (Week 1-2)

**Tasks**:
- [ ] Task 1
- [ ] Task 2
- [ ] Task 3

**Deliverables**:
- [Deliverable 1]
- [Deliverable 2]

---

### Phase 2: [Name] (Week 3-4)

**Tasks**:
- [ ] Task 1
- [ ] Task 2
- [ ] Task 3

**Deliverables**:
- [Deliverable 1]
- [Deliverable 2]

---

### Phase 3: [Name] (Week 5-6)

**Tasks**:
- [ ] Task 1
- [ ] Task 2
- [ ] Task 3

**Deliverables**:
- [Deliverable 1]
- [Deliverable 2]

---

## Success Metrics

**How will we measure success?**

- **Performance**: [Metric and target]
- **Reliability**: [Metric and target]
- **User Satisfaction**: [Metric and target]
- **Developer Experience**: [Metric and target]
- **Cost**: [Metric and target]

**Monitoring**:
- [What metrics will we monitor?]
- [How often will we review?]
- [What are the thresholds for action?]

---

## Migration Strategy

**From**: [Current state]  
**To**: [Future state]

**Steps**:
1. [Migration step 1]
2. [Migration step 2]
3. [Migration step 3]

**Rollback Plan**:
[How to rollback if migration fails]

**Environment / Promotion Notes**:
- Target environment(s): [local-only / <envAlias> / staging / production]
- Approval points: [What must be approved]
- Verification requirements: [How success will be confirmed]
- Promotion rule: [What must happen before advancing current state]

**Timeline**: [X weeks/months]

---

## Documentation Updates

**Required Updates**:
- [ ] README.md
- [ ] API documentation
- [ ] Developer guides
- [ ] Runbooks
- [ ] Architecture diagrams

---

## Related

**Related ADRs**:
- [ADR-XXX] - [Title]
- [ADR-YYY] - [Title]

**Related Documents**:
- [Design doc]
- [Technical spec]
- [Research findings]

**Related Issues/PRs**:
- Issue #XXX
- PR #YYY

---

## References

**Research**:
- [Article/Blog post]
- [Documentation]
- [Case study]

**Similar Implementations**:
- [Company/Project] - [How they solved it]
- [Company/Project] - [How they solved it]

---

## Review History

| Date | Reviewer | Status | Comments |
|------|----------|--------|----------|
| YYYY-MM-DD | [Name] | Approved | [Comments] |
| YYYY-MM-DD | [Name] | Approved | [Comments] |
| YYYY-MM-DD | [Name] | Requested Changes | [Comments] |

---

## Appendix

### Technical Details

[Any technical details, diagrams, code examples, etc.]

### Glossary

- **Term 1**: Definition
- **Term 2**: Definition

---

**Status**: [Proposed / Accepted / Deprecated / Superseded]  
**Last Updated**: [YYYY-MM-DD]  
**Next Review**: [YYYY-MM-DD]
```

---

## ADR Status Definitions

- **Proposed**: Decision is being discussed
- **Accepted**: Decision has been approved and is being implemented
- **Deprecated**: Decision is no longer recommended but still in use
- **Superseded**: Decision has been replaced by a newer ADR

---

## When to Create an ADR

Create an ADR when making decisions about:

- **Architecture**: System design, service boundaries, data flow
- **Technology**: Framework choices, library selections, tool adoption
- **Patterns**: Design patterns, coding standards, architectural patterns
- **Infrastructure**: Deployment strategies, hosting choices, CI/CD
- **Data**: Database choices, schema design, data migration strategies
- **Security**: Authentication methods, authorization patterns, encryption
- **Performance**: Caching strategies, optimization approaches
- **Integration**: Third-party services, API design, webhook patterns

---

## Quality Checklist

Before finalizing ADR:

- [ ] Problem clearly stated
- [ ] At least 3 options considered
- [ ] Pros and cons listed for each option
- [ ] Comparison matrix completed
- [ ] Chosen option justified
- [ ] Consequences documented
- [ ] Implementation plan included
- [ ] Success metrics defined
- [ ] Migration strategy documented
- [ ] Reviewed by stakeholders
- [ ] Diagrams included (if needed)

---

**Version**: 1.0  
**Last Updated**: December 3, 2025  
**Maintained by**: agentry-core

