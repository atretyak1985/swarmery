# agentry evals

Golden regression tests for critical core agents (promptfoo). Editing
`plugins/core/agents/*.md` must not silently break routing, output contracts,
or vendor-neutrality — these tests make such breakage fail loudly.

```bash
cd evals
export ANTHROPIC_API_KEY=…       # runner (haiku) + judge (sonnet)
npx promptfoo@latest eval        # run the suite
npx promptfoo@latest view        # inspect results in the browser
```

## What's covered (v1)
- **tech-lead** — routing sanity (DB work → database-designer, ordering), read-only
  investigation mode, and consulting `project.json`/CLAUDE.md instead of assuming a stack.
- **commit-message** — conventional format with scopes taken from `project.json → commitScopes`.
- **guardrail-checker** — APPROVED/REJECTED verdict contract + rollback for risky actions.

## Growing the corpus
Every real routing bug or contract regression should become a test case here
(same philosophy as unit tests). Prefer `contains`/`regex` for hard contracts and
`llm-rubric` for judgment calls.

## CI
Not wired into CI yet — the suite needs an API key and costs tokens. Wire it as a
manual/nightly job once the corpus stabilizes; the structural checks in
`.github/workflows/ci.yml` (JSON, bash -n, frontmatter, neutrality scan) stay on every push.
