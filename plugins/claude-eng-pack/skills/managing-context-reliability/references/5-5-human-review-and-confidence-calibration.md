---
domain: 5 - Context Management & Reliability
module: "5.5"
title: "Human Review & Confidence Calibration"
---

# 5.5 Human Review & Confidence Calibration

## Overview

Human review is the safety net for automated extraction and classification systems. The core design question is not whether to use human review, but how to allocate limited reviewer capacity to maximise accuracy while minimising cost. Getting this right requires understanding confidence calibration, the trap of aggregate metrics, and stratified sampling strategies. When reviewing an implementation, check that each of these mechanisms is present and correctly ordered before any automation decision is made.

### The Aggregate Metrics Trap

This is the most dangerous misconception in production extraction systems. A system reports 97% overall accuracy. The team celebrates. Management approves full automation for all high-confidence extractions.

The problem: that 97% hides catastrophic failure rates on specific document types. The system extracts dates from standard invoices at 99.5% accuracy. But handwritten receipts? 60%. Scanned PDFs with poor OCR? 72%. International documents with non-standard formatting? 45%.

The aggregate masks the segments where the system fails most. And those segments are often the ones where errors have the highest business impact — handwritten receipts from field staff, international invoices from new suppliers, scanned historical documents for compliance audits.

**The rule: always validate accuracy by document type AND field segment before automating.** Never make automation decisions based on aggregate metrics alone.

| Document Type | Date Accuracy | Amount Accuracy | Name Accuracy |
| --- | --- | --- | --- |
| Standard invoices | 99.5% | 98.2% | 97.8% |
| Handwritten receipts | 60.1% | 55.3% | 71.2% |
| Scanned PDFs | 72.4% | 69.8% | 80.1% |
| International formats | 45.2% | 52.1% | 63.4% |
| **Aggregate** | **97.0%** | **96.1%** | **95.8%** |

The aggregate looks excellent because standard invoices dominate the volume. But three document types have unacceptable accuracy, hidden by the volume-weighted average.

> **Common Mistake**
> Reading a single headline accuracy number and treating it as a green light for automation. A volume-weighted average is dominated by whichever segment has the most traffic, so a healthy aggregate can coexist with 40%–60% error rates on low-volume but high-impact segments. When auditing a system, demand the per-segment breakdown; if only an aggregate exists, treat the automation decision as unvalidated.

### Stratified Random Sampling

Even after validating by document type and field, you need ongoing verification. Stratified random sampling means selecting a representative sample from each stratum (document type, confidence band, field type) and having humans verify it.

The critical insight is that you must sample high-confidence extractions, not just low-confidence ones. Low-confidence items are already routed to human review. High-confidence items are automated. If the model develops a novel error pattern that affects high-confidence extractions, only stratified sampling will catch it.

Stratified sampling serves two purposes:

1. **Ongoing accuracy measurement** — confirm that each segment maintains its validated accuracy rate.
2. **Novel error pattern detection** — discover new failure modes that did not exist in the original validation set.

Without stratified sampling, you are flying blind on your automated extractions. The system could develop a systematic error on a new document format and you would not know until downstream business processes fail.

### Field-Level Confidence Calibration

The model can output confidence scores per field. For an invoice extraction, it might report:

```json
{
  "vendorName": {"value": "Acme Corp", "confidence": 0.98},
  "invoiceDate": {"value": "2024-03-15", "confidence": 0.95},
  "totalAmount": {"value": "$1,247.83", "confidence": 0.72},
  "lineItems": {"value": [...], "confidence": 0.61}
}
```

But raw model confidence scores are not calibrated. A model that reports 0.95 confidence might actually be correct 88% of the time on certain field types. Or 99% of the time on others. The confidence score is relative, not absolute.

**Calibration requires labelled validation sets (ground truth data).** You take a set of documents with known correct extractions, run the model, compare its confidence scores to actual accuracy, and build a calibration curve. This tells you: "When the model reports 0.90 confidence on date fields, it is actually correct 94% of the time. When it reports 0.90 on amount fields, it is actually correct 82% of the time."

Calibrated thresholds then drive routing:

- Fields above the calibrated threshold → automated (with stratified sampling)
- Fields below the calibrated threshold → human review
- Fields in the ambiguous zone → prioritised human review

> **Common Mistake**
> Using raw model confidence scores directly as routing thresholds. A raw 0.95 is not a promise of 95% accuracy — it is an uncalibrated, field-relative signal. Routing on uncalibrated scores over-automates the fields where the model is overconfident and over-reviews the fields where it is underconfident. Check that thresholds are derived from a calibration curve built against labelled ground truth, per field type, not lifted straight from the model output.

### Reviewer Capacity Prioritisation

Human reviewers are expensive and limited, so how their capacity is allocated is a first-order design decision.

**Route the highest-uncertainty items to reviewers first.** This means:

- Low model confidence fields
- Extractions from ambiguous or contradictory source documents
- Document types with historically poor accuracy
- Fields where the model expresses uncertainty (e.g., multiple possible interpretations)

Do NOT spread reviewer capacity evenly across all extractions. An even distribution wastes time reviewing high-confidence items that the model handles well while leaving insufficient capacity for the uncertain items that actually need human judgement.

The prioritisation should be dynamic, not static. As the system processes documents, the queue of items awaiting human review should be ordered by uncertainty. When a reviewer finishes one item, the next item in their queue should be the highest-uncertainty item remaining, not simply the next in chronological order.

### Validation Before Automation

The sequence matters:

1. **Measure accuracy by document type and field segment** — not aggregate.
2. **Calibrate confidence scores** using labelled validation sets.
3. **Set calibrated thresholds** for automation versus human review.
4. **Implement stratified random sampling** for ongoing verification of automated extractions.
5. **Only then reduce human review** on segments that demonstrate consistent, validated accuracy.

Skipping to step 5 based on aggregate metrics is the trap. Every step in this sequence exists to prevent a specific failure mode.

> **Key Concept**
> 97% aggregate accuracy can hide 40% error rates on specific document types. Validate accuracy by document type AND field segment. Calibrate confidence scores using labelled validation sets. Sample high-confidence extractions through stratified sampling. Prioritise limited reviewer capacity on the highest-uncertainty items.

## Audit Checklist

- [ ] Automation decisions are gated on per-segment accuracy (document type AND field), never on an aggregate/headline number.
- [ ] A per-document-type and per-field accuracy breakdown exists, and any segment below the acceptable threshold is excluded from automation.
- [ ] Confidence thresholds are derived from a calibration curve built against labelled ground truth, per field type, not from raw model confidence.
- [ ] Routing sends fields above the calibrated threshold to automation, below it to human review, and ambiguous-zone fields to prioritised human review.
- [ ] Stratified random sampling covers high-confidence (automated) extractions, not just low-confidence ones, so novel error patterns in automated output are detectable.
- [ ] Sampling strata span document type, confidence band, and field type, and run on an ongoing basis rather than only at initial validation.
- [ ] Reviewer capacity is prioritised toward the highest-uncertainty items rather than spread evenly across all extractions.
- [ ] The review queue is ordered dynamically by uncertainty, so a freed reviewer picks up the highest-uncertainty item remaining, not the next chronological one.
- [ ] Human review is reduced only after steps 1–4 (segment measurement, calibration, calibrated thresholds, stratified sampling) are demonstrably in place.

## Sources

- [Anthropic Structured Data Extraction Guide](https://code.claude.com/docs/en/agent-sdk/structured-outputs) — Anthropic
- [Anthropic Human-in-the-Loop Patterns](https://code.claude.com/docs/en/agent-sdk/user-input) — Anthropic
