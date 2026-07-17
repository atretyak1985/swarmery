# Aggregated Audit Checklists — Claude Code Configuration & Workflows

Run these against the implementation under review. For the reasoning behind any item, read the full module file.

## 3.1 CLAUDE.md Hierarchy, Scoping, and Modular Organisation
Full module: `references/3-1-claude-md-hierarchy-scoping-and-modular-organisation.md`

- [ ] Team-wide conventions (naming, error handling, testing, architecture) live in project-level config (`.claude/CLAUDE.md` or root `CLAUDE.md`), not in a developer's user-level `~/.claude/CLAUDE.md`.
- [ ] Rules that must be honoured on every run (blocked tools, required formatters, permission policies) are encoded in `settings.json` or a hook, not left to CLAUDE.md scoping.
- [ ] No configuration relies on "more specific scope wins" or "user-level overrides project-level" behaviour, since contradictory CLAUDE.md rules may resolve arbitrarily.
- [ ] `@` path imports use a bare `@<path>` directive, not an `@import` keyword.
- [ ] `@` imports are not being used to shrink per-session context (they load eagerly); path-scoped `.claude/rules/` is used for conditional loading instead.
- [ ] `CLAUDE.local.md` contains only personal, repo-specific quirks and is gitignored; anything that is a team rule lives in `CLAUDE.md`.
- [ ] Directory-level `CLAUDE.md` files are reserved for package-specific conventions that differ from the project root.
- [ ] `/memory` is used to diagnose which memory files are loaded, not treated as a mechanism that activates them.

## 3.2 Custom Slash Commands and Skills
Full module: `references/3-2-custom-slash-commands-and-skills.md`

- [ ] Skills are directories containing a `SKILL.md` entrypoint; commands are flat `.md` files — a flat file placed directly in `.claude/skills/` does not register a command.
- [ ] New commands prefer the canonical `.claude/skills/<name>/SKILL.md` path over the `.claude/commands/` alias to gain supporting files, automatic discovery, and name precedence.
- [ ] Team-wide commands live under project-scoped `.claude/` (version-controlled); personal-only workflows live under user-scoped `~/.claude/` and are not shared.
- [ ] Verbose or exploratory skills (codebase analysis, brainstorming) declare `context: fork` so their output stays out of the main conversation's context budget.
- [ ] `allowed-tools` is used only to pre-approve tools and skip prompts — it is never relied on as a security boundary; tool restriction uses `disallowed-tools` or permission deny rules.
- [ ] Skills that require parameters supply an `argument-hint` so inputs are explicit.
- [ ] Task-specific procedures live in skills, not CLAUDE.md; always-on universal standards live in CLAUDE.md (or `.claude/rules/`), not skills.
- [ ] File-type-specific conventions use path-scoped `.claude/rules/` so they load as always-on context alongside matching files.
- [ ] Personal skill variants use distinct names (e.g. `/deep-analyse` vs the team's `/analyse`) so they do not override or conflict with shared team skills.

## 3.3 Path-Specific Rules for Conditional Convention Loading
Full module: `references/3-3-path-specific-rules-for-conditional-convention-loading.md`

- [ ] Conventions that apply to a file type spread across many directories are implemented as path-specific rules with glob patterns, not duplicated CLAUDE.md files per directory.
- [ ] Rule files live in `.claude/rules/` and declare their scope with a `paths` glob field in YAML frontmatter.
- [ ] Glob patterns use `**/` prefixes so they match files anywhere in the tree, not just one directory.
- [ ] Type-specific conventions (tests, API handlers, IaC) are kept out of root CLAUDE.md so they only consume context when matching files are being edited.
- [ ] Root CLAUDE.md is reserved for universal standards that apply to all code.
- [ ] Conventions scoped to a single directory use a directory-level CLAUDE.md rather than a glob rule.
- [ ] On-demand, task-specific workflows are implemented as skills in `.claude/skills/`, not as path rules.
- [ ] Changing a convention for a file type requires editing one rule file, not many copies, so no drift can accumulate.

## 3.4 Plan Mode vs Direct Execution
Full module: `references/3-4-plan-mode-vs-direct-execution.md`

- [ ] Mode selection is driven by ambiguity and scope, not by the task's perceived difficulty.
- [ ] Plan mode is used for large-scale changes, architectural decisions, multi-file modifications, and cases with multiple valid approaches.
- [ ] Direct execution is used only for well-scoped changes with a known approach and known location (single function, single file, clear stack trace).
- [ ] Plan mode performs exploration and design without modifying any files.
- [ ] Verbose discovery is routed through the Explore subagent so file listings and dependency graphs do not flood the main context window; only summaries return to the main conversation.
- [ ] Multi-file migrations and investigation-then-implementation tasks use the plan-then-execute hybrid rather than a single mode forced across the whole task.
- [ ] Tasks whose requirements already state complexity start in plan mode immediately, rather than starting in direct execution and switching only if complexity emerges.

## 3.5 Iterative Refinement Techniques
Full module: `references/3-5-iterative-refinement-techniques.md`

- [ ] When prose descriptions are interpreted inconsistently, the response is concrete input/output examples — not more prose.
- [ ] Two to three well-chosen examples are used to establish a pattern; the set is not padded out to cover every possible case.
- [ ] Complex transformations with many edge cases are driven by test-first iteration, with the raw test failures fed back to Claude Code rather than a prose restatement.
- [ ] Test cases cover the happy path, edge cases (null values, empty inputs, boundary conditions), and performance requirements where applicable.
- [ ] The interview pattern is reserved for unfamiliar domains and is not confused with concrete examples, which are for known transformations that the model interprets inconsistently.
- [ ] Feedback on fixes that interact (e.g. error handling, logging format, response structure) is delivered in a single batched message so the model sees all constraints at once.
- [ ] Feedback on independent issues is delivered sequentially, one iteration at a time, to avoid confusing which feedback applies to which part of the code.
- [ ] After switching to examples, generalisation is verified on a fresh case, and edge-case examples are added only when the model handles the standard case but misses the edges.

## 3.6 CI/CD Integration
Full module: `references/3-6-ci-cd-integration.md`

- [ ] Every headless Claude Code invocation passes `-p` (`--print`) so CI jobs do not hang waiting for interactive input.
- [ ] Output consumed by automated systems uses `--output-format json`, with `--json-schema` where a fixed structure is required for programmatic parsing.
- [ ] Code review runs in a separate Claude Code invocation from the one that generated the code, with no shared reasoning context.
- [ ] Per-push automated reviews include prior review findings in context and report only new or still-unaddressed issues, instead of re-scanning the whole PR from scratch.
- [ ] Project CLAUDE.md supplies CI-relevant context (testing standards, fixtures, review criteria, existing coverage) for headless runs.
- [ ] Test-generation runs include existing test files in context so Claude fills coverage gaps rather than duplicating scenarios.
- [ ] System-prompt flag choice matches intent: append (`--append-system-prompt[-file]`) to keep Claude Code's defaults, replace (`--system-prompt[-file]`) only when the agent's identity or permission model differs.
- [ ] Blocking pre-merge checks use the real-time synchronous API; only latency-tolerant scheduled jobs use the Batch API for its 50% cost saving.
- [ ] Permissions and tools are explicitly scoped for headless runs via `--permission-mode`, `--allowedTools`, `--disallowedTools`, and `--tools`.
