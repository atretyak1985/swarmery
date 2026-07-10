---
name: observability
description: "Use this skill for structured logging and OpenTelemetry distributed tracing ONLY -- including log-trace-metric correlation in the project's service code. NOT for metrics or dashboards: Prometheus counters/histograms, Grafana dashboards, and alert rules belong to monitoring. NOT for Helm health probes (the project's deployment workflow / infra-pack skills when enabled)."
version: "1.0.0"
owner: "swarmery-core"
---

# Purpose

Instrument the project's services with structured logging and distributed tracing. Provide patterns for correlating logs, traces, and metrics when diagnosing issues. This skill focuses on code-level instrumentation -- what to log, how to propagate trace context, and how to correlate signals.

**Placeholders used below:** `<mainApp>` = `project.json → mainApp` (the primary application), `<device>` = `project.json → device` (the edge/device service, if the project has one). Repos and their layout come from `project.json → repos`.

# When to use

- Adding structured logging to a new or existing service (`<mainApp>`, `<device>`).
- Instrumenting code with OpenTelemetry distributed tracing.
- Correlating logs with traces to diagnose latency or error issues.
- Setting up log format standards for a new service.
- Reviewing logging practices for PII/secret leakage.

**Disambiguation -- observability vs monitoring:** If the task says "add logging," "add tracing," or "correlate logs with traces," use this skill. If it says "add metrics," "add Prometheus counters," or "create a Grafana dashboard," use `monitoring`. If investigation starts from log search or trace lookup, start here; if it starts from a Prometheus alert, start with `monitoring`.

# When NOT to use

- **Prometheus metrics, Grafana dashboards, or alert rules** -- use `monitoring`.
- **Helm liveness/readiness probes** -- part of the project's deployment workflow (infra-pack skills when enabled).
- **CI pipeline observability** (build metrics, deploy frequency) -- out of scope.
- **Infrastructure-level log routing** (Filebeat, Logstash, Loki config) -- operational concerns; this skill covers application-level instrumentation only.

# Required environment (.claude/skills/observability/SKILL.md)

- Access to the service repo(s) being instrumented -- `<mainApp>` (TypeScript) and/or `<device>` (Python 3.11+).
- OpenTelemetry SDK for the target language (if adding tracing). Check the project's deploy/charts repo for the deployed OTel collector version and match SDK version accordingly.

# Inputs

| Input | Required | Description |
|-------|----------|-------------|
| Target service | Yes | Which service to instrument (`<mainApp>`, `<device>`) |
| Instrumentation goal | Yes | Structured logging, distributed tracing, or correlation for debugging |
| Existing logging setup | No | Current logger configuration (if any) to extend rather than replace |

# Outputs

**Length budget:** Logger wrapper code should not exceed 60 lines. Tracing setup should not exceed 40 lines. Correlation guide should not exceed 20 lines.

Deliverables:
- Structured logger configuration or wrapper code.
- OpenTelemetry tracer setup and span instrumentation code.
- Correlation guide: how to find logs for a given trace ID.
- Updated code files with instrumentation added.

# Procedure

## Step 1: Define the structured log format

All the project's services emit JSON logs with these fields:

```json
{
  "timestamp": "2026-05-24T10:30:00.123Z",
  "level": "INFO",
  "service": "main-app",
  "trace_id": "abc123def456",
  "span_id": "789ghi",
  "device_id": "variable-from-runtime",
  "message": "Telemetry received from device",
  "latency_ms": 45
}
```

Required fields: `timestamp`, `level`, `service`, `message`.
Recommended fields: `trace_id`, `span_id` (for correlation), domain context fields (e.g. `device_id`, `job_id` -- see `project.json → domainTerms`).

**Checkpoint:** Log format reviewed. All four required fields present. No PII or secrets in proposed context fields.

## Step 2: Implement structured logging

**`<device>` (Python):**
```python
import logging
import json
from datetime import datetime, timezone
from typing import Literal

LogLevel = Literal["DEBUG", "INFO", "WARNING", "ERROR", "CRITICAL"]

class StructuredLogger:
    def __init__(self, service: str) -> None:
        self.service = service
        self.logger = logging.getLogger(service)

    def log(self, level: LogLevel, message: str, **context: str | int | float | bool) -> None:
        log_entry = {
            "timestamp": datetime.now(timezone.utc).isoformat(),
            "level": level,
            "service": self.service,
            "message": message,
            **context,
        }
        self.logger.log(getattr(logging, level), json.dumps(log_entry))

    def info(self, message: str, **context: str | int | float | bool) -> None:
        self.log("INFO", message, **context)

    def error(self, message: str, **context: str | int | float | bool) -> None:
        self.log("ERROR", message, **context)

logger = StructuredLogger("device-service")

# Usage -- device_id is a runtime variable, never hardcoded
logger.info("Telemetry received", device_id=device_id, latency_ms=45)
```

**`<mainApp>` (TypeScript):**
```typescript
// src/lib/logger.ts
interface LogContext {
  traceId?: string;
  deviceId?: string;
  jobId?: string;
  latencyMs?: number;
  [key: string]: string | number | boolean | undefined;
}

type LogLevel = 'DEBUG' | 'INFO' | 'WARNING' | 'ERROR';

function log(level: LogLevel, message: string, context: LogContext = {}): void {
  const entry = {
    timestamp: new Date().toISOString(),
    level,
    service: 'main-app',
    message,
    ...context,
  };
  console.log(JSON.stringify(entry));
}

export const logger = {
  info: (message: string, context?: LogContext) => log('INFO', message, context),
  error: (message: string, context?: LogContext) => log('ERROR', message, context),
  warn: (message: string, context?: LogContext) => log('WARNING', message, context),
  debug: (message: string, context?: LogContext) => log('DEBUG', message, context),
};
```

**Checkpoint:** Logger code compiles/lints. No PII in context fields. High-frequency events (per-message at several Hz per device) use DEBUG level, not INFO.

## Step 3: Add distributed tracing with OpenTelemetry

**`<device>` (Python):**
```python
from opentelemetry import trace

tracer = trace.get_tracer("device-service")

async def process_device_message(device_id: str, message: bytes) -> None:
    with tracer.start_as_current_span("receive_message") as span:
        span.set_attribute("device.id", device_id)
        span.set_attribute("protocol.version", "2")
        telemetry = parse_message(message)

        with tracer.start_as_current_span("send_websocket"):
            await send_websocket(device_id, telemetry)
```

**Trace flow across services:**
```
Trace ID: {trace_id}

Span 1: device-service receives message (5ms)
  +-- Span 2: device-service sends WebSocket (2ms)
      +-- Span 3: main-app receives telemetry (10ms)
          +-- Span 4: main-app persists via the ORM -> PostgreSQL (15ms)
          +-- Span 5: main-app broadcasts to browser via SSE (3ms)

Total: 35ms
```

**Checkpoint:** Span names are meaningful (not generic). Span attributes use domain-specific keys (`device.id`, not `id`). No unbounded attributes (no request bodies or user input as span attributes).

## Step 4: Propagate trace context across service boundaries

When `<device>` sends a WebSocket message to `<mainApp>`, inject the trace context into the message headers or payload so `<mainApp>` can continue the same trace.

**Checkpoint:** Trace context injection code added at every service boundary (WebSocket, HTTP). Verified that `<mainApp>` extracts the context on the receiving side.

## Step 5: Correlate logs, traces, and metrics for debugging

When investigating a latency issue:
1. Start from the metric alert (e.g., processing latency p95 > 500ms) -- this comes from the `monitoring` skill's Prometheus alerts.
2. Find traces with duration > 500ms in the tracing backend.
3. Identify the slow span (e.g., database save took 400ms).
4. Search logs by the same `trace_id` to find error or context messages.
5. Root cause: e.g., database connection pool exhausted.

**Checkpoint:** Correlation path documented. User can follow trace_id from alert to logs.

# Self-check

- [ ] Log entries are JSON-structured with at minimum: `timestamp`, `level`, `service`, `message`.
- [ ] No PII, passwords, tokens, or secrets appear in log messages.
- [ ] No hardcoded `device_id` values -- all use runtime variables.
- [ ] `trace_id` is included in log entries when tracing is active.
- [ ] OpenTelemetry spans have meaningful names and domain-specific attributes.
- [ ] Trace context is propagated across service boundaries (WebSocket, HTTP).
- [ ] High-frequency events (per-message at several Hz per device) are logged at DEBUG level, not INFO.
- [ ] Logger type hints are specific (Literal type or enum, not bare `str` for level).
- [ ] Output stays within length budget (60 lines logger, 40 lines tracing, 20 lines correlation).

# Common mistakes

- **Logging PII or secrets.** Never log passwords, tokens, API keys, or personal data. Review log context fields before adding them.
- **Hardcoded device_id in log statements.** Always use a runtime variable. Copying `device_id="d1"` masks issues with other devices.
- **High-cardinality trace attributes.** Do not use request body content, user input, or unbounded IDs as span attributes -- these cause storage explosion in the tracing backend.
- **Missing trace context propagation.** If `<device>` sends telemetry to `<mainApp>` without injecting trace context, the trace breaks at the service boundary and correlation is lost.
- **Logging at INFO for every message.** At 5Hz per device with 10 devices, that is 50 log lines per second. Use DEBUG for per-message logging; use INFO for aggregated summaries or state changes.
- **Confusing observability with monitoring.** This skill handles logging and tracing instrumentation. For Prometheus metrics, dashboards, and alerts, use `monitoring`.

# Escalation

- **Unsure which log aggregation backend is deployed**: check the project's infrastructure repo for Filebeat, Loki, or Cloud Logging configuration. If unclear, ask the user.
- **OpenTelemetry collector not deployed**: tracing instrumentation requires a collector in the cluster. If the project's deploy/charts repo does not have an OTel collector chart, flag it as a prerequisite and stop.
- **Trace context propagation across WebSocket**: if the WebSocket protocol between `<device>` and `<mainApp>` does not support header injection, escalate for protocol design discussion.
- **OTel SDK version mismatch**: check the installed OTel SDK version in `requirements.txt` or `package.json` before writing instrumentation. If the version differs from the examples, adapt the API usage.

# Examples

<example title="Adding structured logging to a new API route in the main app">
```typescript
// src/app/api/orders/[id]/submit/route.ts
import { logger } from '@/lib/logger';
import { auth } from '@/lib/auth';
import { getDb } from '@/lib/db';
import { orders } from '@/lib/db/schema';
import { eq } from 'drizzle-orm';

export const dynamic = 'force-dynamic';

export async function POST(
  req: Request,
  { params }: { params: Promise<{ id: string }> }
): Promise<Response> {
  const session = await auth();
  if (!session) return Response.json({ error: 'Unauthorized' }, { status: 401 });

  const { id } = await params;
  logger.info('Order submission requested', { orderId: id, userId: session.user?.id });

  const [order] = await getDb()
    .select()
    .from(orders)
    .where(eq(orders.id, Number(id)));

  if (!order) {
    logger.warn('Order not found', { orderId: id });
    return Response.json({ error: 'Not found' }, { status: 404 });
  }

  // ... submit order logic

  logger.info('Order submitted', { orderId: id });
  return Response.json({ status: 'submitted' });
}
```
</example>

<example title="Correlating a slow processing trace">
1. Prometheus alert fires: processing latency p95 > 500ms.
2. Query tracing backend: find traces where `device.id={deviceId}` and duration > 500ms.
3. Examine spans: `receive_message` (5ms) -> `send_websocket` (2ms) -> `persist_telemetry` (480ms).
4. The `persist_telemetry` span is the bottleneck.
5. Search logs: `trace_id={traceId}` reveals "connection pool exhausted, waiting for available connection".
6. Root cause: PostgreSQL connection pool size too small for the write rate.
</example>

# Failure modes

| Failure | Symptom | Recovery |
|---------|---------|----------|
| Logs not JSON-structured | Log aggregation backend cannot parse fields | Update logger to emit JSON; check for `console.log(string)` calls |
| Trace breaks at service boundary | Spans appear as separate traces | Add trace context injection to WebSocket message headers/payload |
| PII in log output | Compliance violation | Audit log context fields; remove PII; add a lint rule if possible |
| Log volume too high | Storage costs spike, log search is slow | Reduce INFO-level logging; move per-message logs to DEBUG |
| OTel collector not deployed | Spans are generated but never exported | Deploy an OTel collector via the project's deploy/charts repo; or use stdout exporter for development |

# Related skills

- `monitoring` -- Prometheus metrics, Grafana dashboards, and alert rules. Use monitoring for dashboards; use observability for logging and tracing.
- `code-standards` -- coding standards including type safety for logger parameters.
- `api-integration` -- API route handler patterns where logging is typically added.
- The project's deployment workflow / infra-pack skills when enabled -- deploying OTel collectors and log shipping sidecars.
