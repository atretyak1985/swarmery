# code-search — query patterns and worked examples

Placeholders: `<mainApp>` = `project.json → mainApp`; `<device>` = `project.json → device`; `<infrastructure-repo>` = the project's infrastructure/deployment repo.

## Grep Patterns by Repository

### Main app (TypeScript)
```
Grep("getDb\\(\\)", glob: "*.ts", path: "apps/<mainApp>/src")
Grep("export default|export function", glob: "*.tsx", path: "apps/<mainApp>/src/app")
Grep("EventSource|useTelemetry", glob: "*.{ts,tsx}", path: "apps/<mainApp>/src")
```

### Device repo (Python)
```
Grep("async def", glob: "*.py", path: "<device>/src")
Grep("telemetry", glob: "*.py", path: "<device>")
```

### Deployment config
```
Grep("image:", glob: "*.yaml", path: "<infrastructure-repo>/charts")
Grep("nodePort:", glob: "*.{yaml,tpl}", path: "<infrastructure-repo>")
```

### Cross-Repo

A bare `path: "."` sweeps every repo, including `node_modules`/`vendor`/`dist`/`build`, and is the
most common way to hit ripgrep's own timeout (see `search-procedure.md` → Mode 4). Always pair a
repo-root search with a `glob` that excludes generated/dependency trees, or scope to the specific
repos from `.claude/project.json` instead:

```
Grep("DeviceService", path: ".", glob: "!**/{node_modules,vendor,dist,build,.git}/**")
Grep("/ws/", path: ".", glob: "*.{py,ts,yaml,tpl}")
```

If the repo list is small, prefer searching each repo explicitly over a single unscoped call:

```
Grep("DeviceService", path: "apps/<mainApp>")
Grep("DeviceService", path: "<device>")
Grep("DeviceService", path: "<infrastructure-repo>")
```

## Glob Patterns

```
# API route handlers
Glob("apps/<mainApp>/src/app/api/**/route.ts")

# React components
Glob("apps/<mainApp>/src/components/**/*.tsx")

# Test files
Glob("<device>/test/**/*.py")
Glob("apps/<mainApp>/src/**/*.test.{ts,tsx}")

# Deployment values
Glob("<infrastructure-repo>/**/values*.yaml")

# Database migrations
Glob("<infrastructure-repo>/files/backendMigration/*.sql")
```

## Worked example: finding all uses of the telemetry emitter

**Input:** `query: "telemetryEmitter"`, `search_type: "exact"`, `scope: "apps/<mainApp>/src/"`

**Step 1:** Classify as exact symbol lookup -> Grep

**Step 2:** Execute
```
Grep("telemetryEmitter", glob: "*.ts", path: "apps/<mainApp>/src")
```

**Results:**
```
apps/<mainApp>/src/lib/telemetry/ws-client.ts:5  export const telemetryEmitter = new EventEmitter();
apps/<mainApp>/src/lib/telemetry/ws-client.ts:12   telemetryEmitter.emit(`telemetry:${deviceId}`, parsed.data);
apps/<mainApp>/src/app/api/telemetry/stream/route.ts:2  import { telemetryEmitter } from '@/lib/telemetry/ws-client';
apps/<mainApp>/src/app/api/telemetry/stream/route.ts:11   telemetryEmitter.on(`telemetry:${deviceId}`, handler);
apps/<mainApp>/src/app/api/telemetry/stream/route.ts:14     telemetryEmitter.off(`telemetry:${deviceId}`, handler);
```

**Summary:** 5 occurrences in 2 files. Defined in `ws-client.ts:5`, consumed in `stream/route.ts`.

## Worked example: semantic search for a feature flow

**Input:** `query: "How does order creation work end to end?"`, `search_type: "semantic"`

**Step 1:** Classify as semantic question -> codebase-retrieval

**Step 2:** Execute
```
codebase-retrieval("How does order creation work in the main app, from route handler to database?", directory_path: "apps/<mainApp>")
```

**Step 3:** Verify top 2 results with Read — confirmed `src/app/api/orders/route.ts` exists and contains POST handler at returned line number.

**Results:** codebase-retrieval returns relevant snippets from:
- `src/app/api/orders/route.ts` (POST handler)
- `src/lib/db/schema.ts` (orders table definition)
- `src/app/(dashboard)/orders/new/page.tsx` (order creation form)
