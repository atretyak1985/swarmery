---
domain: 4 - Prompt Engineering & Structured Output
module: "4.4"
title: "Validation, Retry, and Feedback Loops"
---

# 4.4 Validation, Retry, and Feedback Loops

## Overview

Production extraction systems fail. Documents have unexpected formats, numerical values do not add up, and fields end up in the wrong places. The question is not whether failures occur but how your system responds to them. This module covers the validation-retry pattern that turns extraction failures into self-correcting workflows.

### Retry-with-Error-Feedback

The correct retry pattern sends three pieces of information back to the model:

1. **The original document** — so the model has the source to re-examine
2. **The failed extraction** — so the model can see what it produced
3. **The specific validation error** — so the model knows exactly what went wrong

```typescript
// Retry with error feedback
const retryMessages = [
  {
    role: "user",
    content: `Original document:\n${originalDocument}\n\n` +
      `Your extraction:\n${JSON.stringify(failedExtraction)}\n\n` +
      `Validation error: Line items sum to £450 but stated_total is £500. ` +
      `Please re-extract, ensuring all line items are captured.`
  }
];
```

This is dramatically more effective than naive retries. Without the specific error, the model has no guidance for what to fix and typically produces the same mistake. With the error, the model can target its self-correction — re-examining the document for missed line items, checking field placement, or recalculating totals. When reviewing an implementation, confirm the retry actually feeds the validation error back; a retry that resends the same prompt without the error will usually reproduce the same failure.

### The Retry Effectiveness Boundary

The most important concept in this pattern is that retries have a clear effectiveness boundary:

**Retries ARE effective for:**

- Format mismatches (wrong date format, inconsistent currency notation)
- Structural output errors (values in wrong fields, incorrect nesting)
- Misplaced values (data that exists in the document but was extracted into the wrong field)
- Mathematical errors (the model missed a line item affecting the total)

**Retries are NOT effective for:**

- Information genuinely absent from the source document
- Data that exists only in an external document not provided to the model
- Fields requiring knowledge the model does not have

When reviewing an implementation, check that it distinguishes these two scenarios before retrying. If a document genuinely does not contain a department name, no amount of retrying will produce a correct value. The correct action is to flag the extraction for human review or return null (if the schema allows it).

> **Common Mistake**
> An unbounded retry loop looks like a reasonable robustness measure, but it does not address absent-information failures — retrying a field that the source document simply does not contain burns tokens and latency without ever converging. Retries fix fixable errors; missing data must be routed to human review or returned as null.

### Self-Correction Flow Design

Rather than relying solely on external validation logic, you can build self-correction into the extraction schema itself:

**calculated_total vs stated_total:** Extract both the sum the model calculates from individual line items and the total stated in the document. When these differ, you have an automatic discrepancy flag without external logic.

```json
{
  "line_items": [
    { "description": "Widget A", "amount": 150.00 },
    { "description": "Widget B", "amount": 300.00 }
  ],
  "calculated_total": 450.00,
  "stated_total": 500.00,
  "total_discrepancy": true
}
```

**conflict_detected booleans:** Add boolean fields that flag when the source document contains contradictory information. For example, if a document states "payment due: 30 days" in one section but "payment terms: net 60" in another, the model should extract both and set `conflict_detected: true` rather than silently picking one.

### detected_pattern Fields

For code review and analysis pipelines, add `detected_pattern` fields to structured findings. This tracks which specific code construct triggered each finding.

```json
{
  "finding": "Potential SQL injection vulnerability",
  "severity": "critical",
  "detected_pattern": "string concatenation in SQL query",
  "file": "user_service.py",
  "line": 42
}
```

When developers dismiss findings, you can analyse dismissal patterns by `detected_pattern`. If developers consistently dismiss findings triggered by "variable shadowing in nested scope," that pattern likely needs prompt refinement. This creates a systematic improvement loop: extract, validate, collect dismissal data, refine prompts, repeat.

### Schema Syntax Errors vs Semantic Validation Errors

There are two distinct error categories to distinguish:

**Schema syntax errors** — Malformed JSON, missing required fields, wrong data types. **Eliminated entirely** by `tool_use` with JSON schemas (see module 4.3, Structured Output with Tool Use).

**Semantic validation errors** — Correct JSON structure but incorrect values. Line items that do not sum, dates that precede each other incorrectly, values in wrong fields. These require **validation logic** outside the schema and are the focus of retry loops.

The overlap between these two concerns is intentional: `tool_use` solves the first category but not the second. When auditing a pipeline, confirm that structured output is not being relied on to catch semantic errors it cannot detect — those still need explicit validation logic.

> **Key Concept**
> Retry-with-error-feedback works by sending the original document, the failed extraction, and the specific validation error. Retries fix format and structural errors but cannot create information absent from the source document. Always identify whether a failure is fixable before retrying.

## Audit Checklist

- [ ] Retry requests include all three inputs: the original document, the failed extraction, and the specific validation error — not a naive resend of the same prompt.
- [ ] The system classifies each failure as fixable (format, structural, misplaced value, math error) or unfixable (information absent from the source) before retrying.
- [ ] Unfixable failures are routed to human review or returned as null (where the schema allows), not fed into an unbounded retry loop.
- [ ] The extraction schema captures both `calculated_total` and `stated_total` so total discrepancies flag automatically without external logic.
- [ ] `conflict_detected` (or equivalent) booleans are set when the source contains contradictory information, rather than the model silently picking one value.
- [ ] Findings carry `detected_pattern` fields so dismissal data can be analysed per pattern and used to refine prompts.
- [ ] `tool_use` with JSON schemas handles schema syntax errors; semantic validation is implemented as separate logic and is not assumed to be covered by structured output.
- [ ] A closed improvement loop exists: extract, validate, collect dismissal data, refine prompts, repeat.

## Sources

- [Tool Use (Function Calling)](https://platform.claude.com/docs/en/build-with-claude/tool-use) — Anthropic
