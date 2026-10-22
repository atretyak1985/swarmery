# Cache layer

## In plain words

A cache in front of the orders API.

| # | Phase | Doc | Depends on |
|---|---|---|---|
| 1 | Schema | `phase-1.md` | — |
| 2 | Wiring | `phase-2.md` | 1 |

## Decisions

- **D-1** (cheap, decided) — Which key shape? → orders/line-items id
- **D-2** (cheap, recommended) — TTL default? → 5 minutes (без відповіді, рекомендований) (reason: a config value, changed in one line)
- **D-3** (expensive, decided) — Which store backs the cache? → SQLite table (reason: a data migration to undo)

```
- **D-9** (expensive, undecided) — quoted inside a fence → undecided
```

## Risks

- **D-8** (expensive, undecided) — outside the section → undecided
