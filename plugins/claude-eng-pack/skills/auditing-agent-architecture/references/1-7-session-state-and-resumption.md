---
domain: 1 - Agentic Architecture & Orchestration
module: "1.7"
title: "Session State and Resumption"
---

# 1.7 Session State and Resumption

## Overview

Session management determines how an agent maintains continuity across work sessions. In long-running tasks — debugging a complex system, reviewing a large codebase, conducting multi-day research — the agent's context accumulates tool results, file analyses, and reasoning chains. This module covers how to manage that accumulated state: when to continue it, when to branch it, and when to start fresh. When auditing an implementation, the core question is whether the system picks the right continuity strategy for the situation rather than defaulting to one approach for every case.

### Three Session Management Options

The Agent SDK and Claude Code provide three distinct approaches to session management. Each serves a different purpose, and a correct implementation selects the one that matches the given scenario.

**Option 1: `--resume <session-name>`**

Resume continues a specific named session from where it left off. The entire conversation history — including all tool results, analyses, and reasoning — is restored.

**When to use:** The prior context is mostly still valid. Files have not changed significantly since the last session. You want to pick up exactly where you stopped.

**When NOT to use:** Files have been modified since the last session. Tool results in the conversation history no longer reflect the current state of the codebase. This leads to the stale context problem (covered below).

**Option 2: `fork_session`**

Fork creates an independent branch from a shared analysis baseline. After the fork, each branch operates independently — changes in one branch do not affect the other, and branches cannot see each other's results.

**When to use:** You have completed an initial analysis and want to explore divergent approaches from that shared starting point. For example, after analysing a codebase, you fork to compare two refactoring strategies. Each fork builds on the same initial understanding but takes a different direction.

**When NOT to use:** You simply want to continue the same line of investigation. Fork is for divergence, not continuation. If you are not comparing alternatives, use resume.

**Option 3: Fresh start with summary injection**

Start a completely new session but inject a structured summary of the prior session's findings into the initial context. The new session has no stale tool results — only the curated summary you provide.

**When to use:** Tool results from the prior session are stale (files have changed, APIs have been updated, dependencies have shifted). Context has degraded over a long session (too many irrelevant tool results cluttering the history). You need a clean baseline with preserved knowledge.

**When NOT to use:** The prior context is still valid and you want to maintain the full conversation history. In this case, resume is more efficient.

> **Key Concept**
>
> Three session management options serve three distinct purposes: **resume** for continuation, **fork** for divergent exploration, and **fresh start with summary injection** for when prior tool results are stale. Selecting the right option for each scenario is the difference between consistent and contradictory agent behaviour.

### The Stale Context Problem

The stale context problem is the central concept of this module. It occurs when an agent resumes a session after code modifications and reasons from cached tool results that no longer reflect the current state of files.

**How it manifests:** A developer works with Claude Code to analyse a codebase. They make changes to 3 files and resume the session. Claude gives contradictory advice about those files — recommending changes that were already made, or referencing code that no longer exists — because it is reasoning from the old tool results still in its conversation history.

**Why it happens:** When you resume a session, the entire conversation history is restored, including every tool result from the previous session. If a file was read during the previous session and has since been modified, the old file contents are still in the conversation as a tool result. The model reasons from that stale data alongside any new data, leading to contradictions.

**The naive fix (and why it is insufficient):** Simply resuming the session and asking the agent to re-read the modified files. This is better than nothing, but the stale tool results remain in the conversation history. The model may still reference old information from earlier in the context, especially for tangential decisions that do not directly involve the modified files.

**The correct fix:** Start a fresh session with a structured summary of prior findings. Specify which files have changed so the agent can perform targeted re-analysis of those files. The fresh session has no stale tool results, and the injected summary preserves the knowledge from the prior session without the outdated data.

> **Common Mistake**
>
> "Resume the session and ask the agent to re-read the changed files" looks like a complete fix for stale context — reject it as sufficient on its own. The stale tool results remain in history and can still influence reasoning, especially on tangential decisions. A fresh start with summary injection is the reliable choice when files have changed.

### Targeted Re-Analysis vs Full Re-Exploration

When files have changed, the agent does not need to re-analyse the entire codebase. This is wasteful, especially for large codebases where only a few files were modified.

The correct approach is **targeted re-analysis**: inform the agent about the specific files that changed and let it re-analyse only those files. The summary from the prior session covers everything that has not changed.

**What targeted re-analysis looks like in practice:**

- Start a fresh session.
- Inject a structured summary: "Prior analysis found X, Y, and Z across the codebase. The following 3 files have been modified since: auth.ts, database.ts, and api-routes.ts."
- The agent re-reads and re-analyses only the 3 modified files.
- It combines the fresh analysis of changed files with the preserved summary of unchanged files.

This is faster than full re-exploration and more reliable than resuming with stale context.

### When to Use Each Option: Decision Matrix

| Scenario | Best Option | Reasoning |
| --- | --- | --- |
| Continuing work from yesterday, no files changed | `--resume` | Prior context is valid, full history is useful |
| Comparing two refactoring approaches | `fork_session` | Divergent exploration from shared baseline |
| Resuming after modifying 3 of 50 files | Fresh start + summary | Stale tool results for modified files would cause contradictions |
| Long session with cluttered history | Fresh start + summary | Degraded context benefits from a clean baseline |
| Exploring a testing strategy vs a documentation strategy | `fork_session` | Two independent approaches from the same analysis |
| Resuming after dependency updates | Fresh start + summary | Multiple files may have changed indirectly |

### Practical Example: The Contradictory Advice Bug

A developer uses Claude Code to analyse a 50-file codebase over two days. On Day 1, they analyse the authentication module and identify three issues. Overnight, they fix all three issues by modifying `auth.ts`, `session.ts`, and `middleware.ts`.

On Day 2, they resume the session. Claude recommends fixing the three issues that were already fixed — because the old tool results showing the unfixed code are still in the conversation history. Worse, when asked about the current state of auth.ts, Claude gives contradictory answers: sometimes referencing the old code (from the stale tool result) and sometimes referencing the new code (from a fresh read).

The fix: start a fresh session with a summary. "Prior analysis identified three authentication issues in auth.ts, session.ts, and middleware.ts. All three have been fixed. Please re-analyse these three files to verify the fixes and check for any new issues introduced by the changes."

The fresh session has no stale tool results. The agent reads the current files, verifies the fixes, and provides consistent advice based on the actual current state.

For how re-injected summaries fit into structured handoffs between phases of work, see module 1.4 (Workflow Enforcement and Handoff).

## Audit Checklist

- [ ] `--resume` is used only when files are unchanged since the prior session and the full history is still valid
- [ ] `fork_session` is reserved for comparing divergent approaches from a shared baseline, never for plain continuation
- [ ] When files have changed between sessions, a fresh start with structured summary injection is used instead of resume
- [ ] Injected summaries name the specific files that changed so the agent performs targeted re-analysis, not full re-exploration
- [ ] "Resume then re-read the changed files" is not treated as a complete fix — stale tool results remaining in history are recognised as a source of contradictory reasoning
- [ ] Resuming after dependency updates is handled as a stale-context risk, since multiple files may have changed indirectly
- [ ] Long sessions with cluttered or degraded history are reset with a fresh start plus summary rather than continued indefinitely
- [ ] Fresh-session summaries preserve prior findings for unchanged files so knowledge is not lost when starting clean

## Sources

- [Claude Code Documentation](https://code.claude.com/docs/en) — Anthropic
- [Claude Agent SDK Overview](https://platform.claude.com/docs/en/agent-sdk/overview) — Anthropic
