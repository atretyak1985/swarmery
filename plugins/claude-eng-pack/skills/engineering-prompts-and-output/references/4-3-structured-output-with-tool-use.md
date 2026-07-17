---
domain: 4 - Prompt Engineering & Structured Output
module: "4.3"
title: "Structured Output with Tool Use"
---

# 4.3 Structured Output with Tool Use

## Overview

When you need guaranteed schema-compliant structured output from Claude, there is a clear reliability hierarchy:

1. **`tool_use` with JSON schemas** — eliminates JSON syntax errors entirely
2. **Prompt-based JSON** — model can produce malformed JSON

Prefer `tool_use` with a JSON schema for any output you intend to parse programmatically. With tool use, the tool's JSON schema constrains the shape of what Claude returns, eliminating syntax issues like missing brackets, trailing commas, or unquoted keys. The separate `tool_choice` parameter is what forces the model to call the tool at all. Prompt-based extraction (asking the model to output JSON in a text response) provides no structural guarantees and will periodically produce unparseable output in production. When reviewing an implementation, check that anything downstream that expects parseable JSON is fed by a tool schema, not by free-text prompting.

### tool_choice: The Three Modes

The `tool_choice` parameter controls whether and how the model calls tools. There are three modes to distinguish when auditing a call site:

**`"auto"` (default):** The model decides whether to call a tool or return text. It may choose to respond with a text message instead of calling the extraction tool. Use this when the model legitimately needs the option to respond conversationally.

**`"any"`:** The model MUST call a tool but chooses which one. Use this when you have multiple extraction schemas (e.g., `extract_invoice`, `extract_receipt`, `extract_contract`) and the document type is unknown. The model selects the appropriate tool and returns structured output. Guaranteed structured output, flexible tool selection.

**`{"type": "tool", "name": "extract_metadata"}`:** The model MUST call the specific named tool. Use this to force a mandatory first step — for example, ensuring metadata extraction runs before enrichment steps. No flexibility, maximum control.

```typescript
// Force guaranteed structured output with unknown document type
const response = await client.messages.create({
  model: "claude-sonnet-5",
  max_tokens: 4096,
  tool_choice: { type: "any" },
  tools: [extractInvoiceTool, extractReceiptTool, extractContractTool],
  messages: [{ role: "user", content: documentText }]
});

// Force a specific extraction step
const response = await client.messages.create({
  model: "claude-sonnet-5",
  max_tokens: 4096,
  tool_choice: { type: "tool", name: "extract_metadata" },
  tools: [extractMetadataTool],
  messages: [{ role: "user", content: documentText }]
});
```

### What tool_use Does NOT Prevent

This is a subtle point that is easy to get wrong. `tool_use` with JSON schemas eliminates **syntax** errors but does NOT prevent **semantic** errors:

- **Sum discrepancies:** Line items that do not sum to the stated total
- **Field placement errors:** Values placed in the wrong fields (e.g., a date in an amount field when both are strings)
- **Fabrication:** The model invents values for required fields when the source document lacks the information

The schema guarantees structure. It does not guarantee correctness. When reviewing a pipeline that relies on tool output, check that semantic validation is handled by additional logic rather than assumed to come for free from the schema (see module 4.4 — Validation, Retry, and Feedback Loops).

### Schema Design for Production

Effective schema design prevents entire classes of errors at the structural level:

**Optional/nullable fields** — When source documents may not contain certain information, make those fields optional or nullable. This is the primary defence against fabrication. If a field is required, the model is pressured to produce a value even when the source has none. If the field is nullable, the model can honestly return `null`.

```json
{
  "type": "object",
  "properties": {
    "invoice_number": { "type": "string" },
    "vendor_name": { "type": "string" },
    "payment_terms": { "type": ["string", "null"] },
    "purchase_order": { "type": ["string", "null"] }
  },
  "required": ["invoice_number", "vendor_name"]
}
```

**"unclear" enum value** — For ambiguous cases where the source is genuinely unclear, add an explicit "unclear" option to enum fields. This prevents the model from forcing a classification when the evidence is ambiguous.

**"other" + detail string** — For extensible categorisation, include an "other" enum value paired with a freeform detail string field. This captures edge cases that your predefined categories do not cover.

```json
{
  "category": {
    "type": "string",
    "enum": ["invoice", "receipt", "contract", "unclear", "other"]
  },
  "category_detail": {
    "type": ["string", "null"],
    "description": "Freeform detail when category is 'other'"
  }
}
```

**Format normalisation rules** — Include format normalisation instructions in the prompt alongside the schema. The schema enforces structure; the prompt enforces formatting consistency (e.g., "All dates in ISO 8601 format," "All currency amounts as decimal numbers without currency symbols").

> **Key Concept**
> tool_use with JSON schemas eliminates syntax errors but not semantic errors. Make fields optional/nullable when source documents may lack information — this prevents the model from fabricating values. Use tool_choice "any" for guaranteed structured output when the document type is unknown.

## Audit Checklist

- [ ] Output that is parsed programmatically comes from a `tool_use` JSON schema, not from prompt-based free-text JSON.
- [ ] `tool_choice` is set explicitly per call site: `"any"` when structured output is required but the document type is unknown, `{"type": "tool", "name": ...}` to force a specific mandatory step, and `"auto"` only where a conversational text response is genuinely acceptable.
- [ ] Semantic validation (sum checks, field-placement checks, fabrication checks) is implemented as additional logic — the schema is not treated as a correctness guarantee.
- [ ] Fields that may be absent from source documents are declared optional or nullable rather than required, so the model can return `null` instead of fabricating a value.
- [ ] Enum fields that can be ambiguous include an explicit `"unclear"` option so the model is not forced into a classification.
- [ ] Extensible categorisation includes an `"other"` enum value paired with a freeform detail string field for edge cases outside predefined categories.
- [ ] Format normalisation rules (e.g. ISO 8601 dates, currency as decimals without symbols) are specified in the prompt, since the schema alone does not enforce formatting consistency.

## Sources

- [Tool Use (Function Calling)](https://platform.claude.com/docs/en/build-with-claude/tool-use) — Anthropic
