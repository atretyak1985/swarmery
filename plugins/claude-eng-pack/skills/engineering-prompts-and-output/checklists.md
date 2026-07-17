# Aggregated Audit Checklists — Prompt Engineering & Structured Output

Run these against the implementation under review. For the reasoning behind any item, read the full module file.

## 4.1 System Prompts with Explicit Criteria
Full module: `references/4-1-system-prompts-with-explicit-criteria.md`

- [ ] System prompts define explicit categorical criteria — what to flag, what to skip — rather than vague instructions like "be conservative" or "use your best judgement".
- [ ] Comment or documentation flags trigger on a concrete condition (e.g. claimed behaviour contradicts actual code behaviour), not subjective judgement.
- [ ] Severity levels are defined with concrete code examples, not prose descriptions like "could cause system failures".
- [ ] No category relies on the model's self-reported confidence as the primary filter for what counts as a valid finding.
- [ ] High false-positive categories are temporarily disabled while their criteria are improved, rather than left running and eroding trust in accurate categories.
- [ ] Confidence scores, where used, drive routing to human review only — applied after explicit criteria, never instead of them.
- [ ] Explicit criteria come first in the pipeline; confidence-based routing comes second.

## 4.2 Few-Shot Prompting
Full module: `references/4-2-few-shot-prompting.md`

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

## 4.3 Structured Output with Tool Use
Full module: `references/4-3-structured-output-with-tool-use.md`

- [ ] Output that is parsed programmatically comes from a `tool_use` JSON schema, not from prompt-based free-text JSON.
- [ ] `tool_choice` is set explicitly per call site: `"any"` when structured output is required but the document type is unknown, `{"type": "tool", "name": ...}` to force a specific mandatory step, and `"auto"` only where a conversational text response is genuinely acceptable.
- [ ] Semantic validation (sum checks, field-placement checks, fabrication checks) is implemented as additional logic — the schema is not treated as a correctness guarantee.
- [ ] Fields that may be absent from source documents are declared optional or nullable rather than required, so the model can return `null` instead of fabricating a value.
- [ ] Enum fields that can be ambiguous include an explicit `"unclear"` option so the model is not forced into a classification.
- [ ] Extensible categorisation includes an `"other"` enum value paired with a freeform detail string field for edge cases outside predefined categories.
- [ ] Format normalisation rules (e.g. ISO 8601 dates, currency as decimals without symbols) are specified in the prompt, since the schema alone does not enforce formatting consistency.

## 4.4 Validation, Retry, and Feedback Loops
Full module: `references/4-4-validation-retry-and-feedback-loops.md`

- [ ] Retry requests include all three inputs: the original document, the failed extraction, and the specific validation error — not a naive resend of the same prompt.
- [ ] The system classifies each failure as fixable (format, structural, misplaced value, math error) or unfixable (information absent from the source) before retrying.
- [ ] Unfixable failures are routed to human review or returned as null (where the schema allows), not fed into an unbounded retry loop.
- [ ] The extraction schema captures both `calculated_total` and `stated_total` so total discrepancies flag automatically without external logic.
- [ ] `conflict_detected` (or equivalent) booleans are set when the source contains contradictory information, rather than the model silently picking one value.
- [ ] Findings carry `detected_pattern` fields so dismissal data can be analysed per pattern and used to refine prompts.
- [ ] `tool_use` with JSON schemas handles schema syntax errors; semantic validation is implemented as separate logic and is not assumed to be covered by structured output.
- [ ] A closed improvement loop exists: extract, validate, collect dismissal data, refine prompts, repeat.

## 4.5 Batch Processing Strategies
Full module: `references/4-5-batch-processing-strategies.md`

- [ ] Blocking workflows (pre-merge checks, real-time review feedback) run on the synchronous API, never on batch.
- [ ] Batch is reserved for latency-tolerant workflows only (overnight reports, weekly audits, nightly runs, bulk document extraction).
- [ ] Batch scheduling accounts for the up-to-24-hour processing window and works backwards from the SLA (e.g. final batch submitted at least 24 hours before the deadline, with buffer for collection and validation).
- [ ] Every batch request carries a unique `custom_id` used to correlate requests with their responses.
- [ ] Failure handling identifies failures by `custom_id` and resubmits only the failed items with targeted modifications — not the whole batch.
- [ ] Prompts are refined against a representative 5-10 document sample set before the full batch is submitted.
- [ ] No batch item depends on multi-turn tool calling or agentic loops; any step needing tool execution mid-processing runs on the synchronous API.

## 4.6 Multi-Instance and Multi-Pass Review
Full module: `references/4-6-multi-instance-and-multi-pass-review.md`

- [ ] Review runs in an independent Claude instance, not as a follow-up turn in the same session that generated the output
- [ ] Review quality is not delegated to "please review carefully" instructions or extended thinking inside the generating session
- [ ] Large multi-file reviews are split into per-file local passes rather than processed in a single pass
- [ ] A separate cross-file integration pass runs after the per-file passes to catch data-flow, API-contract, and contradiction issues
- [ ] Attention dilution is not "fixed" by switching to a higher-tier model or larger context window
- [ ] Findings carry a self-reported confidence score, and low-confidence findings are routed to human review
- [ ] Confidence thresholds are calibrated against labelled validation sets before being used for automated routing
- [ ] Uncalibrated raw confidence scores are never used to drive automated decisions
