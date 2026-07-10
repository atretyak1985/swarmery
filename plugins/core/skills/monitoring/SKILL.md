---
name: monitoring
description: "Use this skill for Prometheus metrics, Grafana dashboards, alert rules, and ServiceMonitor wiring ONLY -- including instrumenting an endpoint with Prometheus counters or histograms. NOT for logs or traces: structured logging, OpenTelemetry spans, and log-trace correlation belong to observability. NOT for Helm liveness/readiness probes (the project's deployment workflow / infra-pack skills when enabled)."
version: "1.0.0"
owner: "swarmery-core"
---

# Purpose

Define, instrument, and verify Prometheus metrics, Grafana dashboards, and alert rules across the project's platform. Produce metric definitions, dashboard JSON, alert rule YAML, and ServiceMonitor resources that connect instrumentation to Prometheus scraping.

**Placeholders used below:** `<mainApp>` = `project.json → mainApp` (the primary application), `<device>` = `project.json → device` (the edge/device service, if the project has one), `<project>` = `project.json → name` snake_cased (the metric-name prefix). Repos and their layout come from `project.json → repos`.

# When to use

- Adding Prometheus counters, histograms, or gauges to `<mainApp>` or `<device>` code.
- Creating or modifying Grafana dashboard JSON for one of the project's services.
- Writing or reviewing Prometheus alert rules (PrometheusRule CRDs or rule files).
- Wiring a new service for Prometheus scraping via ServiceMonitor in the project's deploy/charts repo.
- Investigating why a Prometheus alert fired or a metric spiked.
- Reviewing alert coverage for a newly deployed service.

**Disambiguation -- monitoring vs observability:** If the task says "instrument" or "add metrics," use this skill. If it says "add logging," "add tracing," or "correlate logs with traces," use `observability`. If investigation starts from a Prometheus alert, start here; if it starts from log search or trace lookup, start with `observability`.

# When NOT to use

- **Structured logging or distributed tracing** -- use `observability`.
- **OpenTelemetry spans or trace context propagation** -- use `observability`.
- **Helm liveness/readiness probes** -- part of the project's deployment workflow (infra-pack skills when enabled).
- **CI pipeline observability** (build times, deploy frequency) -- out of scope.
- **Log aggregation queries** (Loki, Cloud Logging) -- use `observability`.

# Required environment (.claude/skills/monitoring/SKILL.md)

- Access to the application repo(s) listed in `project.json → repos` (e.g., `<mainApp>` using `prom-client` for TypeScript, `<device>` using `prometheus_client` for Python).
- Access to the project's deploy/charts repo (Helm charts, ServiceMonitor/PrometheusRule CRDs), if one exists.
- Access to the project's infrastructure repo (Prometheus scrape configs, Grafana provisioning), if one exists.

# Inputs

| Input | Required | Description |
|-------|----------|-------------|
| Target service | Yes | Which service to instrument (`<mainApp>`, `<device>`, or new) |
| Metric intent | Yes | What to measure (latency, throughput, error rate, saturation, domain-specific) |
| Alert thresholds | No | Operator-defined SLOs or severity levels (if creating alerts) |
| Existing dashboards | No | Path to current Grafana JSON to extend |

# Outputs

**Length budget:** Combined code output must not exceed 150 lines per instrumented service. Dashboard JSON panels are excluded from this limit.

Deliverables:
- Metric definitions in application code (TypeScript or Python).
- Prometheus alert rule YAML (PrometheusRule CRD or standalone rule file).
- Grafana dashboard JSON (or panel additions to existing dashboard).
- ServiceMonitor/PodMonitor YAML for the deploy/charts repo.
- Verification evidence: `helm lint` pass for chart changes, metric endpoint curl output.

# Procedure

## Step 1: Identify metric type and naming

Choose Counter, Histogram, or Gauge. Follow the naming convention `<project>_{subsystem}_{unit}_{suffix}` (e.g., `myproject_telemetry_messages_total`, `myproject_telemetry_latency_seconds`).

**Checkpoint:** Metric name confirmed to follow `<project>_{subsystem}_{unit}_{suffix}` convention.

## Step 2: Instrument the application code

**`<device>` (Python):**
```python
from prometheus_client import Counter, Histogram, Gauge

telemetry_messages = Counter(
    'myproject_telemetry_messages_total',
    'Total telemetry messages received',
    ['device_id', 'type']
)
telemetry_latency = Histogram(
    'myproject_telemetry_latency_seconds',
    'Telemetry processing latency',
    ['device_id']
)
devices_connected = Gauge(
    'myproject_devices_connected',
    'Number of connected devices',
    ['status']
)

# Usage -- device_id comes from runtime, never hardcode
telemetry_messages.labels(device_id=device_id, type='POSITION_UPDATE').inc()
telemetry_latency.labels(device_id=device_id).observe(latency_s)
devices_connected.labels(status='active').set(active_count)
```

**`<mainApp>` (TypeScript):**
```typescript
// src/lib/metrics.ts
import { Counter, Histogram, Registry } from 'prom-client';

export const registry = new Registry();

export const telemetryMessages = new Counter({
  name: 'myproject_telemetry_messages_total',
  help: 'Total telemetry messages received',
  labelNames: ['device_id', 'type'] as const,
  registers: [registry],
});

// Usage -- device_id is a runtime variable
telemetryMessages.labels({ device_id: deviceId, type: 'POSITION_UPDATE' }).inc();
```

Expose via an API route (e.g. `src/app/api/metrics/route.ts`) guarded by a scrape-only auth check. Check `package.json` for the actual `prom-client` version -- the v14 and v15 APIs differ in registry handling.

**Checkpoint:** Code compiles/lints. Metric labels use only runtime variables, no hardcoded values.

## Step 3: Wire Prometheus scraping via ServiceMonitor (deploy/charts repo)

```yaml
# <deploy-repo>/charts/<mainApp>/templates/servicemonitor.yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: {{ include "<mainApp>.fullname" . }}
  labels:
    {{- include "<mainApp>.labels" . | nindent 6 }}
spec:
  selector:
    matchLabels:
      {{- include "<mainApp>.selectorLabels" . | nindent 8 }}
  endpoints:
    - port: http
      path: /api/metrics
      interval: 15s
```

Run `helm lint` after adding or modifying any chart template.

**Checkpoint:** `helm lint` passes. ServiceMonitor selector labels match the Kubernetes Service labels.

## Step 4: Define alert rules

```yaml
groups:
  - name: project-alerts
    rules:
      - alert: HighErrorRate
        expr: >
          rate(http_server_requests_seconds_count{status=~"5.."}[5m])
          / rate(http_server_requests_seconds_count[5m]) > 0.05
        for: 5m
        labels:
          severity: critical
        annotations:
          summary: "Error rate above 5% for {{ $labels.service }}"

      - alert: DeviceDisconnected
        expr: myproject_devices_connected{status="active"} < 1
        for: 2m
        labels:
          severity: critical
        annotations:
          summary: "No active devices connected"
```

Every alert rule MUST include a `for:` duration to avoid firing on transient spikes.

**Checkpoint:** Every alert has a `for:` duration. Alert thresholds confirmed with user (or flagged for confirmation).

## Step 5: Build Grafana dashboard panels

Golden signals PromQL reference:

| Signal | PromQL |
|--------|--------|
| Latency (p95) | `histogram_quantile(0.95, rate(http_server_requests_seconds_bucket[5m]))` |
| Traffic | `rate(http_server_requests_seconds_count[5m])` |
| Errors | `rate(http_server_requests_seconds_count{status=~"5.."}[5m]) / rate(http_server_requests_seconds_count[5m])` |
| Saturation (CPU) | `100 - (avg by (instance) (rate(node_cpu_seconds_total{mode="idle"}[5m])) * 100)` |

Domain-specific panel examples:
- Message rate per device: `rate(myproject_telemetry_messages_total[5m])`
- Latency percentiles: `histogram_quantile(0.95, rate(myproject_telemetry_latency_seconds_bucket[5m]))`
- Connected device count: `myproject_devices_connected{status="active"}`

**Checkpoint:** Dashboard JSON is syntactically valid. Panel PromQL references only metrics that exist.

## Step 6: Verify the instrumentation

- Curl the metrics endpoint and confirm new metrics appear.
- Run `helm lint` on any modified chart.
- Verify alert rule syntax: `promtool check rules <file>` (if available).

**Checkpoint:** Verification evidence collected. Ready to return results.

# Self-check

- [ ] Metric names follow `<project>_{subsystem}_{unit}_{suffix}` convention.
- [ ] No hardcoded `device_id` values in metric labels -- all use runtime variables.
- [ ] Every alert rule includes a `for:` duration.
- [ ] Label cardinality is bounded -- no unbounded label values (e.g., request path, user ID).
- [ ] ServiceMonitor or scrape config connects the metric endpoint to Prometheus.
- [ ] `helm lint` passes for any modified chart.
- [ ] Dashboard JSON or panel definitions are syntactically valid.
- [ ] Combined code output does not exceed 150 lines per service.

# Common mistakes

- **Hardcoded device_id in examples or code.** Always use a runtime variable. Copying `device_id="d1"` into production creates metrics for only one device.
- **Alert rules without `for:` duration.** Omitting `for:` causes alerts to fire on single-sample spikes.
- **Unbounded label cardinality.** Using request path, user ID, or trace ID as a metric label explodes Prometheus storage.
- **Eager metric registration at import time in `<mainApp>`.** Register metrics in a function, not at module scope, if the registry depends on runtime config.
- **Forgetting ServiceMonitor wiring.** Defining metrics in code without a ServiceMonitor means Prometheus never scrapes them.
- **Mixing monitoring and observability concerns.** This skill handles metrics and dashboards. For structured logging and tracing, use `observability`.

# Escalation

- **Prometheus not scraping the endpoint**: verify ServiceMonitor selector labels match the Service; if they do not match and the chart structure is unclear, escalate to the user.
- **Unsure about SLO thresholds**: surface the proposed values and ask the user to confirm before writing alert rules.
- **prom-client version mismatch**: if `package.json` shows a version whose API differs from the examples here, flag it and adapt.
- **Cross-repo wiring unclear**: if the scrape config lives in the project's infrastructure repo and the structure is unfamiliar, escalate rather than guessing.

# Examples

<example title="Instrument telemetry latency in the device service">
Input: "Add a histogram for telemetry processing latency in the device service."

```python
from prometheus_client import Histogram

telemetry_latency = Histogram(
    'myproject_telemetry_latency_seconds',
    'Time to process a single telemetry message',
    ['device_id'],
    buckets=[0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0]
)

async def process_telemetry(device_id: str, message: bytes) -> None:
    with telemetry_latency.labels(device_id=device_id).time():
        parsed = parse_message(message)
        await store_telemetry(parsed)
```
</example>

<example title="Alert when no devices are connected">
```yaml
- alert: NoDevicesConnected
  expr: myproject_devices_connected{status="active"} == 0
  for: 3m
  labels:
    severity: critical
  annotations:
    summary: "No active devices connected for 3 minutes"
    description: "Check the device-service pods and upstream connectivity."
```
</example>

# Failure modes

| Failure | Symptom | Recovery |
|---------|---------|----------|
| Metric not appearing in Prometheus | `/api/metrics` returns the metric but Prometheus targets page shows "down" | Check ServiceMonitor selector labels match the Kubernetes Service labels |
| Cardinality explosion | Prometheus OOM or slow queries | Audit labels; remove unbounded values; use `le` buckets for histograms |
| Alert firing on transient spike | Alert resolves within seconds | Add or increase `for:` duration |
| prom-client v14/v15 API mismatch | TypeScript compilation errors on registry methods | Check `package.json` version; adjust import pattern accordingly |
| Dashboard panel shows "No data" | Panel renders but is empty | Verify PromQL label matchers against actual metric labels; check time range |

# Related skills

- `observability` -- structured logging, distributed tracing, log correlation. Use observability for instrumentation that produces logs or traces; use monitoring for metrics, dashboards, and alerts.
- `monorepo-coordination` -- when monitoring changes span the app repo + deploy repo + infrastructure repo.
- The project's deployment workflow / infra-pack skills when enabled -- Helm chart patterns (health probes, resource templates) and verifying monitoring coverage before promoting to production.
