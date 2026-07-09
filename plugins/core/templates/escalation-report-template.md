# Escalation Report — Quality Gate Failure

> Use this template when a Phase 5 Quality Gate returns FAIL after 2 re-dispatch rounds, or when a task has been retried > 2 times across any phase. Fill and hand to Tech Lead / user for resolution decision.

---

## Task

| Field | Value |
|-------|-------|
| **Task ID** | [task-id] |
| **Task Description** | [What was being implemented] |
| **Implementation Agent** | [Agent name] |
| **Quality Gate Agents** | [Agents in Phase 5 — e.g. verification-agent, quality-checker, plan-reviewer, security-auditor] |
| **Attempts exhausted** | [N/2 re-dispatch rounds or N/3 implementation retries] |
| **Escalation to** | Tech Lead / User |
| **Timestamp** | [YYYY-MM-DDTHH:MM:SSZ] |

---

## Failure History

### Attempt 1
- **Phase that failed**: [Phase N]
- **Issues found**: [Summary — error messages, quality verdicts, specific failures]
- **Fixes applied**: [What changed between attempt 1 and 2]
- **Result**: FAIL — [Why it still failed]

### Attempt 2
- **Phase that failed**: [Phase N]
- **Issues found**: [Summary]
- **Fixes applied**: [What changed]
- **Result**: FAIL — [Why it still failed]

### Attempt 3 (if applicable)
- **Phase that failed**: [Phase N]
- **Issues found**: [Summary]
- **Fixes applied**: [What changed]
- **Result**: FAIL — [Why it still failed]

---

## Root Cause Analysis

**Why the task keeps failing**: [Analysis — what underlying issue is not resolved by retries]

**Systemic issue**: [Is this a one-off or a pattern? Does it point to a missing guardrail, missing context, or wrong plan?]

**Complexity assessment**: [Was the task properly scoped? Was the Phase 3 plan adequate?]

**Which pre-mortem risk (Phase 3.6) materialised**: [Reference the risk table entry if applicable, or "not identified in pre-mortem" — flag for retrospective]

---

## Impact Assessment

| Aspect | Detail |
|--------|--------|
| **Blocking** | [What other tasks / phases are blocked by this] |
| **Timeline impact** | [How this affects the overall task/sprint schedule] |
| **Quality if accepted** | [What quality compromises exist if current state is merged] |
| **Security / data risk** | [Any safety concerns with the current failing state] |

---

## Recommended Resolution

Choose one — Tech Lead or user must select and confirm:

- [ ] **Re-plan** — return to Phase 3 with the failure as a new Unknown-codebase input; revise plan before retry
- [ ] **Reassign** — delegate Phase 4 to a different specialist agent: [recommended agent + reason]
- [ ] **Decompose** — break the failing task into smaller sub-tasks: [proposed breakdown]
- [ ] **Revise approach** — architecture or design change needed; invoke `@architecture-designer` before retry
- [ ] **Accept with limitations** — merge current state with documented known issues: [what is documented]
- [ ] **Defer** — move to next sprint; [reason why deferral is safe]

---

## Decision Required

| Field | Value |
|-------|-------|
| **Decision maker** | Tech Lead / User |
| **Decision deadline** | [When decision is needed to avoid further delay] |
| **Next action after decision** | [What Tech Lead will do once decision is received] |

