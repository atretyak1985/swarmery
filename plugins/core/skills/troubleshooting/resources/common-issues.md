# Common Issues & Quick Fixes

> Placeholders: `<device>` = the device/edge service (`project.json → device`); `<staging-project-id>` /
> `<staging-domain>` = the staging environment's cloud project / public domain (see `project.json → cloud`).

## ImagePullBackOff
**Cause**: Cloud registry token expired
```bash
gcloud auth login
gcloud auth configure-docker <region>-docker.pkg.dev
cd <infrastructure-repo> && ./files/dockerSecret.sh
```

## Pod CrashLoopBackOff
**Check**: `kubectl logs -n <ns> <pod> --previous`
**Common causes**: Missing env vars, wrong image tag, health probe failing

## Stale deployment config (changes not applying)
**Cause**: Subchart .tgz is cached
```bash
# 1. Bump version in subchart Chart.yaml
# 2. Bump dependency version in parent Chart.yaml
# 3. Refresh chart dependencies:
helm dependency update charts/<umbrella-chart>/
```

## WebSocket Connection Refused
**Check**: Is the device-service pod running?
```bash
kubectl get pods -n <ns> -l app=<device>
kubectl logs -n <ns> <pod>
```
**Check**: Is ingress routing /ws/ to port 8081?
```bash
kubectl get ingress -n <ns> -o yaml
```

## Database Migration Failed
**Check**: Migration status
```bash
kubectl exec -n <data-ns> postgresql-0 -- psql -U postgres -d backend -c "SELECT * FROM flyway_schema_history ORDER BY installed_rank DESC LIMIT 5;"
```

## Node.js Build Fails (main app)
**Cause**: `NODE_ENV=development` inherited from shell
```bash
NODE_ENV=production npm run build
```

## Next.js Prerender Crash
**Cause**: Auth pages missing force-dynamic export
```typescript
export const dynamic = 'force-dynamic';
```

## Pi Cluster: kubectl Not Connecting
```bash
alias kpi="KUBECONFIG=$HOME/.kube/pi-config kubectl"
kpi get nodes
```

## Minikube Tunnel Not Working
```bash
sudo minikube tunnel
# Verify /etc/hosts has: 127.0.0.1 d16.local api.d16.local d1.d16.local ...
```

## ERR_CONNECTION_TIMED_OUT on HTTPS (port 80 works, port 443 doesn't)

Three things to check in order:

**1. Cloud firewall rule on wrong network**
The most common cause. `gcloud` silently defaults to `default` VPC. The staging VM is on `minikube-network`.
```bash
# Check which network the rule is on
gcloud compute firewall-rules list --project=<staging-project-id> \
  --format="table(name,network.basename(),allowed[].map().firewall_rule().list())"

# Fix: delete and recreate on correct network
gcloud compute firewall-rules delete allow-https --project=<staging-project-id> --quiet
gcloud compute firewall-rules create allow-https \
  --project=<staging-project-id> --network=minikube-network \
  --direction=INGRESS --priority=1000 --action=ALLOW \
  --rules=tcp:443 --source-ranges=0.0.0.0/0
```

**2. ingress-nginx is NodePort instead of LoadBalancer**
`minikube tunnel` only activates SSH port-forwarding for `LoadBalancer` services.
```bash
kubectl get svc -n ingress-nginx ingress-nginx-controller
# If TYPE=NodePort, patch it:
kubectl patch svc ingress-nginx-controller -n ingress-nginx \
  -p '{"spec":{"type":"LoadBalancer"}}'
# Then restart the tunnel service on the staging VM:
sudo systemctl restart minikube-tunnel
```
Long-term fix: manage ingress-nginx via the chart (not minikube addon) with `controller.service.type: LoadBalancer`.

**3. Stale SSH tunnel processes**
When ingress-nginx service ClusterIP changes, old SSH tunnel processes hold the port.
```bash
# On the staging VM:
sudo ss -tlnp | grep -E ':(80|443)'   # Check who owns the binding
sudo cat /proc/<pid>/cmdline | tr '\0' ' '  # Verify it forwards to correct ClusterIP
sudo systemctl restart minikube-tunnel  # Kill stale processes, spawn fresh ones
```

## Bitnami Redis — PASSWORDS ERROR on Upgrade

**Symptom:** `helm upgrade` fails with:
```
PASSWORDS ERROR: 'global.redis.password' must not be empty
```

**Cause:** Bitnami Redis chart requires `redis.auth.password` to be explicitly set in values when
enabling Redis for the first time or upgrading after it was previously disabled.

**Fix in values file:**
```yaml
redis:
  auth:
    password: "$REDIS_PASSWORD"   # Template var, substituted by mapEnvValuesFromEnv.sh
    existingSecret: ""            # Must be empty to use inline password
```

Secret name: `<release>-redis`, key: `redis-password`.

After updating, re-run `mapEnvValuesFromEnv.sh -en <env>` and then `helm upgrade`.

---

## Auth.js `error=Configuration` After IdP (Keycloak) Login

**Symptom:** User completes the Keycloak login, but the Auth.js callback URL shows `error=Configuration`.
Browser may show a redirect loop or a generic error page.

**Cause:** Auth.js (NextAuth v5) does server-side token exchange via Keycloak's internal cluster DNS
(`keycloak-http.<data-ns>.svc.cluster.local`). The returned JWT `iss` claim contains the
internal URL, which does not match `AUTH_KEYCLOAK_ISSUER` (the public HTTPS URL). Auth.js rejects
the token.

**Fix:** Add `KC_HOSTNAME_URL` to Keycloak's env vars:
```yaml
- name: KC_HOSTNAME_URL
  value: "https://keycloak.<staging-domain>"
```
This forces Keycloak to use the public URL in all JWT `iss` claims regardless of which URL the
token exchange request came from.

See the project's auth-domain skill (if enabled) for full details on `KC_HOSTNAME` vs `KC_HOSTNAME_URL`.

---

## NetworkPolicy Blocking Traffic (silent failure)

If traffic between the ingress controller and the auth service is mysteriously blocked with no errors:

**Cause**: NetworkPolicy uses `name: ingress-nginx` label selector, but that label doesn't exist on the namespace by default.

**Symptom**: The auth service returns 502/504 from ingress, or connection times out, despite pod being healthy.

**Fix in the manifest:**
```yaml
# ❌ Custom label — doesn't exist unless manually applied
namespaceSelector:
  matchLabels:
    name: ingress-nginx

# ✅ Auto-applied by Kubernetes >= 1.21 — no manual label needed
namespaceSelector:
  matchLabels:
    kubernetes.io/metadata.name: ingress-nginx
```

**Temporary fix (without redeploying):**
```bash
kubectl label namespace ingress-nginx name=ingress-nginx
```
