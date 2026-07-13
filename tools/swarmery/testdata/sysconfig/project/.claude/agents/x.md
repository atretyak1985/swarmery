---
name: x
description: Project-scope agent named x — shadows the global x (agent_name_duplicate fixture).
model: claude-sonnet-5
---

Fixture body of the project agent x. Same name as the global agent x in a
different scope — override confusion the agent_name_duplicate rule flags.
The plugin agent "toolpack:x" never counts (composite name).

## Boundaries

- Fixture boundary — keeps the agent_no_boundaries lint rule quiet.
