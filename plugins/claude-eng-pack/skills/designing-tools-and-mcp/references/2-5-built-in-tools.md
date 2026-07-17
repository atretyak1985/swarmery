---
domain: 2 - Tool Design & MCP Integration
module: "2.5"
title: "Built-in Tools"
---

# 2.5 Built-in Tools

## Overview

Claude Code provides six built-in tools for working with codebases: Read, Write, Edit, Bash, Grep, and Glob. Each has a specific purpose, and using the wrong tool for a task wastes time, context tokens, or both. When reviewing an implementation, check that each tool is used for its intended purpose — confusing these tools leads to failed operations, wasted context, or subtly wrong results.

### Grep vs Glob: The Core Distinction

This is the single most important distinction among the built-in tools. Getting it wrong produces failed searches or results that only appear correct.

**Grep searches file CONTENTS for patterns.** Use Grep when you need to find text inside files. Function callers. Error messages. Import statements. Variable assignments. Any time you are searching for what files contain, Grep is the tool.

```
// Find all files that call processLegacyOrder()
Grep: "processLegacyOrder"

// Find all error messages containing "timeout"
Grep: "timeout"

// Find all files that import a specific module
Grep: "import.*from 'utils/auth'"
```

**Glob matches file PATHS by naming patterns.** Use Glob when you need to find files by name, extension, or directory structure. Test files. Configuration files. All TypeScript files in a specific directory. Any time you are searching for files based on their path, Glob is the tool.

```
// Find all test files
Glob: "**/*.test.tsx"

// Find all configuration files
Glob: "**/config.*"

// Find all MDX files in the domains directory
Glob: "content/domains/**/*.mdx"
```

**The distinction in one sentence:** Grep finds what is INSIDE files. Glob finds files by their NAMES.

When reviewing an implementation, watch for the wrong tool being used. If someone uses Glob to find function callers, it will fail — Glob matches paths, not contents. If someone uses Grep to find test files by naming pattern, it will work technically (by searching for "test" in filenames via content), but it is the wrong tool; Glob is the correct choice.

### Read, Write, and Edit

These three tools handle file operations, each optimised for a different use case.

**Edit** performs targeted modifications using unique text matching. You specify the exact text to find and its replacement. It is fast and precise because it touches only the specific text you identify.

```
Edit:
  old_string: "function processOrder(id: string)"
  new_string: "function processOrder(id: string, validate: boolean = true)"
```

**When Edit fails:** Edit requires unique text matching. If the text you specify appears in multiple places in the file, Edit cannot determine which occurrence to change and will fail. This is not a bug — it is a safety mechanism preventing unintended modifications.

**When Edit can't find a unique anchor.** The fix per the [Edit tool docs](https://code.claude.com/docs/en/tools-reference#edit-tool-behavior) is to **widen `old_string` with more surrounding context until it pins down one location**, or set `replace_all: true` if you actually want every occurrence updated. Both options keep you on Edit and cost almost no extra context. Read + Write (load the whole file, write the whole file back) is the last resort. It works. But you've now spent a file's worth of tokens on what was usually a one-line change.

The ordering:

1. Try Edit with the shortest anchor that's plausibly unique.
2. On a non-unique match, **widen `old_string`** until it matches one location, or use `replace_all: true` if you want every occurrence changed.
3. Only fall back to Read + Write when neither of those can disambiguate the target.

Don't default to Read + Write for every modification — it burns context tokens. And don't jump straight from a non-unique Edit failure to Read + Write. Widening the anchor or using `replace_all` is the documented response. Read + Write is the fallback, not the next step. When reviewing an implementation, flag either pattern.

### Incremental Codebase Understanding

How you explore a codebase matters as much as which tools you use. There is a right way and a wrong way.

**Wrong: Read all files upfront.** Loading every file into context before you understand what you need is a context-budget killer. If a codebase has 200 files and you read them all, you have consumed your entire context window on files that are mostly irrelevant to your task. This is the single biggest anti-pattern in codebase exploration.

**Right: Incremental discovery.** Start narrow. Expand only as needed.

1. **Grep to find entry points.** Search for the function name, class name, or error message that anchors your investigation. This tells you which files are relevant.

2. **Read to follow imports and trace flows.** Once you know which files matter, Read them to understand the code structure. Follow import statements to discover related files.

3. **Grep again to trace usage.** If you find a wrapper function or re-export, Grep for that name across the codebase to find all consumers.

4. **Read only what you need.** Each file you read should be justified by what you discovered in the previous step.

This approach uses minimal context for maximum understanding. You discover the codebase's structure progressively, spending tokens only on files that are relevant to your task.

### Tracing Function Usage Across Wrapper Modules

A common codebase pattern: a function is defined in one module, re-exported through a wrapper, and consumed through the wrapper's name. Simple Grep for the original function name will miss consumers who import through the wrapper.

The correct approach:

1. **Grep for the function definition** to find where it is defined
2. **Read the defining file** to identify exported names
3. **Grep for each exported name** across the codebase to find all consumers
4. If the function is re-exported through a barrel file (e.g. `index.ts`), **Grep for the barrel file's module name** to find consumers who import from it

This multi-step trace is more thorough than a single Grep and catches indirect consumers that a simple search would miss.

### The Deprecation Scenario

This scenario comes up frequently in real codebase maintenance: find all files that call a deprecated function AND find the test files that exercise it. The correct tool sequence:

1. **Grep for the function name** — finds every file whose contents reference the function, including any tests that import it directly (content search)
2. **Glob for sibling test files** — finds the test file that pairs with each caller by naming convention, e.g. `OrderProcessor.ts` → `OrderProcessor.test.tsx`, even when the test exercises the function indirectly through the source module (path matching)
3. **Grep again for wrapper names** — when a caller exposes the function through a wrapper (e.g. `applyLegacyOrder` calls `processLegacyOrder` internally), Grep for the wrapper name to find tests that cover the function transitively through it

For example, if Grep reveals that `OrderProcessor.ts` and `RefundHandler.ts` call the deprecated function, Glob for `**/OrderProcessor.test.*` and `**/RefundHandler.test.*` to pull in their sibling test files even if those tests never mention `processLegacyOrder` by name. If either source file wraps the function under a new name, Grep for that wrapper name to catch any additional tests.

This is Grep, then Glob, then Grep again — content search for direct references, path matching for adjacent tests, content search for indirect coverage. Not Glob first.

> **Key Concept**
> Grep searches file contents. Glob matches file paths. Edit is the default for modifications. On a non-unique match, widen the anchor or use `replace_all: true`. Read + Write is the last-resort fallback. Build codebase understanding incrementally. Never read all files upfront.

## Audit Checklist

- [ ] Content searches (function callers, error messages, imports) use Grep, not Glob
- [ ] File-by-path searches (by name, extension, or directory) use Glob, not Grep
- [ ] Edit is the default for file modifications; Read + Write is treated as a last resort
- [ ] On a non-unique Edit match, `old_string` is widened with more context (or `replace_all: true` is used) rather than falling back to Read + Write
- [ ] Codebase exploration is incremental — Grep to find entry points, Read to trace flows, Grep to trace usage — with no reading of all files upfront
- [ ] Function-usage traces follow re-exports and barrel files, not just the original function name
- [ ] Deprecation-style searches run Grep, then Glob for sibling test files, then Grep again for wrapper names — never Glob first

## Sources

- [Claude Code Documentation — Built-in Tools](https://code.claude.com/docs/en/tools) — Anthropic
- [Building with Claude API — Anthropic](https://platform.claude.com/docs/en/build-with-claude) — Anthropic
