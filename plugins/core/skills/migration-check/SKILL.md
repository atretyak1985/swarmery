---
name: migration-check
description: "Validate SQL migration safety, check ORM-to-migration schema alignment, review new migration SQL files, or detect schema drift between the database and application Zod/DTO types. Do not use for deployment config validation (use deployment), identity-provider database issues (use the auth-domain skill), or IaC drift detection (use infrastructure-as-code)."
version: "1.0.0"
owner: "agentry-core"
disable-model-invocation: true
allowed-tools: Read, Grep, Glob, Bash
---

# Purpose

Validate database migration safety and schema consistency for the project's PostgreSQL database. Check migration SQL files for safety issues, verify alignment between the migration-managed schema, the ORM schema in the main app (project.json -> `mainApp`), and Zod/DTO validation types consumed by route handlers. Examples below use Flyway-style migrations and Drizzle ORM -- adapt to the project's actual tools (`CLAUDE.md`, project.json -> `stack.db`). For post-incident SQL data fixes that need to be captured as migrations, see `infrastructure-as-code` skill. For field-level alignment between the ORM, Zod, and route handler inputs beyond what this skill covers, see `api-contract`.

DO NOT use Java Entity terminology anywhere in reports or output unless the project actually has Java services. The TypeScript validation layer is called "Zod/DTO".

# When to use

- Reviewing new migration SQL files before applying
- Checking alignment between SQL migrations and the ORM schema after a schema change
- Validating migration safety (reversibility, index concurrency, NOT NULL defaults)
- Detecting drift between the database schema and TypeScript Zod/DTO types in the main app
- Pre-deploy migration review as part of a release checklist

# When NOT to use

- Deployment config validation or deploying -- use `deployment`
- Identity-provider database issues (e.g., an IdP's own `user_access` schema) -- use the auth-domain skill
- IaC config drift or post-incident capture -- use `infrastructure-as-code`
- PostgreSQL pod health or connection issues -- use `deployment` / `troubleshooting`

# Required environment

- Runtime: `.claude/skills/migration-check/SKILL.md`
- Tools: Read, Grep, Glob, Bash
- SQL migrations: the infrastructure repo's migrations directory (check `CLAUDE.md` / project.json -> `repos` for the exact path)
- ORM schema: `apps/<mainApp>/src/lib/db/schema.ts`
- Schema name: per project convention (the examples below use `backend`)

# Inputs

- `migration_file: string` (optional) -- specific migration file to review
- `scope: enum` -- one of: `safety-check`, `schema-alignment`, `full-audit`

# Outputs

- Format: Migration Report in markdown (see template in Examples)
- Length budget: max 60 lines for `safety-check`; max 120 lines for `full-audit`. For large migration sets (>10 files), summarize passing files as one row per batch.
- The report includes: migration file inventory, safety assessment, schema alignment table (columns: Migration, ORM, Zod/DTO), and recommendations

# Procedure

1. **Discover migration files** -- Run `ls` on the project's migrations directory to find all migration files. Do NOT rely on any static table for current state.
   **Checkpoint:** File list obtained from disk.

2. **Safety check each migration** -- For each migration file (or the specific one under review), apply the six safety checks below.
   **Checkpoint:** All checks evaluated.

3. **Read the ORM schema** -- Read `apps/<mainApp>/src/lib/db/schema.ts` to get the application-side schema.
   **Checkpoint:** Schema file read successfully.

4. **Compare alignment** -- Verify each table/column in the SQL migrations has a matching ORM definition and corresponding Zod/DTO type. Use the column label "Zod/DTO" -- never "Java Entity".
   **Checkpoint:** Alignment table populated with Migration, ORM, and Zod/DTO columns.

5. **Produce report** -- Fill in the Migration Report template.
   **Checkpoint:** Report complete and within length budget.

### Safety checks for each migration

- [ ] **Reversibility**: Can this migration be rolled back? Are there DROP statements without backup?
- [ ] **Data safety**: No destructive operations (DROP TABLE, TRUNCATE) without explicit backup step
- [ ] **Index safety**: Large table indexes use `CREATE INDEX CONCURRENTLY` to avoid locks
- [ ] **Constraints**: New NOT NULL columns have `DEFAULT` values to avoid breaking existing rows
- [ ] **Naming**: Follows `V{major}.{minor}.{patch}__{description}.sql` convention
- [ ] **Idempotency**: Uses `IF NOT EXISTS` / `IF EXISTS` guards
- [ ] **Ordering**: Version numbers are sequential with no duplicate or conflicting versions across branches
- [ ] **Indexes**: Columns used in frequent queries (per ORM query usage in the main app) have indexes -- flag missing indexes as a recommendation

# Self-check

- [ ] I discovered migration files from disk (`ls`), not from a hardcoded table
- [ ] I read the actual ORM schema file, not assumed its contents
- [ ] The report uses "Zod/DTO" column, NOT "Java Entity" (unless the project has Java services)
- [ ] I used the full path `apps/<mainApp>/src/lib/db/schema.ts` for the ORM schema
- [ ] I flagged any migration that modifies an already-applied migration file
- [ ] I checked for `CONCURRENTLY` on index creation for tables with existing data

# Common mistakes

- DO NOT rely on a static "Known Migrations" table for current state -- always read the migration directory from disk
- DO NOT use "Java Entity" in reports for a TypeScript stack -- the validation layer is "Zod/DTO"
- DO NOT suggest modifying an already-applied migration -- create a new migration instead
- DO NOT approve a migration that adds NOT NULL without DEFAULT on a table with existing data
- DO NOT mix DDL (schema changes) and DML (data changes) in a single migration file

# Escalation

- STOP when: a migration contains `DROP TABLE` or `TRUNCATE` on a production table
- STOP when: the ORM schema file cannot be found or is significantly out of sync (5+ columns missing)
- STOP when: a migration has a migration-tool state of `FAILED` (partially applied) -- for partially applied migrations, if migration is idempotent, advise a repair command (e.g., `flyway repair`) to clear the failed state; if not idempotent, advise restore from backup before re-applying

# Examples

## Example: Migration safety review

Reviewing `V1.0.4__add_mission_status.sql`:

```sql
-- V1.0.4__add_mission_status.sql
ALTER TABLE backend.mission
  ADD COLUMN status VARCHAR(20) NOT NULL DEFAULT 'DRAFT';

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_mission_status
  ON backend.mission (status);
```

**Assessment:**
- Reversibility: YES -- `ALTER TABLE DROP COLUMN` can undo
- Data safety: PASS -- no destructive operations
- Index safety: PASS -- uses `CONCURRENTLY`
- Constraints: PASS -- NOT NULL has DEFAULT 'DRAFT'
- Naming: PASS -- follows `V{version}__{description}.sql`
- Idempotency: PARTIAL -- index uses `IF NOT EXISTS` but column does not

**Before (not idempotent):**
```sql
ALTER TABLE backend.mission ADD COLUMN status VARCHAR(20) NOT NULL DEFAULT 'DRAFT';
```

**After (idempotent):**
```sql
ALTER TABLE backend.mission ADD COLUMN IF NOT EXISTS status VARCHAR(20) NOT NULL DEFAULT 'DRAFT';
```

## Example: Schema alignment report

```markdown
## Migration Report

**Database:** PostgreSQL (schema: backend)
**Migration Tool:** Flyway
**ORM:** Drizzle (apps/<mainApp>)

### Migration Files
| File | Status | Safe | Notes |
|------|--------|------|-------|
| V1.0.1__create_backend_schema.sql | Applied | YES | Creates all initial tables |
| V1.0.2__create_indexes.sql | Applied | YES | Index creation |

### Schema Alignment
| Table | Migration | ORM | Zod/DTO | Issues |
|-------|-----------|-----|---------|--------|
| settings | YES | YES | YES | None |
| mission | YES | YES | MISSING | No Zod schema for mission input validation |

### Recommendations
- Add Zod input validation schema for mission creation in the main app's route handler
```

# Failure modes

| Mode | Symptom | Fix |
|------|---------|-----|
| ORM schema out of sync | TypeScript type errors on column that exists in the migrations but not the ORM | Add missing column to the ORM schema |
| NOT NULL without DEFAULT | Migration fails with `column contains null values` | Add DEFAULT value or run data backfill in separate migration first |
| Modified applied migration | Migration-tool checksum mismatch error on deploy | Never modify applied migrations; create new migration |
| Missing Zod/DTO validation | Invalid data accepted by route handler causing DB constraint violation | Add Zod input schema in route handler |
| Partially applied migration | Migration tool shows FAILED state with checksum mismatch | If idempotent: run the tool's repair command; if not: restore from backup |

# Related skills

- `api-contract` -- compose when schema alignment check reveals a Zod/DTO field mismatch; api-contract validates field-level alignment between the ORM, Zod, and route handler inputs beyond what this skill covers
- `infrastructure-as-code` -- defer for post-incident SQL fix capture; compose when a manual psql fix needs to become a migration
- `deployment` -- no direct overlap; migrations are not deploy-managed, but the migration tool typically runs via an init container configured in the deployment values
- auth-domain skill -- no overlap; the identity provider manages its own database; this skill covers the application schema only
