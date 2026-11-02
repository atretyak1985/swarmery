---
name: uses-helpers
description: Fixture skill for skill_missing_helper — one present and several missing helper references.
---

# Uses helpers

Read [the present resource](resources/present.md#top) first, then `resources/missing.md`.

Run `scripts/run.sh` (shipped) and `scripts/absent.sh` (not shipped). The working
repo's `bin/consumer.sh` is not this skill's file: the skill ships no bin/.

Run `bash ${CLAUDE_PLUGIN_ROOT}/bin/tool.sh --check` for the shipped tool.

Ignored shapes: [the docs](https://example.com/docs/guide.md), `resources/<name>.md`,
`templates/{kind}.md`, ![inline](data:image/png;base64,AAAA), `scripts/`, [anchor](#uses-helpers).

```bash
bash ${CLAUDE_PLUGIN_ROOT}/bin/gone.sh
bash scripts/also-not-judged.sh
```
