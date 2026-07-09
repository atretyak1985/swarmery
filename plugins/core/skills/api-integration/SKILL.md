---
name: api-integration
description: "Implement REST route handlers, ORM queries, WebSocket telemetry connections, or SSE streaming endpoints in the main web app (project.json -> mainApp), or consume those APIs from client components. Do not use for schema-only changes, deployment/infrastructure config work, or device firmware development."
version: "1.0.0"
owner: "agentry-core"
allowed-tools: Read, Write, Grep, Glob
---

# Purpose

Produce tested integration code for connecting the layers of the platform: REST route handlers in the main app (project.json -> `mainApp`), ORM database queries, server-side WebSocket clients that subscribe to device/edge telemetry (project.json -> `device`), and SSE endpoints that fan out real-time data to browsers. All generated code follows the project's conventions (lazy DB init, Auth.js session checks, Zod validation at boundaries). The code patterns below are illustrated with Drizzle ORM -- adapt them to the project's actual ORM (check `CLAUDE.md` and project.json -> `stack.db`).

# When to use

- Implementing a new API route handler (`src/app/api/**/route.ts`) in the main app
- Writing an ORM query (select, insert, update, delete) in a Server Component or route handler
- Connecting the main app to a device/edge WebSocket endpoint for telemetry ingestion
- Creating or modifying an SSE streaming endpoint or a client-side `EventSource` hook

# When NOT to use

- Modifying the ORM schema definition itself without implementing queries (use `api-contract`)
- Writing or modifying deployment manifests or infrastructure code (use `deployment`)
- Writing firmware/edge code for the device repo (use the device-domain skills from the project's enabled packs)
- Reviewing existing code for style or quality (use `code-standards` or `code-quality`)
- Adding observability instrumentation (metrics, tracing) to an endpoint (compose with `observability`)

# Required environment

- Runtime: `.claude/skills/api-integration/SKILL.md`
- Tools: Read, Write, Grep, Glob
- File system assumptions (verify against the live repo; paths follow the `apps/<mainApp>/` convention):
  - `apps/<mainApp>/` contains the Next.js application
  - `apps/<mainApp>/src/lib/db/index.ts` exports `getDb()` with lazy initialization
  - `apps/<mainApp>/src/lib/db/schema.ts` defines the ORM tables (schema namespace per project conventions)
  - `apps/<mainApp>/src/lib/auth.ts` exports the `auth()` function (Auth.js v5)

# Inputs

- `task: string` -- description of the integration to implement
- `entity: string` -- the ORM table/entity involved (e.g., "missions", "devices")
- `integration_type: "rest" | "orm" | "websocket" | "sse"` -- which layer to implement

# Outputs

- Format: TypeScript source files written to the appropriate location in `apps/<mainApp>/src/`
- Length budget: each route handler file under 80 lines; each hook under 50 lines
- Template: file path + integration pattern applied + assumptions noted

# Procedure

1. **Identify integration type** -- Determine whether the task requires a REST route, ORM query, WebSocket connection, or SSE endpoint.
   **Checkpoint:** Integration type confirmed before any code generation.

2. **Verify existing patterns** -- Grep/Glob for existing implementations of the same integration type in `apps/<mainApp>/src/`.
   **Checkpoint:** At least one reference implementation found, or note the pattern is new.

3. **Verify the ORM schema** -- Read `apps/<mainApp>/src/lib/db/schema.ts` to confirm table and column names.
   **Checkpoint:** Table exists in schema. If the table is not found or column names are ambiguous, STOP and ask the user. Do not write code based on a guessed schema.

4. **Confidence gate** -- Before writing any file, confirm: (a) schema verified, (b) target file path determined, (c) integration pattern selected. If any element is uncertain, STOP and ask.
   **Checkpoint:** All three conditions met.

5. **Pre-write existence check** -- Glob the target file path. If the file already exists, Read it first and present a summary of what will change. Do not overwrite without informing the user.
   **Checkpoint:** User informed of overwrites, or file confirmed new.

6. **Implement the integration** -- Write the code following the patterns below. Apply these rules:
   - Always use `getDb()` for database access (never eager init)
   - Always check `await auth()` in authenticated route handlers and return 401 on missing session
   - Always validate external input with Zod before processing
   - Always use `export const dynamic = 'force-dynamic'` on routes that read session or environment
   - Always clean up EventSource and WebSocket connections in `useEffect` return
   - Use `getServerEnv()` for environment-specific configuration (never hardcode URLs)
   **Checkpoint:** File written.

7. **Verify the implementation** -- Read the written file back and confirm it follows all six rules above.
   **Checkpoint:** File follows all rules; no obvious type errors.

8. **Post-write contract check** -- Run `api-contract` skill to verify field alignment across layers (ORM schema, route handler, Zod types).
   **Checkpoint:** Field alignment verified or mismatches flagged.

# Key patterns

## Pattern 1: REST Route Handler (CRUD)

```typescript
// src/app/api/devices/route.ts
import { auth } from '@/lib/auth';
import { getDb } from '@/lib/db';
import { devices } from '@/lib/db/schema';

export const dynamic = 'force-dynamic';

export async function GET() {
  const session = await auth();
  if (!session) return Response.json({ error: 'Unauthorized' }, { status: 401 });
  const allDevices = await getDb().select().from(devices);
  return Response.json(allDevices);
}
```

## Pattern 2: ORM Queries (Drizzle shown)

```typescript
import { getDb } from '@/lib/db';
import { devices, missions, deviceMissions } from '@/lib/db/schema';
import { eq } from 'drizzle-orm';

const activeDevices = await getDb().select().from(devices).where(eq(devices.active, true));

const missionWithDevices = await getDb()
  .select().from(missions)
  .leftJoin(deviceMissions, eq(missions.id, deviceMissions.missionId))
  .leftJoin(devices, eq(deviceMissions.deviceId, devices.id))
  .where(eq(missions.id, missionId));

const [newMission] = await getDb()
  .insert(missions).values({ name: 'Patrol Alpha', type: 'BY_ROUTE' }).returning();
```

## Pattern 3: Lazy Database Initialization

```typescript
// src/lib/db/index.ts -- reference only, do not recreate
let db: ReturnType<typeof drizzle<typeof schema>> | null = null;
declare global { var __db: ReturnType<typeof drizzle<typeof schema>> | undefined; }
export function getDb() {
  if (!db) {
    const pool = new Pool({ connectionString: process.env.DATABASE_URL });
    db = drizzle(pool, { schema });
    if (process.env.NODE_ENV === 'development') globalThis.__db = db;
  }
  return db;
}
```

## Pattern 4: WebSocket Telemetry (Server-Side)

```typescript
// src/lib/telemetry/ws-client.ts
import WebSocket from 'ws';
import { EventEmitter } from 'events';
import { z } from 'zod';

export const telemetryEmitter = new EventEmitter();

// Example GPS/IMU telemetry shape -- replace fields with the project's device telemetry contract
const TelemetrySchema = z.object({
  LATITUDE: z.number(), LONGITUDE: z.number(), ALTITUDE: z.number(),
  RELATIVE_ALTITUDE: z.number(), HEADING: z.number(), GROUND_SPEED: z.number(),
  VERTICAL_SPEED: z.number(), BATTERY_REMAINING: z.number(), SYSTEM_STATUS: z.number(),
  DEVICE_MODE: z.string(), GPS_FIX_TYPE: z.number(), SATELLITES_VISIBLE: z.number(),
  ROLL: z.number(), PITCH: z.number(), YAW: z.number(),
});

export function connectToDevice(deviceId: string, wsUrl: string) {
  const ws = new WebSocket(wsUrl);
  ws.on('message', (data: Buffer) => {
    const parsed = TelemetrySchema.safeParse(JSON.parse(data.toString()));
    if (!parsed.success) { console.error(`Invalid telemetry from ${deviceId}:`, parsed.error.message); return; }
    telemetryEmitter.emit(`telemetry:${deviceId}`, parsed.data);
  });
  ws.on('close', () => { setTimeout(() => connectToDevice(deviceId, wsUrl), 3000); });
  ws.on('error', (err) => { console.error(`WS error for ${deviceId}:`, err.message); });
  return ws;
}
```

## Pattern 5: SSE Streaming (Server)

```typescript
// src/app/api/telemetry/stream/route.ts
import { telemetryEmitter } from '@/lib/telemetry/ws-client';
export const dynamic = 'force-dynamic';

export async function GET(req: Request) {
  const deviceId = new URL(req.url).searchParams.get('deviceId');
  const stream = new ReadableStream({
    start(controller) {
      const handler = (data: unknown) => { controller.enqueue(`data: ${JSON.stringify(data)}\n\n`); };
      telemetryEmitter.on(`telemetry:${deviceId}`, handler);
      req.signal.addEventListener('abort', () => { telemetryEmitter.off(`telemetry:${deviceId}`, handler); controller.close(); });
    },
  });
  return new Response(stream, { headers: { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache', Connection: 'keep-alive' } });
}
```

## Pattern 6: Browser EventSource Hook

```typescript
// src/hooks/useTelemetry.ts
'use client';
import { useEffect, useRef, useState } from 'react';

export function useTelemetry(deviceId: string) {
  const [telemetry, setTelemetry] = useState<Telemetry | null>(null);
  const [connected, setConnected] = useState(false);
  const retryRef = useRef<ReturnType<typeof setTimeout>>();
  useEffect(() => {
    let es: EventSource;
    function connect() {
      es = new EventSource(`/api/telemetry/stream?deviceId=${deviceId}`);
      es.onopen = () => setConnected(true);
      es.onmessage = (event) => setTelemetry(JSON.parse(event.data));
      es.onerror = () => { setConnected(false); es.close(); retryRef.current = setTimeout(connect, 3000); };
    }
    connect();
    return () => { es?.close(); if (retryRef.current) clearTimeout(retryRef.current); };
  }, [deviceId]);
  return { telemetry, connected };
}
```

# Self-check

- [ ] Every route handler uses `getDb()` (never eager DB init)
- [ ] Every authenticated route checks `await auth()` and returns 401 on missing session
- [ ] Every route reading session or env has `export const dynamic = 'force-dynamic'`
- [ ] All external data (WebSocket messages, request bodies) validated with Zod
- [ ] No hardcoded hostnames or URLs -- all from `getServerEnv()` or runtime env
- [ ] All `useEffect` hooks opening EventSource/WebSocket return a cleanup function
- [ ] ORM queries use correct table/column names verified from `schema.ts`
- [ ] Confidence gate passed before any file was written

# Common mistakes

- DO NOT use eager database initialization (`export const db = drizzle(pool)`) -- breaks build (no DATABASE_URL at build time)
- DO NOT hardcode edge-device hostnames like `d1.d16.local` -- use runtime config via `getServerEnv()`
- DO NOT skip Zod validation on WebSocket inbound data -- `JSON.parse` alone provides no type safety
- DO NOT use an empty `setTimeout(() => {}, 3000)` for reconnection -- pass the actual reconnect function
- DO NOT use `next/font/google` -- causes prerender failures in recent Next.js versions

# Escalation

- STOP and ask when: the ORM schema table for the target entity does not exist
- STOP and ask when: the Auth.js configuration is missing or uses an unfamiliar provider
- STOP and ask when: the task requires bidirectional WebSocket (command sending) -- no established pattern
- REFUSE when: asked to hardcode environment-specific values (URLs, hostnames, API keys) into source code

# Examples

## Worked example: GET /api/missions/:id

**Input:** "Add a route handler to get a single mission by ID with its assigned devices."

**Step 1-3:** Verify schema via Grep -> confirm `missions` table with id, name, type; confirm `deviceMissions` join table.

**Step 5:** Glob `apps/<mainApp>/src/app/api/missions/[id]/route.ts` -> file does not exist -> proceed.

**Step 6:** Write:
```typescript
// apps/<mainApp>/src/app/api/missions/[id]/route.ts
import { auth } from '@/lib/auth';
import { getDb } from '@/lib/db';
import { missions, deviceMissions, devices } from '@/lib/db/schema';
import { eq } from 'drizzle-orm';

export const dynamic = 'force-dynamic';

export async function GET(_req: Request, { params }: { params: Promise<{ id: string }> }) {
  const session = await auth();
  if (!session) return Response.json({ error: 'Unauthorized' }, { status: 401 });
  const { id } = await params;
  const missionId = parseInt(id, 10);
  if (isNaN(missionId)) return Response.json({ error: 'Invalid mission ID' }, { status: 400 });
  const result = await getDb().select().from(missions)
    .leftJoin(deviceMissions, eq(missions.id, deviceMissions.missionId))
    .leftJoin(devices, eq(deviceMissions.deviceId, devices.id))
    .where(eq(missions.id, missionId));
  if (result.length === 0) return Response.json({ error: 'Mission not found' }, { status: 404 });
  return Response.json(result);
}
```

# Failure modes

| Mode | Symptom | Fix |
|------|---------|-----|
| `getDb()` at build time | Build error: "DATABASE_URL is not defined" | Ensure `getDb()` is only called inside request handlers, never at module scope |
| SSE 401 loop | Browser console shows repeated 401 | Use cookie-based auth (Auth.js default) or pass token as query param |
| WS silent drop | Telemetry stops updating, no error in logs | Ensure `ws.on('close')` handler calls `setTimeout(reconnect, delay)` |

# Related skills

- `api-contract` -- MANDATORY post-write: verify field alignment across ORM schema, Zod, and route handlers
- `code-standards` -- defer for style and convention checks on implemented code
- `code-quality` -- defer for function length, complexity, and code smell checks
- `observability` -- compose when adding metrics or tracing to an endpoint
