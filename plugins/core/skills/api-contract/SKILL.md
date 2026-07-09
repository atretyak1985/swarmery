---
name: api-contract
description: "Verify field-level alignment between the ORM schema, Zod validation schemas, and route handler response shapes in the main app (project.json -> mainApp), or check that SQL migrations match the ORM table definitions. Don't use it for writing new API routes (use api-integration), reviewing code style or naming (use code-standards), or auditing function length and complexity (use code-quality)."
version: "1.0.0"
owner: "agentry-core"
allowed-tools: Read, Grep, Glob
disable-model-invocation: true
---

# Purpose

Detects field-name mismatches, type mismatches, and nullable/optional inconsistencies across the three layers that define an API entity in the main app: the ORM schema (`apps/<mainApp>/src/lib/db/schema.ts`), the Zod validation schema (co-located with the route or in `src/lib/`), and the route handler's JSON response shape (`src/app/api/**/route.ts`). It also verifies that the ORM table definitions stay in sync with the SQL migrations kept in the infrastructure repo's migrations directory (check `CLAUDE.md` / project.json -> `repos` for the exact path). Examples below use Drizzle ORM and Flyway-style SQL migrations -- adapt to the project's actual ORM and migration tool (project.json -> `stack.db`).

# When to use / When NOT to use

**Use when:**
- An ORM schema table or column was added, renamed, or had its type changed
- A new Zod schema was added or an existing one was modified for request/response validation
- A route handler's returned JSON shape was changed or a new route handler was created
- A SQL migration added/altered a column and the ORM schema needs verification

**Do NOT use when:**
- Writing a new API route handler from scratch (use `api-integration`)
- Reviewing code for style, naming, or function length (use `code-standards` or `code-quality`)
- Running a database migration without any schema alignment concern
- Refactoring a React component that does not consume or produce API data

**Near-miss disambiguation:**
- "A schema naming convention looks wrong" -- if the issue is a naming *convention* violation (e.g., camelCase vs PascalCase preference), route to `code-standards`. If the issue is a field name that *differs between layers* (the ORM says `model_id`, Zod says `modelId`), this skill owns it.
- "Check whether the Zod schema has the right types" -- if you are comparing Zod types *against the ORM schema or the SQL migrations*, this skill. If you are checking whether `any` types exist or whether type annotations are missing, route to `code-standards`.

# Required environment

- Runtime: `.claude/skills/api-contract/SKILL.md`
- Tools: Read, Grep, Glob (no writes -- this is a read-only verification skill)
- Frontmatter flag `disable-model-invocation: true` keeps this skill user/agent-explicit (it does not auto-trigger on matching phrases). As a knowledge module it never dispatches subagents in any case; depth control lives at the agent layer (see ARCHITECTURE.md -> Delegation depth).
- File system assumptions (verify against the live repo):
  - `apps/<mainApp>/src/lib/db/schema.ts` exists and defines the ORM tables
  - `apps/<mainApp>/src/app/api/**/route.ts` contains route handlers
  - The infrastructure repo contains the SQL migration files (`*.sql`)
  - Migration state varies per environment; this skill compares against the latest migration file on disk, not a running database.

# Inputs

- `entity: string` -- the entity name to verify (e.g., "device", "mission", "place")
- `scope: "single-entity" | "full-scan"` -- whether to check one entity or all entities

# Outputs

- **Format:** Markdown report following the output template below
- **Length budget:** one report section per entity checked; max 500 lines for a full-scan
- **Downstream handoff:** When findings exist, produce a structured summary block at the end of the report in the format `CONTRACT-ISSUES: {entity} | CRITICAL: {n} | HIGH: {n} | MEDIUM: {n} | LOW: {n}` for consumption by CI gates or downstream agents. Fixes require `@implementation-agent` or a developer; this skill does not auto-fix.

## Output template

```markdown
## API Contract Report for "{entity}"

### Route Handler DTO (main app)
[Route path, file:line, Zod schema name, response shape with field list]

### Shared / Client Type (main app)
[Interface name, file:line, fields with types]

### Database Schema
[SQL migration file:line + ORM schema file:line, column list]

### Issues Found
| # | Severity | Location | Issue | Fix |
|---|----------|----------|-------|-----|

### Action Required
[Prioritized fixes with owner assignment]

### Summary
CONTRACT-ISSUES: {entity} | CRITICAL: {n} | HIGH: {n} | MEDIUM: {n} | LOW: {n}
```

# Procedure

1. **Locate the ORM table definition** -- Grep for the entity table name in `apps/<mainApp>/src/lib/db/schema.ts`. Record every column name, SQL type, and nullable flag. Checkpoint: at least one table found, or stop and report "entity not found in ORM schema."

2. **Locate the Zod schema(s)** -- Grep for Zod schemas (e.g., `z.object`, `createInsertSchema`, `createSelectSchema`) referencing the entity in `apps/<mainApp>/src/`. Record every field name, Zod type, and optional/nullable modifier. Checkpoint: if no Zod schema exists, flag as finding (severity: High -- "No input validation for entity X").

3. **Locate the route handler(s)** -- Glob for `apps/<mainApp>/src/app/api/{entity}/**/route.ts`. Read each handler and extract the JSON response shape (fields returned in `Response.json()`). Checkpoint: at least one route handler found, or note "no route handler for entity X."

4. **Locate the SQL migration** -- Grep for `CREATE TABLE` or `ALTER TABLE` referencing the entity in the infrastructure repo's migration files (`*.sql`). Record column names and SQL types. Checkpoint: migration found, or note "no SQL migration for entity X" (may be a new entity not yet migrated).

5. **Cross-compare all four sources** -- For each field, verify:
   - Column name matches across the ORM schema, Zod, route handler, and SQL migration
   - Type is compatible (e.g., `integer` in SQL = `integer()` in the ORM = `z.number()` in Zod = `number` in JSON)
   - Nullable/optional treatment is consistent (nullable in DB = `.nullable()` in Zod = `field?: T | null` in response)
   - Device-protocol-sourced fields (if any) keep the protocol's naming convention (often UPPER_SNAKE_CASE) consistently through the pipeline
   Checkpoint: comparison table complete for all fields.

6. **Produce the report** -- Fill in the output template with all findings. Each finding must include `file:line` citation, severity, and a specific fix recommendation. Findings below 80% confidence must be marked `[LOW-CONFIDENCE]`. Checkpoint: report filled with zero placeholder text.

7. **Final acceptance check** -- Verify the report has zero placeholder text (`[TODO]`, `[TBD]`, empty cells), every finding has a `file:line` citation, severity levels follow the defined criteria, and the summary line is populated.

# Self-check before returning

- [ ] Every finding cites a specific `file:line` location
- [ ] Every finding has a severity level (Critical / High / Medium / Low) assigned per the criteria below
- [ ] No finding below 80% confidence is emitted without the `[LOW-CONFIDENCE]` marker
- [ ] The ORM schema was compared field-by-field against at least one other source (Zod, route handler, or SQL migration)
- [ ] Device-protocol-sourced fields were checked for naming-convention consistency (e.g., UPPER_SNAKE_CASE)
- [ ] The report contains zero placeholder text (no `[TODO]`, `[TBD]`, or empty cells)
- [ ] The `CONTRACT-ISSUES:` summary line is present at the end of the report

# Common mistakes to avoid

- DO NOT report a Zod `.optional()` as a mismatch when the ORM column has a `.default()` -- both make the field non-required on insert
- DO NOT assume device-protocol field names should be camelCase -- wire-protocol fields often use UPPER_SNAKE_CASE and must stay that way through the telemetry pipeline
- DO NOT flag migration-tool bookkeeping tables (e.g., `flyway_schema_history`) as a missing ORM table
- DO NOT emit findings for vendored or auto-generated files
- DO NOT confuse a naming *convention* issue (owned by `code-standards`) with a field name *mismatch between layers* (owned by this skill)

# Escalation

- **Stop and ask when:** an ORM column type has no obvious Zod equivalent (e.g., custom pgEnum not mapped)
- **Stop and ask when:** more than 10 Critical findings are detected in a single entity (likely a structural mismatch requiring architectural review)
- **Refuse and explain when:** asked to auto-fix contract mismatches (this is a verification-only skill; fixes require `@implementation-agent`)

# Severity criteria

- **Critical** -- field exists in one layer but not another, causing runtime errors (e.g., route returns field X but the ORM schema has no column X)
- **High** -- type mismatch that would cause data corruption or validation failure (e.g., `string` in Zod but `integer` in the ORM)
- **Medium** -- nullable/optional inconsistency that could cause unexpected nulls in the UI
- **Low** -- naming style inconsistency that does not affect runtime behavior

# Examples

<example>
## Worked example: verifying the `device` entity

**Step 1: ORM schema** (`apps/<mainApp>/src/lib/db/schema.ts:45`, Drizzle shown)
```typescript
export const devices = backendSchema.table('device', {
  id: serial('id').primaryKey(),
  name: varchar('name', { length: 100 }).notNull(),
  model_id: integer('model_id').references(() => deviceModels.id),
  active: boolean('active').notNull().default(true),
  fleet_id: integer('fleet_id').references(() => fleets.id),
});
```

**Step 2: Zod schema** (`apps/<mainApp>/src/lib/validations/device.ts:8`)
```typescript
export const deviceSchema = z.object({
  id: z.number(),
  name: z.string().min(1).max(100),
  modelId: z.number().nullable(),  // camelCase vs snake_case mismatch
  active: z.boolean(),
  fleetId: z.number().nullable(),
});
```

**Step 3: Route handler** (`apps/<mainApp>/src/app/api/devices/route.ts:12`)
```typescript
export async function GET() {
  const session = await auth();
  if (!session) return Response.json({ error: 'Unauthorized' }, { status: 401 });
  const allDevices = await getDb().select().from(devices);
  return Response.json(allDevices);  // Returns snake_case from the ORM
}
```

**Step 4: SQL migration** (infrastructure repo, `V3__create_device.sql:1`)
```sql
CREATE TABLE backend.device (
  id SERIAL PRIMARY KEY,
  name VARCHAR(100) NOT NULL,
  model_id INTEGER REFERENCES backend.device_model(id),
  active BOOLEAN NOT NULL DEFAULT TRUE,
  fleet_id INTEGER REFERENCES backend.fleet(id)
);
```

**Report:**

## API Contract Report for "device"

### Issues Found

| # | Severity | Location | Issue | Fix |
|---|----------|----------|-------|-----|
| 1 | Medium | `src/lib/validations/device.ts:5` | Zod uses `modelId` (camelCase) but the ORM/SQL use `model_id` (snake_case). Route handler returns snake_case from the ORM directly. | Align Zod field name to `model_id` or add a transform layer in the route handler. |
| 2 | Medium | `src/lib/validations/device.ts:7` | Zod uses `fleetId` (camelCase) but the ORM/SQL use `fleet_id` (snake_case). | Same fix as #1 -- align naming convention. |
| 3 | Low | `src/app/api/devices/route.ts:15` | Route handler returns raw ORM result without Zod validation on the output shape. | Wrap response in `deviceSchema.array().parse(allDevices)` or document that output validation is intentionally skipped. |

### Action Required
1. Decide on naming convention (camelCase vs snake_case) for API responses and align Zod + ORM accordingly
2. Add output validation or document its intentional absence

### Summary
CONTRACT-ISSUES: device | CRITICAL: 0 | HIGH: 0 | MEDIUM: 2 | LOW: 1
</example>

# Failure modes

- **Mode 1:** ORM schema uses a custom `pgEnum` not found in the SQL migrations -- detect by checking if the enum type exists in migration files; fix by flagging as Critical and requesting migration review
- **Mode 2:** Route handler uses `res.json()` with a spread operator mixing multiple query results -- detect by reading the handler for `...` spread patterns; fix by tracing each spread source to verify all fields are intentional
- **Mode 3:** Zod schema imports from a barrel file that re-exports modified types -- detect by following import chains; fix by reading the actual source file rather than the barrel export

# Related skills

- `code-standards` -- defer to this skill for coding convention violations (naming, type safety, style); compose when a contract issue overlaps with a naming convention issue
- `api-integration` -- defer to this skill for implementing new API routes or integration patterns; compose when a contract check reveals a missing integration pattern
- `code-quality` -- defer to this skill for function length, complexity, and code smell audits; the contract check is narrower and field-level specific
