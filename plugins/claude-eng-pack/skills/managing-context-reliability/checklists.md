# Aggregated Audit Checklists — Context Management & Reliability

Run these against the implementation under review. For the reasoning behind any item, read the full module file.

## 5.1 Context Window Management
Full module: `references/5-1-context-window-management.md`

- [ ] Transactional facts (amounts, dates, order numbers, statuses) are extracted into a persistent case facts block that is included in every prompt and never summarised.
- [ ] Multi-issue sessions persist each issue as its own structured entry, preventing cross-contamination between issues during summarisation.
- [ ] Aggregated inputs lead with a "Key Findings Summary" and use explicit section headers, so critical facts are not buried in the middle where the model may miss them.
- [ ] The "lost in the middle" fix is structural (input layout), not a prompt instruction asking the model to attend to the middle.
- [ ] Verbose tool results are trimmed to only the relevant fields before entering conversation history, ideally in a `PostToolUse` hook or the tool implementation itself.
- [ ] Every API request includes the full conversation history, since the API is stateless with no server-side session state.
- [ ] Upstream agents return structured data (key facts, citations, relevance scores, metadata) rather than verbose reasoning chains for downstream consumers.
- [ ] Prompt caching places static content (system instructions, tool definitions, reference docs) first with the `cache_control` breakpoint at the end of the static block, and volatile content after it.
- [ ] The caching strategy accounts for the ~5-minute ephemeral cache lifetime, targeting bursts of related requests rather than content reused hours apart.

## 5.2 Escalation & Ambiguity Resolution
Full module: `references/5-2-escalation-and-ambiguity-resolution.md`

- [ ] An explicit human request triggers immediate escalation, with no attempt to resolve the issue first.
- [ ] Escalation fires on policy gaps (situations the policy is silent on), while documented policy violations are answered directly rather than escalated.
- [ ] The "cannot make progress" trigger fires only after an actual resolution attempt, not on anticipated difficulty.
- [ ] Escalation is not driven by sentiment or frustration detection.
- [ ] Escalation is not driven by self-reported model confidence scores.
- [ ] A frustrated customer with a resolvable issue is resolved (with acknowledgement) rather than escalated, unless they reiterate a request for a human.
- [ ] Ambiguous customer matches trigger a request for a disambiguating identifier, never a heuristic pick (most recent, most active, or similar).
- [ ] Escalation criteria with few-shot examples live in the system prompt, and prompt optimisation is attempted before adding classifiers or sentiment analysis.
- [ ] Escalation handoffs use a structured format (customer ID, root cause, recommended action).

## 5.3 Error Propagation in Multi-Agent Systems
Full module: `references/5-3-error-propagation-in-multi-agent-systems.md`

- [ ] Subagent error responses carry structured context with all four elements: failure type, attempted action, partial results, and alternative approaches.
- [ ] Failure type is categorised as transient, validation, business, or permission so the coordinator can pick the right recovery path.
- [ ] No subagent returns empty results marked as `success` when the operation actually failed (silent suppression).
- [ ] A single subagent failure does not terminate the whole pipeline; the coordinator continues with partial results or targeted recovery (no workflow termination).
- [ ] Access failures (timeout, connection error, permission denial) are distinguished from valid empty results, with `shouldRetry` set accordingly.
- [ ] Valid empty results are treated as the answer and not retried; access failures are eligible for retry.
- [ ] Partial results gathered before a failure are preserved and passed to the coordinator rather than discarded.
- [ ] Subagents attempt local recovery for transient failures (retry, fallback sources, degraded responses) before escalating to the coordinator.
- [ ] Synthesis output includes coverage annotations that flag topic areas limited by unavailable sources rather than silently omitting them.

## 5.4 Codebase Exploration & Context Degradation
Full module: `references/5-4-codebase-exploration-and-context-degradation.md`

- [ ] Context degradation is treated as an attention/recall problem, not a token-limit one — enlarging the context window is not relied on as the fix.
- [ ] Transcripts are monitored for drift from specific references (class names, file paths, dependency chains) toward generic "typical pattern" language.
- [ ] Extended exploration agents write key findings to scratchpad files and read them back rather than relying on conversation context.
- [ ] Scratchpad maintenance is instructed from the start of a session, not triggered reactively after context has already degraded.
- [ ] Verbose exploration is delegated to subagents with isolated context, and the coordinator retains only structured summaries — not raw subagent transcripts.
- [ ] Phased exploration injects prior-phase summaries into new subagent prompts to prevent cold-start re-exploration.
- [ ] `/compact` is used proactively during long sessions, not only when context limits are reached.
- [ ] Each agent exports a structured state manifest (explored paths, key findings, current phase, next steps) to a known location for crash recovery.
- [ ] On resume, the coordinator loads the manifest and injects it so agents continue without repeating prior exploration.

## 5.5 Human Review & Confidence Calibration
Full module: `references/5-5-human-review-and-confidence-calibration.md`

- [ ] Automation decisions are gated on per-segment accuracy (document type AND field), never on an aggregate/headline number.
- [ ] A per-document-type and per-field accuracy breakdown exists, and any segment below the acceptable threshold is excluded from automation.
- [ ] Confidence thresholds are derived from a calibration curve built against labelled ground truth, per field type, not from raw model confidence.
- [ ] Routing sends fields above the calibrated threshold to automation, below it to human review, and ambiguous-zone fields to prioritised human review.
- [ ] Stratified random sampling covers high-confidence (automated) extractions, not just low-confidence ones, so novel error patterns in automated output are detectable.
- [ ] Sampling strata span document type, confidence band, and field type, and run on an ongoing basis rather than only at initial validation.
- [ ] Reviewer capacity is prioritised toward the highest-uncertainty items rather than spread evenly across all extractions.
- [ ] The review queue is ordered dynamically by uncertainty, so a freed reviewer picks up the highest-uncertainty item remaining, not the next chronological one.
- [ ] Human review is reduced only after steps 1–4 (segment measurement, calibration, calibrated thresholds, stratified sampling) are demonstrably in place.

## 5.6 Information Provenance & Multi-Source Synthesis
Full module: `references/5-6-information-provenance-and-multi-source-synthesis.md`

- [ ] Every finding carries a structured claim-source mapping: claim, source URL, document name, relevant excerpt, and publication date
- [ ] Subagents emit findings in the structured claim-source format rather than free-form prose
- [ ] The synthesis agent's prompt explicitly requires preserving and merging claim-source mappings when combining findings
- [ ] The final output includes inline citations or a structured reference section tracing each claim to its source
- [ ] Conflicting source values are annotated with both values and full attribution — never averaged, selected by recency, or picked by publisher authority
- [ ] Publication/data-collection dates are required in all structured outputs and preserved through merging, so trends are not misread as contradictions
- [ ] Content is rendered by type: financial data as tables, news as prose, technical findings as structured lists
- [ ] Document analysis completes with conflicts intact and explicitly annotated, leaving resolution to the coordinator or consumer
- [ ] Reports distinguish well-established findings from contested ones, preserving original source characterisations and methodological context
