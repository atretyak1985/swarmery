# Environment Matrix

## Environments

| Property | Local Dev | Staging (`<envAlias>`) | Staging / Production |
|----------|-----------|------------------------|----------------------|
| **Infra** | Minikube | Minikube on a cloud VM | target managed/shared cluster |
| **Namespace** | chart-defined local namespace | chart-defined shared namespace | chart-defined promoted namespace |
| **Primary app repo** | the web portal repo (project.json → mainApp) | same | same |
| **Chart repo** | the chart/deployment repo (project.json → repos) | same | same |
| **Infra repo** | the infrastructure repo (project.json → repos) | same | same |
| **Promotion source** | direct validation | version-pinning repo desired state | version-pinning repo promoted state |
| **Registry** | container registry (e.g. GCP Artifact Registry) | same | same |
| **Values file** | `values.localdev.yaml` | `values.<envAlias>.yaml` (or equivalent chart values) | environment-specific promoted values |

## Service Ports

| Service | HTTP | WebSocket | Metrics |
|---------|------|-----------|---------|
| edge service (project.json → device) | 8080 | 8081 | 8080 (/metrics) |
| web portal (project.json → mainApp) | 3000 | app-specific realtime path | app-specific metrics if enabled |
| Keycloak | ingress-defined | - | platform metrics as configured |

## Environment Naming Note

- CI/CD and operations docs should prefer the staging alias (`project.json → cloud.envAlias`).
- Terraform or older infra references may still call the same environment **`dev`**.
- Treat them as the same target unless a document explicitly states otherwise.

## Image Registry

Registry: `<region>-docker.pkg.dev/<gcp-project>/<registry-repo>/`

| Image | Arch | Tag Convention |
|-------|------|---------------|
| edge service image | arm64 | git short hash |
| web portal image | amd64 | git short hash |
| promoted deployment reference | n/a | immutable digest |
