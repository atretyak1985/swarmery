---
domain: 4 - Prompt Engineering & Structured Output
module: "4.2"
title: "Few-Shot Prompting"
---

# 4.2 Few-Shot Prompting

## Overview

Few-shot examples are the most effective technique for achieving consistent, well-formatted output from Claude. Not more instructions. Not confidence thresholds. Not temperature adjustments. When your output is inconsistent, few-shot examples are the first tool to reach for.

When reviewing an implementation, watch for the failure pattern where detailed instructions produce inconsistent results and the team responds by piling on ever-more-detailed prose. That is pulling the wrong lever. The correct fix in this situation is almost always to add few-shot examples rather than to add more instructions.

### When to Deploy Few-Shot Examples

Three specific triggers tell you few-shot examples are needed:

**1. Detailed instructions alone produce inconsistent formatting.** You have written a thorough prompt specifying the output format, but the model produces different structures across invocations — sometimes a bulleted list, sometimes a table, sometimes prose. More instructions will not fix this. A few examples showing the exact format you want will.

**2. The model makes inconsistent judgement calls on ambiguous cases.** For a code review tool, the model flags variable shadowing as "critical" in one file and "minor" in another. For a tool selection agent, it routes "check my order" to different tools depending on phrasing. These ambiguous cases need examples demonstrating the correct judgement, with reasoning.

**3. Extraction tasks produce empty/null fields for information that exists in the document.** The information is present but in an unexpected format — embedded in narrative text rather than a structured table, or split across multiple paragraphs. Few-shot examples showing extraction from varied document structures resolve this.

### How to Construct Effective Examples

The construction rules are precise:

**Use 2-4 targeted examples.** Fewer than 2 does not establish a pattern. More than 4 wastes tokens without proportional benefit. Target your examples at the specific ambiguous scenarios causing problems.

**Each example must show reasoning.** Do not just show input-output pairs. Show why one action was chosen over plausible alternatives. This teaches the model to generalise its judgement to novel patterns, not just match the specific cases in your examples.

```
Example: Tool selection for "check my order #12345"
Input: "check my order #12345"
Selected tool: lookup_order
Reasoning: The user provides an order number (#12345), indicating
they want order-specific information. Even though this could be
interpreted as a general customer query, the specific order
identifier makes lookup_order the correct choice over get_customer.
```

Without the reasoning, the model learns only "queries mentioning order numbers go to lookup_order." With the reasoning, the model learns the general principle: specific identifiers route to specific lookup tools.

**Cover the failing scenarios.** If your extraction works on tables but fails on narrative text, your examples should show correct extraction from narrative text. If your code review is inconsistent on variable shadowing, your examples should classify variable shadowing scenarios at different severity levels with reasoning.

### The Hallucination Reduction Effect

Few-shot examples have a powerful secondary effect: they dramatically reduce hallucination in extraction tasks. When the model sees examples of correct extraction from varied document structures — inline citations vs bibliographies, narrative descriptions vs structured tables, headers vs embedded text — it learns to handle structural variety without inventing data.

This is particularly effective for documents with inconsistent formatting. A financial report might present expenses in a table on one page and in a narrative paragraph on the next. Without few-shot examples, the model may successfully extract from the table but return empty fields for the narrative section (or worse, fabricate values). With examples showing both structures, extraction quality improves significantly.

### Few-Shot for Reducing False Positives

In code review and analysis scenarios, few-shot examples serve a dual purpose: they demonstrate both what to flag and what to ignore. Examples that distinguish acceptable code patterns from genuine issues reduce false positives while maintaining detection of real problems.

```text
Example: Variable shadowing assessment
Code: function process(items) {
  const result = items.map(item => {
    const result = transform(item);  // shadows outer 'result'
    return result;
  });
  return result;
}
Severity: minor
Reasoning: The inner 'result' shadows the outer variable but
within a limited scope (arrow function). The code is still readable
and the shadow does not cause a bug. This is a style preference,
not a defect. Flag as minor only if style consistency is in scope.
```

This example teaches the model to distinguish genuine bugs from benign patterns, reducing false positives while preserving the ability to generalise to genuinely problematic shadowing cases.

> **Key Concept**
> Few-shot examples are the most effective technique for consistency. Use 2-4 targeted examples that include reasoning for decisions, not just input-output pairs. Deploy them when instructions alone produce inconsistent results, ambiguous judgements, or empty extraction fields for data that exists.

### Few-Shot vs Other Techniques

When reviewing an implementation, check that few-shot examples are being applied to the right class of problem. Some symptoms look like they call for examples but are better solved with another technique entirely:

| Problem | Correct Technique |
| --- | --- |
| Inconsistent output formatting | Few-shot examples |
| Malformed JSON output | tool_use with JSON schemas |
| Fabricated values for missing fields | Optional/nullable schema fields |
| Wrong tool selection | Better tool descriptions (first), then few-shot |
| Model misses information in narrative text | Few-shot examples showing narrative extraction |
| Extraction sum does not match total | Validation-retry loop |

## Audit Checklist

- [ ] Inconsistent output formatting is addressed with few-shot examples, not by adding more prose instructions.
- [ ] The prompt uses between 2 and 4 examples — fewer than 2 fails to establish a pattern, more than 4 wastes tokens without proportional benefit.
- [ ] Each example includes reasoning that explains why one action was chosen over plausible alternatives, not just an input-output pair.
- [ ] Examples target the specific ambiguous or failing scenarios (e.g. narrative-text extraction, borderline severity calls), not generic cases.
- [ ] Extraction prompts include examples spanning varied document structures (tables, narrative text, inline citations vs bibliographies) to reduce empty fields and fabricated values.
- [ ] Code-review and analysis examples demonstrate both what to flag and what to ignore, to control false positives.
- [ ] Malformed JSON output is handled with tool_use and JSON schemas rather than few-shot examples.
- [ ] Fabricated values for missing fields are handled with optional/nullable schema fields.
- [ ] Wrong tool selection is addressed first by improving tool descriptions, and only then with few-shot examples.
- [ ] Extraction totals that fail to reconcile are handled with a validation-retry loop, not by adding more examples.

## Sources

- Prompt Engineering Overview — Anthropic
- Building with Claude API — Anthropic
