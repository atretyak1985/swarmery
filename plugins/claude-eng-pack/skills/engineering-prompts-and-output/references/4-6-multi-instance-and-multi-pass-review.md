---
domain: 4 - Prompt Engineering & Structured Output
module: "4.6"
title: "Multi-Instance and Multi-Pass Review"
---

# 4.6 Multi-Instance and Multi-Pass Review

## Overview

When Claude reviews its own output, it has a structural disadvantage: it retains the reasoning context from generation. The model remembers why it made each decision and is less likely to question those decisions. This is not a bug — it is an inherent property of self-review within the same session. When reviewing an implementation, check that it understands this limitation and designs around it.

### The Self-Review Limitation

A model reviewing its own output in the same conversation session retains its original reasoning chain. It already "knows" why it chose each approach, classified each finding at a particular severity, or selected certain values. When asked to review, it tends to confirm rather than challenge those decisions.

An **independent instance** — a separate Claude invocation without the prior reasoning context — approaches the output fresh. It evaluates the code, findings, or extraction based solely on what it sees, without the bias of "I chose this because..." This makes independent review significantly more effective at catching subtle issues.

Prefer a separate model instance for review. Adding "please review carefully" instructions to the same session, or relying on extended thinking within the generating session, does not overcome the self-confirmation bias — the reasoning context is still present. Use a fresh instance instead.

```typescript
// Anti-pattern: self-review in the same session
const generation = await client.messages.create({
  messages: [
    { role: "user", content: "Write a function to process orders" },
    { role: "assistant", content: generatedCode },
    { role: "user", content: "Now review your code for bugs" }
    // Model retains its reasoning — less likely to find its own mistakes
  ]
});

// Correct: independent review instance
const review = await client.messages.create({
  messages: [
    {
      role: "user",
      content: `Review this code for bugs, security issues, and edge cases:\n\n${generatedCode}`
    }
    // Fresh instance — no prior reasoning context
  ]
});
```

### Multi-Pass Review Architecture

Large reviews (multi-file PRs, complex extraction pipelines, broad code audits) suffer from **attention dilution** when processed in a single pass. The symptoms are specific and recognisable:

- Detailed feedback on some files, superficial comments on others
- Obvious bugs missed in the middle of the review
- Contradictory findings — flagging a pattern as problematic in one file while approving identical code elsewhere

The fix is to split the review into focused passes:

**Pass 1: Per-file local analysis.** Analyse each file individually with a focused review prompt. This ensures consistent depth across all files. Each invocation examines only one file, so the model gives it full attention.

**Pass 2: Cross-file integration.** After all per-file analyses are complete, run a separate pass that receives all per-file findings and checks for cross-file issues: data flow between modules, consistent API usage across services, dependency conflicts, and contradictions in the per-file findings themselves.

```typescript
// Pass 1: Per-file analysis
const perFileFindings = await Promise.all(
  files.map(file =>
    client.messages.create({
      messages: [{
        role: "user",
        content: `Review this file for local issues (bugs, security, logic errors):\n\n${file.content}`
      }]
    })
  )
);

// Pass 2: Cross-file integration
const integrationReview = await client.messages.create({
  messages: [{
    role: "user",
    content: `Given these per-file findings, identify cross-file issues:\n` +
      `- Data flow inconsistencies between modules\n` +
      `- Contradictory patterns flagged in different files\n` +
      `- API contract violations across service boundaries\n\n` +
      `Findings:\n${JSON.stringify(perFileFindings)}`
  }]
});
```

This architecture directly addresses the three symptoms of attention dilution. Per-file passes ensure consistent depth. The integration pass catches cross-file issues that no single-file review would identify. And the separation prevents contradictory findings from appearing in the same output.

### Why Larger Context Windows Do Not Fix This

A tempting response to a review that cannot handle 14 files at once is to give the model more capacity: switch to a higher-tier model with a larger context window. But the problem is not context size. It is attention quality. A larger context window does not prevent the model from giving uneven attention across files. Only focused, per-file passes ensure consistent depth.

> **Common Mistake**
> A larger context window looks like a plausible fix for attention dilution — reject it. Larger windows address capacity limits (output that does not fit at all), not attention quality across a batch of files. If depth is uneven across files, the remedy is focused per-file passes, not a bigger model or window.

### Confidence-Based Routing

For findings that are uncertain, the model can self-report confidence alongside each finding. This enables a routing strategy:

- **High confidence findings:** Report directly to developers
- **Low confidence findings:** Route to human review for validation
- **Threshold calibration:** Use labelled validation sets to calibrate what confidence score correlates with actual accuracy

```json
{
  "finding": "Potential race condition in order processing",
  "severity": "major",
  "confidence": 0.65,
  "reasoning": "The lock acquisition pattern appears correct but the unlock timing depends on an async callback whose ordering I cannot fully verify.",
  "route": "human_review"
}
```

The confidence score is not self-reported accuracy — it is the model's assessment of its own certainty. Calibrate it by running labelled examples (where you know the correct answer) through the system and measuring the relationship between reported confidence and actual accuracy. Adjust routing thresholds based on this calibration data.

Distinguish between raw confidence scores (uncalibrated, unreliable for automated decisions) and calibrated confidence thresholds (validated against labelled sets, suitable for routing). Using uncalibrated confidence for automated decisions is an anti-pattern.

> **Key Concept**
> A model reviewing its own output in the same session retains reasoning context and is less likely to question its decisions. Use independent instances for review. Split large reviews into per-file local passes plus a cross-file integration pass to prevent attention dilution. Calibrate confidence thresholds using labelled validation sets before using them for routing.

### Putting It All Together

A production review architecture combines all three concepts:

1. **Generation:** First instance generates code, extraction, or analysis
2. **Per-file review:** Independent instances review each output unit individually
3. **Integration review:** Separate instance checks cross-unit consistency
4. **Confidence routing:** Low-confidence findings go to human review
5. **Calibration loop:** Labelled validation sets continuously calibrate confidence thresholds

This architecture is more expensive than single-pass review. The trade-off is worth it when review quality directly affects production reliability — CI/CD pipelines, financial extraction, compliance analysis, and any system where missed issues have downstream consequences.

## Audit Checklist

- [ ] Review runs in an independent Claude instance, not as a follow-up turn in the same session that generated the output
- [ ] Review quality is not delegated to "please review carefully" instructions or extended thinking inside the generating session
- [ ] Large multi-file reviews are split into per-file local passes rather than processed in a single pass
- [ ] A separate cross-file integration pass runs after the per-file passes to catch data-flow, API-contract, and contradiction issues
- [ ] Attention dilution is not "fixed" by switching to a higher-tier model or larger context window
- [ ] Findings carry a self-reported confidence score, and low-confidence findings are routed to human review
- [ ] Confidence thresholds are calibrated against labelled validation sets before being used for automated routing
- [ ] Uncalibrated raw confidence scores are never used to drive automated decisions

## Sources

- [Prompt Engineering Overview](https://platform.claude.com/docs/en/build-with-claude/prompt-engineering/overview) — Anthropic
