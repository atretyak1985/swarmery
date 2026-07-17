---
domain: 4 - Prompt Engineering & Structured Output
module: "4.1"
title: "System Prompts with Explicit Criteria"
---

# 4.1 System Prompts with Explicit Criteria

## Overview

The single biggest mistake in production prompt engineering is relying on vague instructions. Phrases like "be conservative," "only report high-confidence findings," or "use your best judgement" give the model no actionable decision boundary. They sound reasonable — which is exactly why they slip into production prompts unnoticed and survive review unchallenged. When auditing a system prompt, treat this kind of language as a red flag.

The correct approach is **explicit categorical criteria** that define precisely what the model should flag and what it should skip. Compare these two system prompts for a CI/CD code review pipeline:

**Wrong approach:**

```
Review this code. Be conservative. Only report high-confidence findings.
```

**Correct approach:**

```
Flag comments only when claimed behaviour contradicts actual code behaviour.
Report bugs and security vulnerabilities.
Skip minor style preferences and local patterns.
```

The first gives the model no criteria to apply. "Conservative" means different things in different contexts, and "high-confidence" is a subjective threshold the model cannot calibrate. The second provides concrete categories: what to report (bugs, security), what to skip (style, local patterns), and a specific trigger for comment flags (claimed vs actual behaviour contradiction).

### The False Positive Trust Problem

High false positive rates in one category destroy developer trust in **all** categories. This is a critical, often-overlooked insight. If your "documentation mismatch" findings are wrong 40% of the time, developers stop reading your "security vulnerability" findings too — even if those are 98% accurate. Trust is not category-specific; it bleeds across the entire output.

The fix is counterintuitive but effective: **temporarily disable high false-positive categories** while you improve the prompts for those categories. This immediately restores trust in the categories that are working well. You then iterate on the problematic category's criteria with concrete code examples, re-enabling it only once precision improves.

This is not abandoning the category — it is prioritising system-wide trust over category completeness.

### Severity Calibration with Code Examples

Defining severity levels requires **concrete code examples**, not prose descriptions. Compare:

**Prose description (insufficient):**

```
Critical: Issues that could cause system failures or data loss
Minor: Issues that affect code readability but not functionality
```

**Code example approach (correct):**

```
Critical — Unsanitised user input in SQL query:
  query = f"SELECT * FROM users WHERE id = {user_input}"

Minor — Inconsistent variable naming:
  userName vs user_name in the same module
```

The prose description forces the model to interpret what "could cause system failures" means. The code example removes ambiguity entirely. When the model sees actual code patterns classified at each severity level, it produces consistent classification across invocations.

> **Key Concept**
> Explicit categorical criteria always outperform vague instructions. Define what to flag (bugs, security vulnerabilities) and what to skip (style preferences, local patterns) using concrete code examples for each severity level. Never rely on "be conservative" or confidence-based filtering.

### Why Confidence-Based Filtering Fails

"Only report high-confidence findings" is a tempting fix that shows up often in real prompts. It sounds like good engineering — filter by confidence, keep only the strong signals.

> **Common Mistake**
> Confidence-based filtering looks like a plausible way to cut false positives — reject it as a substitute for criteria. LLM self-reported confidence is poorly calibrated: the model is often highly confident about incorrect findings and uncertain about correct ones. Confidence scores are useful for routing (sending low-confidence findings to human review, see module 4.6 (Multi-Instance and Multi-Pass Review)), but they are not a substitute for explicit criteria that define what constitutes a valid finding in the first place.

The hierarchy is: **explicit criteria first**, confidence-based routing second. Never skip the first step.

## Audit Checklist

- [ ] System prompts define explicit categorical criteria — what to flag, what to skip — rather than vague instructions like "be conservative" or "use your best judgement".
- [ ] Comment or documentation flags trigger on a concrete condition (e.g. claimed behaviour contradicts actual code behaviour), not subjective judgement.
- [ ] Severity levels are defined with concrete code examples, not prose descriptions like "could cause system failures".
- [ ] No category relies on the model's self-reported confidence as the primary filter for what counts as a valid finding.
- [ ] High false-positive categories are temporarily disabled while their criteria are improved, rather than left running and eroding trust in accurate categories.
- [ ] Confidence scores, where used, drive routing to human review only — applied after explicit criteria, never instead of them.
- [ ] Explicit criteria come first in the pipeline; confidence-based routing comes second.

## Sources

- [Prompt Engineering Overview](https://platform.claude.com/docs/en/build-with-claude/prompt-engineering/overview) — Anthropic
