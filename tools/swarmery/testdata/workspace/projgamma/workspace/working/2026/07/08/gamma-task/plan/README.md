# Gamma epic

Objective: exercise the epic parser with a real phase-sequencing table.

## Phase sequencing

| # | Phase | Doc | Repo area | Depends on | Parallel? | Est. |
|---|---|---|---|---|---|---|
| 1 | Schema + write API | `phase-1-schema.md` | daemon | — | with 2 | 1 d |
| 2 | Parser | `phase-2-parser.md` | daemon | 1 | — | 1 d |
| 3 | UI surface | `phase-3-ui.md` | web | 1, 2 | — | 2 d |

**Critical path:** 1 → 2 → 3.
