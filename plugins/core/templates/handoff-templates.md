# Handoff Templates

> Standardized templates for agent-to-agent handoffs. Consistent handoffs prevent context loss — the #1 cause of multi-agent coordination failure.

| Situation | Template |
|-----------|----------|
| Phase 5 Quality Gate approves | QA PASS (#1) |
| Phase 5 Quality Gate rejects | QA FAIL (#2) |
| Moving between NEXUS-style phases | Phase Gate Handoff (#3) |
| End of sprint / task batch | Sprint Handoff (#4) |
| Quality gate failure after 2 retries | → use `escalation-report-template.md` |

---

## 1. QA PASS

Use when Phase 5 (`@verification-agent`, `@quality-checker`, `@plan-reviewer`, `@security-auditor`) all return green.

```markdown
# QA Verdict: PASS ✅

## Task
| Field | Value |
|-------|-------|
| **Task ID** | [task-id] |
| **Description** | [Description] |
| **Implementation Agent** | [Agent] |
| **QA Agents** | [Agents] |
| **Attempt** | [N] |
| **Timestamp** | [YYYY-MM-DDTHH:MM:SSZ] |

## Evidence
**Verification**: PASS — [test counts, build, lint, typecheck summary]
**Quality**: PASS — [code-quality findings, none / minor]
**Plan review**: PASS — [plan vs implementation match]
**Security**: PASS — [no new vulnerabilities, OWASP checks]

## Acceptance Criteria
- [x] [Criterion 1] — verified
- [x] [Criterion 2] — verified

## Next Action
→ Tech Lead: mark Phase 5 COMPLETE, advance to Phase 6 (Downstream)
```

---

## 2. QA FAIL

Use when any Phase 5 agent returns FAIL and retry count < 2.

```markdown
# QA Verdict: FAIL ❌

## Task
| Field | Value |
|-------|-------|
| **Task ID** | [task-id] |
| **Description** | [Description] |
| **Implementation Agent** | [Agent] |
| **QA Agents** | [Agents] |
| **Attempt** | [N of 2] |
| **Timestamp** | [YYYY-MM-DDTHH:MM:SSZ] |

## Issues Found

### Issue 1: [Category] — Severity: [Critical/High/Medium/Low]
**Description**: [Exact description]
**Expected**: [What acceptance criteria require]
**Actual**: [What actually happened]
**Evidence**: [Test output line / verification block excerpt]
**Fix instruction**: [Specific, actionable — which file, what to change]
**File(s) to modify**: [Exact paths]

### Issue 2: [Category] — Severity: [...]
[Same structure]

## Acceptance Criteria Status
- [x] [Criterion 1] — passed
- [ ] [Criterion 2] — FAILED (see Issue 1)

## Retry Instructions
**For @implementation-agent**:
1. Fix ONLY the issues listed above
2. Do NOT introduce new features or changes
3. Re-submit for Phase 5 QA when all issues are addressed
4. This is attempt [N] of 2 maximum

**If attempt 2 fails**: Tech Lead fills `escalation-report-template.md`

## Next Action
→ Tech Lead: re-dispatch @implementation-agent + @debugger with issue list above
```

---

## 3. Phase Gate Handoff

Use when transitioning between major workflow phases (e.g. Phase 3 → Phase 3.6 → Phase 4, or Phase 5 → Phase 6).

```markdown
# Phase Gate Handoff

## Transition
| Field | Value |
|-------|-------|
| **From Phase** | Phase [N] — [Name] |
| **To Phase** | Phase [N+1] — [Name] |
| **Gate owner** | Tech Lead |
| **Gate result** | PASSED / BLOCKED |
| **Timestamp** | [YYYY-MM-DDTHH:MM:SSZ] |

## Gate Criteria
| # | Criterion | Result | Evidence |
|---|-----------|--------|----------|
| 1 | [Criterion] | ✅ PASS / ❌ FAIL | [File path or output reference] |
| 2 | [Criterion] | ✅ PASS / ❌ FAIL | [Reference] |

## Artifacts Carried Forward
1. [File path] — [Purpose in next phase]
2. [File path] — [Purpose in next phase]

## Constraints for Next Phase
- [Constraint from this phase's findings]
- [Assumption that must be validated in next phase]

## Agent Activation
| Agent | Role in next phase | Priority |
|-------|--------------------|----------|
| [@agent] | [What it does] | Immediate / As needed |
```

---

## 4. Sprint / Task-Batch Handoff

Use at sprint boundaries or when handing off a batch of tasks to a new session.

```markdown
# Sprint / Task-Batch Handoff

## Summary
| Field | Value |
|-------|-------|
| **Sprint / Batch** | [ID or description] |
| **Period** | [Start] → [End] |
| **Goal** | [Goal statement] |

## Completion Status
| Task ID | Description | Status | QA Attempts | Notes |
|---------|-------------|--------|-------------|-------|
| [ID] | [Description] | ✅ Complete | [N] | — |
| [ID] | [Description] | ⚠️ Carried over | [N] | [Reason] |
| [ID] | [Description] | ❌ Escalated | [N] | [See escalation report] |

## Quality Metrics
- **Phase 5 first-pass rate**: [X]%
- **Average retries per task**: [N]
- **Tasks completed**: [X/Y]

## Carried Over
| Task ID | Description | Reason | Priority |
|---------|-------------|--------|----------|
| [ID] | [Description] | [Why not completed] | [High/Medium/Low] |

## Retrospective Insights
**What worked well**: [Key successes]
**What to improve**: [Key improvements for next sprint]
**Action items**: [Specific changes — agent config, plan quality, etc.]

## Next Sprint Preview
**Goal**: [Proposed goal]
**Key tasks**: [Top priority items]
**Open risks**: [Unresolved risks from this sprint]
```

