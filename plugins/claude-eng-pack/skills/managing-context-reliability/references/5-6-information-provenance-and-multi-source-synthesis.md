---
domain: 5 - Context Management & Reliability
module: "5.6"
title: "Information Provenance & Multi-Source Synthesis"
---

# 5.6 Information Provenance & Multi-Source Synthesis

## Overview

Information provenance — knowing where every claim comes from and how confident you should be in it — is the difference between a research system that produces trustworthy outputs and one that produces plausible-sounding fiction. When reviewing an implementation, check how attribution survives (or dies) through multi-agent synthesis pipelines, how the system handles conflicting sources, and how temporal context prevents false contradictions.

### Structured Claim-Source Mappings

Every finding in a multi-agent research system must carry its provenance. This is not optional metadata — it is the structural guarantee that the final output can be traced back to specific sources. Each finding must include:

- **Claim:** The specific assertion being made
- **Source URL:** Where the information was found
- **Document name:** The title of the source document
- **Relevant excerpt:** The specific passage that supports the claim
- **Publication date:** When the source was published or data was collected

```json
{
  "claim": "Global renewable energy investment reached $495 billion in 2023",
  "sourceUrl": "https://example.com/iea-report-2024",
  "documentName": "IEA World Energy Investment Report 2024",
  "relevantExcerpt": "Total investment in renewable energy technologies reached approximately $495 billion in calendar year 2023, representing a 17% increase over 2022.",
  "publicationDate": "2024-06-15"
}
```

The critical challenge is that attribution dies during summarisation. When a synthesis agent combines findings from multiple subagents, it naturally compresses and paraphrases. Without explicit instructions to preserve claim-source mappings, the synthesis produces statements like "Investment in renewable energy has grown significantly" — no amount, no source, no date.

**Downstream agents must explicitly preserve and merge claim-source mappings through synthesis.** This requires:

- Subagents output findings in the structured claim-source format.
- The synthesis agent is instructed to maintain these mappings when combining findings.
- The final output includes inline citations or a structured reference section that traces each claim to its source.

### Conflict Handling

When two credible sources report different statistics for the same measure, the synthesis agent faces a critical decision. The wrong approach — and a common one to check for — is to arbitrarily select one value.

**Example:** Source A reports 12% market growth. Source B reports 8% market growth. Both are credible publications.

**Wrong approach:** Select the more recent source, or average the values, or pick the one from the more authoritative publisher.

**Correct approach:** Annotate with both values and full source attribution. Let the consumer decide.

```markdown
Market growth estimates vary by source:
- **12% growth** — IEA World Energy Report (published June 2024, using 2023 calendar year data)
- **8% growth** — Bloomberg NEF Annual Review (published March 2024, using July 2022–June 2023 data)

The difference may reflect different reporting periods and methodological approaches.
```

This preserves the full picture. The consumer can see both values, understand the sources, and make their own judgement about which is more relevant to their needs. Arbitrarily selecting one value destroys information and presents a false certainty.

> **Common Mistake**
> Averaging two conflicting statistics, or defaulting to the most recent or most authoritative source, looks like a tidy resolution — reject it. These moves collapse two real data points into one and manufacture false certainty; the correct behaviour is to surface both values with full attribution and let the consumer decide.

### Temporal Awareness

Different publication dates explain different numbers. This is not a contradiction — it is temporal context that must be preserved.

Consider two sources:

- Source A (published 2023): reports 8% growth
- Source B (published 2024): reports 12% growth

Without publication dates, these look contradictory. With dates, they tell a story: growth accelerated from 8% to 12% over the measured period. The "conflict" is actually a trend.

**Require publication/data collection dates in all structured outputs.** This is not just metadata tidiness — it is essential for correct interpretation. Without temporal context, valid trends are misinterpreted as data quality issues, and the synthesis agent may incorrectly flag or suppress findings that are actually consistent.

Subagents must include these dates in their structured outputs. The synthesis agent must preserve them through the merging process. And the final output must present them alongside the data they describe.

### Content-Appropriate Rendering

Different types of content demand different presentation formats. Synthesis should not flatten everything into a uniform format:

**Financial data → Tables.** Numbers, comparisons, and trends are most readable in tabular format. Forcing financial data into prose paragraphs makes it harder to compare values and spot patterns.

| Year | Investment ($B) | Growth (%) |
| --- | --- | --- |
| 2021 | 366 | 12% |
| 2022 | 423 | 16% |
| 2023 | 495 | 17% |

**News and current events → Prose.** Narrative context, cause-and-effect relationships, and chronological developments read naturally as paragraphs.

**Technical findings → Structured lists.** Architectural patterns, API specifications, and configuration options are clearest as bulleted or numbered lists with clear hierarchy.

Forcing all content into a single format — all tables, or all prose, or all lists — degrades readability and comprehension. The synthesis agent should select the appropriate rendering format based on the content type.

### Attribution Preservation Through Multi-Step Synthesis

In a multi-agent pipeline, attribution must survive every step:

- **Research subagent** collects findings with claim-source mappings.
- **Analysis subagent** evaluates findings and adds assessment, preserving original mappings.
- **Synthesis subagent** combines findings from multiple agents, merging mappings.
- **Report generation** produces the final output with inline citations.

At each step, there is a risk of attribution loss. The most common failure point is step 3, where the synthesis agent combines and paraphrases findings without carrying the source mappings forward. The synthesis agent's prompt must explicitly require that every claim in its output is traceable to a specific source.

Reports should include explicit sections distinguishing well-established findings from contested ones, preserving original source characterisations and methodological context. A finding supported by three independent sources is different from a finding based on a single report, even if both are presented with equal confidence in the text.

### Completing Analysis with Conflicts Intact

When document analysis encounters conflicting values, the analysis agent must complete its work with the conflicts included and explicitly annotated. It should not resolve the conflict — that decision belongs to the coordinator or the consumer.

```json
{
  "field": "annualRevenue",
  "conflictDetected": true,
  "values": [
    {
      "value": "$4.2M",
      "source": "Annual Report 2023",
      "context": "Audited financial statements, fiscal year ending December 2023"
    },
    {
      "value": "$3.8M",
      "source": "SEC Filing Q4 2023",
      "context": "Preliminary unaudited figures, calendar year 2023"
    }
  ],
  "possibleExplanation": "Difference may reflect audited vs preliminary figures and fiscal vs calendar year reporting periods"
}
```

The coordinator can then decide how to handle the conflict: present both values, investigate further, or escalate to a human analyst.

> **Key Concept**
> Every claim needs a structured mapping: claim + source URL + document name + excerpt + publication date. Attribution dies during summarisation unless explicitly preserved. Conflicting sources should be annotated with both values and attribution — never arbitrarily pick one. Different dates explain different numbers. Render content appropriately: financial data as tables, news as prose, technical findings as lists.

## Audit Checklist

- [ ] Every finding carries a structured claim-source mapping: claim, source URL, document name, relevant excerpt, and publication date
- [ ] Subagents emit findings in the structured claim-source format rather than free-form prose
- [ ] The synthesis agent's prompt explicitly requires preserving and merging claim-source mappings when combining findings
- [ ] The final output includes inline citations or a structured reference section tracing each claim to its source
- [ ] Conflicting source values are annotated with both values and full attribution — never averaged, selected by recency, or picked by publisher authority
- [ ] Publication/data-collection dates are required in all structured outputs and preserved through merging, so trends are not misread as contradictions
- [ ] Content is rendered by type: financial data as tables, news as prose, technical findings as structured lists
- [ ] Document analysis completes with conflicts intact and explicitly annotated, leaving resolution to the coordinator or consumer
- [ ] Reports distinguish well-established findings from contested ones, preserving original source characterisations and methodological context

## Sources

- [Anthropic Multi-Agent Research Patterns](https://www.anthropic.com/engineering/built-multi-agent-research-system) — Anthropic
- [Anthropic Prompt Engineering — Citation and Attribution](https://platform.claude.com/docs/en/build-with-claude/citations) — Anthropic
