# Cache layer

| # | Phase | Doc | Depends on |
|---|---|---|---|
| 1 | Schema | `phase-1.md` | — |
| 2 | Wiring | `phase-2.md` | 1 |

## Decisions

- **D-1** (cheap, decided) — Which key shape? → orders/line-items id
- **D-2** (expensive, undecided) — Invalidate on write or on read? → undecided (reason: every writer has to change)
- **D-2** (cheap, decided) — a duplicate id → ignored
- not a decision line
