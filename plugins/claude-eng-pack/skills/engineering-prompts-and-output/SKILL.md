---
name: engineering-prompts-and-output
description: Use when writing or reviewing system prompts, few-shot examples, structured output via tool use, JSON schema validation with retry loops, Message Batches API processing, or multi-instance/multi-pass review pipelines such as automated PR reviewers. Also when outputs are inconsistent between runs, JSON fails validation, false positives erode reviewer trust, hallucinated fields appear, or self-review keeps missing defects.
---

# Engineering Prompts and Output

## Overview

Best-practice reference for the prompt-and-output layer: explicit criteria in system prompts, few-shot construction, schema-enforced output, validation/retry, batch processing, and review pipelines. Each reference module states the correct pattern, the named anti-patterns, and ends with an **Audit Checklist** of verifiable conditions.

## When to use

- Writing or reviewing a system prompt, few-shot set, output schema, or an automated review pipeline
- Debugging: inconsistent verdicts, invalid JSON, false-positive floods, hallucinated values, missed defects on self-review
- Deciding: severity criteria vs confidence filtering, `tool_choice` mode for extraction, retry strategy, batch vs real-time

Not for tool interface design itself (use designing-tools-and-mcp) or human-review calibration (use managing-context-reliability).

## Quick reference

| Module | Read when the question is about |
|---|---|
| `references/4-1-system-prompts-with-explicit-criteria.md` | Explicit severity criteria, the false-positive trust problem |
| `references/4-2-few-shot-prompting.md` | Constructing examples, hallucination reduction, false-positive control |
| `references/4-3-structured-output-with-tool-use.md` | The three `tool_choice` modes, what `tool_use` does not guarantee, schema design |
| `references/4-4-validation-retry-and-feedback-loops.md` | Retry-with-error-feedback, its limits, schema vs semantic errors |
| `references/4-5-batch-processing-strategies.md` | Message Batches API, result matching, SLA maths, failure handling |
| `references/4-6-multi-instance-and-multi-pass-review.md` | Why self-review fails, multi-pass architecture, confidence-based routing |

## How to audit

1. Match the pipeline under review to modules in the table; read those files.
2. Run each module's **Audit Checklist** item by item against the actual prompts/schemas/code.
3. Report every unchecked item as a finding with `file:line` evidence and the module's recommended fix.

For a fast full-domain sweep, `checklists.md` aggregates all six checklists.

## Related

Sampling and calibrating against human review: managing-context-reliability (`5-5`). Orchestrating multiple reviewer instances: auditing-agent-architecture (`1-2`, `1-3`).
