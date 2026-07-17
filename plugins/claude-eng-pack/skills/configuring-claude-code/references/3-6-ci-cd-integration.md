---
domain: 3 - Claude Code Configuration & Workflows
module: "3.6"
title: "CI/CD Integration"
---

# 3.6 CI/CD Integration

## Overview

Integrating Claude Code into CI/CD pipelines transforms it from an interactive developer tool into an automated review and generation engine. Five concepts govern whether a headless integration behaves correctly, and the `-p` flag is the most fundamental of them — without it the pipeline never runs at all.

### The -p Flag: Non-Interactive Mode

Claude Code defaults to interactive mode — it expects keyboard input and displays a conversational interface. In a CI pipeline, there is no keyboard. Without the `-p` flag, the CI job hangs indefinitely, waiting for input that will never arrive.

```bash
# WRONG — hangs in CI
claude "Analyse this pull request for security issues"

# CORRECT — runs non-interactively
claude -p "Analyse this pull request for security issues"
```

The `-p` flag (also `--print`) switches Claude Code to print mode: it processes the prompt, outputs the result to stdout, and exits. No interactive input required.

When reviewing a CI integration that hangs with logs showing Claude waiting for input, the fix is the `-p` flag. Wrong fixes to reject: `CLAUDE_HEADLESS=true` (does not exist), `--batch` (does not exist), and redirecting stdin from `/dev/null` (does not properly address Claude Code's interactive mode).

> **Key Concept**
> The `-p` flag is the single most important fact for running Claude Code headlessly. When a CI pipeline hangs and the logs show Claude waiting for input, the fix is always `-p`.

### Structured Output for CI

In CI pipelines, Claude Code output must be machine-parseable. Humans are not reading the output — automated systems are processing it to post inline PR comments, update dashboards, or trigger downstream workflows.

Two flags work together:

- `--output-format json` — forces JSON output instead of human-readable text
- `--json-schema` — enforces a specific JSON structure for the output

```bash
claude -p \
  --output-format json \
  --json-schema '{"type":"object","properties":{"findings":{"type":"array","items":{"type":"object","properties":{"file":{"type":"string"},"line":{"type":"integer"},"severity":{"type":"string"},"message":{"type":"string"}}}}}}' \
  "Review this PR for security issues"
```

The output conforms to the specified schema, enabling automated systems to:

- Parse findings programmatically
- Post findings as inline PR comments at the exact file and line
- Filter by severity for different notification channels
- Track findings across review runs

### Session Context Isolation

The same Claude session that generated code is less effective at reviewing its own changes. This is not a theoretical concern — it is a measurable effect.

**Why self-review is weaker:**

When Claude generates code in a session, it builds up reasoning context: why it chose this approach, what tradeoffs it considered, what alternatives it rejected. When you then ask it to review the same code in the same session, it retains that reasoning context. It is less likely to question decisions it already justified to itself.

**The fix: independent review instances**

Use a separate Claude Code invocation for review — one that has no access to the generation session's reasoning context. The independent reviewer evaluates the code on its own merits, without the bias of prior justification.

```bash
# Step 1: Generate code (session A)
claude -p "Implement the authentication middleware"

# Step 2: Review code (session B — independent, no shared context)
claude -p "Review the authentication middleware for security issues, error handling gaps, and edge cases"
```

This connects to module 1 (Agentic Architecture & Orchestration) on multi-instance review architectures and module 5 (Context Management & Reliability) on context management. In CI/CD it shows up specifically as the choice between reusing a generation session and spawning an independent reviewer — when auditing a review pipeline, check that review runs in a fresh instance.

### Incremental Review Context

Automated reviews run on every push. Without context about previous reviews, each run analyses the entire PR from scratch, so it re-derives the same findings every time. A genuinely fixed issue drops out on its own, because the changed code no longer triggers it. The ones that keep coming back are the issues the developer saw and deliberately chose not to change; a context-free re-scan cannot tell those apart from new problems, so it flags them again on every push.

The fix: include prior review findings in context and instruct Claude to report only new or still-unaddressed issues.

```bash
claude -p \
  --output-format json \
  "Review this PR. Here are the findings from the previous review:
  ${PREVIOUS_FINDINGS}

  Report ONLY:
  1. New issues not in the previous findings
  2. Issues from the previous findings that are still present

  Do NOT re-report previous findings the developer has already reviewed and chosen not to act on."
```

Duplicate comments erode developer trust. If every push generates the same five comments regardless of whether the developer fixed the issues, developers stop reading the comments. Incremental review context preserves the signal-to-noise ratio.

### CLAUDE.md for CI Context

When Claude Code runs in CI, it reads the project's CLAUDE.md files just as it does in interactive mode. This means the CLAUDE.md is the mechanism for providing project-specific context to CI-invoked Claude Code:

- **Testing standards:** what makes a valuable test, what patterns to follow, what to avoid
- **Available fixtures:** which test fixtures exist, how to use them, what data they contain
- **Review criteria:** what constitutes a critical finding vs a minor style issue
- **Existing test coverage:** what is already covered, to avoid suggesting duplicate tests

Without this context in CLAUDE.md, CI-invoked test generation produces low-value boilerplate. With it, generated tests follow the team's patterns and add genuine coverage.

```text
# .claude/CLAUDE.md — CI-relevant section
## Testing Standards

- Tests must use the factory pattern from test/factories/ for data creation
- Integration tests connect to the test database via test/setup/db.ts
- Do not test private implementation details — test public API contracts
- Coverage target: 80% branch coverage for new code
- Available fixtures: test/fixtures/users.json, test/fixtures/orders.json
```

### CLI Flags Reference

The `-p` flag is the most important flag, but a production integration also depends on the flags that shape a headless run: how output is formatted, which system prompt is used, and how permissions and tools are scoped. These flags work with `claude -p` in CI and with the interactive `claude` command.

**System prompt flags.** Claude Code provides four flags here, and the append-versus-replace distinction is the one to get right:

| Flag | Effect |
| --- | --- |
| `--system-prompt "<text>"` | Replaces the entire default system prompt |
| `--system-prompt-file <path>` | Replaces the default prompt with a file's contents |
| `--append-system-prompt "<text>"` | Appends text to the default prompt |
| `--append-system-prompt-file <path>` | Appends a file's contents to the default prompt |

Append when Claude should stay a coding assistant that also follows your extra rules. Appending keeps the default tool guidance, safety instructions, and coding conventions, so you only supply what differs. Replace when the identity or permission model differs from Claude Code's, like a non-coding agent in a pipeline no human watches. Replacing drops the entire default prompt, so you own everything the task still needs.

**Headless output and limits (print mode).**

| Flag | Effect |
| --- | --- |
| `--output-format text\|json\|stream-json` | Output shape for `-p`; `json` and `stream-json` are machine-parseable |
| `--input-format text\|stream-json` | Input shape for `-p` |
| `--json-schema '<schema>'` | Validate the final output against a JSON Schema |
| `--max-turns <n>` | Cap the number of agentic turns, then exit |
| `--verbose` | Full turn-by-turn output |

**Permissions, tools, and context.**

| Flag | Effect |
| --- | --- |
| `--permission-mode <mode>` | Start in `default`, `acceptEdits`, `plan`, `auto`, `dontAsk`, or `bypassPermissions` |
| `--allowedTools "<rules>"` | Tools that run without a permission prompt, e.g. `"Bash(git diff *)" "Read"` |
| `--disallowedTools "<rules>"` | Deny rules; a bare tool name removes the tool from context entirely |
| `--tools "Bash,Edit,Read"` | Restrict which built-in tools are available at all |
| `--add-dir <path>` | Add a directory Claude may read and edit (grants file access, not configuration discovery) |
| `--model <alias\|name>` | Set the session model (`sonnet`, `opus`, or a full model name) |

**Session and start-up.** `-c` / `--continue` resumes the most recent conversation in the current directory, and `-r` / `--resume <id|name>` resumes a specific session. `--bare` is minimal mode: it skips auto-discovery of hooks, skills, plugins, MCP servers, auto memory, and CLAUDE.md so scripted calls start faster, leaving Claude with the Bash and file read/edit tools only. Reach for `--bare` when you want a fast, predictable scripted run and don't need project configuration loaded.

### Providing Existing Tests to Avoid Duplication

When running test generation in CI, include existing test files in context. Without them, Claude Code may suggest tests that already exist, wasting developer review time. Including existing tests enables Claude to identify coverage gaps rather than duplicating existing scenarios.

### Batch API vs Real-Time for CI Workflows

The Message Batches API offers 50% cost savings but has processing times up to 24 hours with no guaranteed latency SLA. This creates a clear decision boundary:

| Workflow type | API choice | Reason |
| --- | --- | --- |
| Pre-merge checks (blocking) | Real-time (synchronous) | Developers wait for results |
| Overnight technical debt reports | Batch API | Not time-sensitive, 50% savings |
| Weekly code audit | Batch API | Scheduled, latency-tolerant |
| Nightly test generation | Batch API | Runs overnight, reviewed next morning |

Pre-merge checks are blocking workflows. Developers cannot merge until the check completes. Batch API is unsuitable because there is no latency guarantee.

> **Common Mistake**
> Reaching for the Batch API on a blocking pre-merge check because of the 50% cost saving looks reasonable, but reject it: the saving is real only for latency-tolerant work. A pre-merge gate that developers wait on needs the real-time synchronous API, because the Batch API offers no latency guarantee.

## Audit Checklist

- [ ] Every headless Claude Code invocation passes `-p` (`--print`) so CI jobs do not hang waiting for interactive input.
- [ ] Output consumed by automated systems uses `--output-format json`, with `--json-schema` where a fixed structure is required for programmatic parsing.
- [ ] Code review runs in a separate Claude Code invocation from the one that generated the code, with no shared reasoning context.
- [ ] Per-push automated reviews include prior review findings in context and report only new or still-unaddressed issues, instead of re-scanning the whole PR from scratch.
- [ ] Project CLAUDE.md supplies CI-relevant context (testing standards, fixtures, review criteria, existing coverage) for headless runs.
- [ ] Test-generation runs include existing test files in context so Claude fills coverage gaps rather than duplicating scenarios.
- [ ] System-prompt flag choice matches intent: append (`--append-system-prompt[-file]`) to keep Claude Code's defaults, replace (`--system-prompt[-file]`) only when the agent's identity or permission model differs.
- [ ] Blocking pre-merge checks use the real-time synchronous API; only latency-tolerant scheduled jobs use the Batch API for its 50% cost saving.
- [ ] Permissions and tools are explicitly scoped for headless runs via `--permission-mode`, `--allowedTools`, `--disallowedTools`, and `--tools`.

## Sources

- [Claude Code CLI Reference](https://code.claude.com/docs/en/cli-reference) — Anthropic
- [Anthropic Message Batches API Documentation](https://platform.claude.com/docs/en/build-with-claude/batch-processing) — Anthropic
