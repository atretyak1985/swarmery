---
domain: 2 - Tool Design & MCP Integration
module: "2.1"
title: "Tool Interface Design"
---

# 2.1 Tool Interface Design

## Overview

Tool descriptions are the PRIMARY mechanism LLMs use for tool selection. This is not supplementary metadata. It is not an afterthought. It is THE mechanism. When a model receives a set of tools, it reads the descriptions to decide which tool to call. If those descriptions are minimal — something like "Retrieves customer information" — the model lacks the context to differentiate between tools that serve overlapping purposes.

### What Makes a Good Tool Description

A production-grade tool description includes five elements:

1. **What the tool does** — its primary purpose, stated unambiguously
2. **What inputs it expects** — data types, formats, constraints, and required versus optional fields
3. **Example queries it handles well** — concrete use cases that anchor the model's understanding
4. **Edge cases and limitations** — what the tool does NOT do, and what happens when inputs fall outside expected ranges
5. **Explicit boundaries** — when to use THIS tool versus similar tools in the same toolkit

Here is the difference between a minimal and a production-grade description:

**Minimal (causes misrouting):**

```
get_customer: "Retrieves customer information"
lookup_order: "Retrieves order details"
```

**Production-grade (reliable selection):**

```
get_customer: "Looks up a customer account by email address,
phone number, or customer ID. Returns customer profile
(name, contact details, account status, loyalty tier).
Use this when you need to verify who the customer is.
Do NOT use for order-specific queries — use lookup_order
for those."
```

```
lookup_order: "Retrieves order details by order number
(format: #NNNNN) or tracking ID. Returns order status,
items, shipping details, and refund eligibility.
Use this when a customer asks about a specific order.
Do NOT use for customer identity verification —
use get_customer for that."
```

The second version gives the model explicit disambiguation. It knows which identifiers each tool accepts, what each returns, and crucially, when NOT to use each tool.

### The Misrouting Problem

Two tools with overlapping or near-identical descriptions cause selection confusion. A classic example: `get_customer` and `lookup_order` with minimal descriptions, causing the agent to route "check my order #12345" to the wrong tool.

When reviewing an implementation, check that the chosen fix addresses the root cause. Four options look plausible here, but only one is correct as a first step:

- **Expand descriptions** — correct. Low effort, high leverage, directly addresses the root cause.
- **Few-shot examples** — wrong. Adds token overhead without fixing why the model is confused. You are treating symptoms, not the disease.
- **Routing classifier** — wrong. Over-engineered as a first step. Bypasses the LLM's natural language understanding and adds infrastructure complexity.
- **Tool consolidation** — wrong as a first step. It is a valid architectural choice long-term, but requires significantly more effort than expanding descriptions.

> **Common Mistake**
> A routing classifier or few-shot examples look like plausible fixes for misrouting — reject them as the first move. They address symptoms while leaving the ambiguous descriptions (the actual cause) in place, and they add token and infrastructure overhead. Prefer low-effort, high-leverage fixes: better descriptions before routing classifiers, scoped access before full access, community servers before custom builds.

### Tool Splitting

Generic tools with broad responsibilities create ambiguity. The fix is to split them into purpose-specific tools with defined input/output contracts.

**Before splitting:**

```
analyze_document: "Analyses a document and returns results"
```

**After splitting:**

```
extract_data_points: "Extracts structured data fields
(dates, amounts, names) from a document"

summarize_content: "Produces a concise summary of a
document's key arguments and conclusions"

verify_claim_against_source: "Checks whether a specific
claim is supported by the source document, returning
supporting/contradicting evidence"
```

Each resulting tool has a narrow, clearly described purpose. The model can select the right one based on what the user actually needs.

### Tool Renaming for Clarity

When two tools have confusingly similar names, renaming eliminates functional overlap at the interface level. For example, renaming `analyze_content` to `extract_web_results` with a web-specific description makes the tool's purpose unambiguous without changing its implementation.

### System Prompt Interactions

Keyword-sensitive instructions in system prompts can create unintended tool associations that override well-written descriptions. If your system prompt says "always check customer details before proceeding", the model may associate any customer-related query with `get_customer` regardless of what the tool descriptions say.

Always review system prompts for conflicts after updating tool descriptions. This is a subtle failure mode to check for when auditing an implementation.

> **Key Concept**
> Tool descriptions are the primary mechanism LLMs use for tool selection. When misrouting occurs, the first fix is always to improve descriptions — not to add few-shot examples, routing classifiers, or tool consolidation.

## Audit Checklist

- [ ] Every tool description covers all five elements: what it does, expected inputs (types, formats, constraints, required vs optional), example queries, edge cases and limitations, and explicit boundaries versus similar tools
- [ ] No two tools carry overlapping or near-identical descriptions that could cause selection confusion
- [ ] Each description states when NOT to use the tool and names the sibling tool to use instead
- [ ] Generic, broad-responsibility tools are split into purpose-specific tools with defined input/output contracts
- [ ] Confusingly similar tool names are renamed to eliminate functional overlap at the interface level
- [ ] Misrouting is addressed first by improving descriptions, not by adding few-shot examples, a routing classifier, or tool consolidation
- [ ] System prompts are reviewed for keyword-sensitive instructions that could override tool descriptions and create unintended tool associations
- [ ] Fixes for tool-selection issues favour low-effort, high-leverage changes before infrastructure (better descriptions before routing classifiers)

## Sources

- [Tool use — Anthropic API Documentation](https://platform.claude.com/docs/en/build-with-claude/tool-use) — Anthropic
- [Model Context Protocol Specification — Tools](https://modelcontextprotocol.io/docs/concepts/tools) — Model Context Protocol
