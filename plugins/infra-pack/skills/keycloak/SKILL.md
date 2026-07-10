---
name: keycloak
description: "Configure Keycloak realm/client settings, integrate Auth.js v5 OIDC in the web portal (project.json → mainApp), set Keycloak Helm values (init/full stage), recover bootstrap admin, or debug KC_HOSTNAME/issuer mismatches. Do not use for Helm chart templating (use helm-chart-expert), GCP firewall rules (use kubernetes-deployment), or database migrations (use migration-check)."
version: "1.0.0"
owner: "swarmery-infra"
allowed-tools: Read, Bash, Write, Edit, Grep, Glob, WebFetch, WebSearch
---

# Purpose

Provide Keycloak identity and access management patterns for the platform. Cover realm setup, client configuration for Auth.js v5 in the web portal repo (project.json → mainApp), two-stage Helm deployment (init/full), bootstrap admin recovery, issuer mismatch fixes, and NetworkPolicy requirements. For Helm chart template authoring beyond Keycloak values, defer to `helm-chart-expert`. For GCP firewall or ingress debugging, defer to `kubernetes-deployment`.

# When to use

- Configuring the `<keycloak-realm>` realm, clients, roles, or groups in Keycloak
- Integrating Auth.js v5 with Keycloak OIDC in the web portal
- Setting up Keycloak Helm values (init stage or full stage with ingress)
- Debugging Auth.js `error=Configuration` or issuer mismatch (`iss` claim)
- Recovering from bootstrap admin failures (existing database, OOM-kill)
- Configuring NetworkPolicy for ingress-to-Keycloak traffic

# When NOT to use

- Authoring Helm chart templates or `_helpers.tpl` -- use `helm-chart-expert`
- Debugging GCP firewall rules or minikube tunnel -- use `kubernetes-deployment`
- Checking migration safety -- use `migration-check`
- Detecting IaC config drift -- use `infrastructure-as-code`

# Required environment

- Runtime: `.claude/skills/keycloak/SKILL.md`
- Tools: `kubectl`, `helm`, `bash`
- Keycloak version: 26.x (codecentric/keycloakx Helm chart)
- Setup script: the infrastructure repo's `files/keycloak/setup-keycloak.sh` (or the project's equivalent)
- Namespace: environment-specific (`<infra-namespace>`)

# Inputs

- `operation: enum` -- one of: `realm-setup`, `auth-integration`, `debug-auth`, `bootstrap-recovery`, `helm-values`
- `environment: string` -- target environment (localdev, `<envAlias>`, prod)
- `symptom: string` (optional) -- error message or behavior being debugged

# Outputs

- Format: configuration YAML, Auth.js TypeScript code, or a diagnostic command sequence
- Length budget: max 150 lines per response; for multi-operation tasks, produce one section per operation
- For `debug-auth`: ordered diagnosis steps with specific kubectl/curl commands
- For `bootstrap-recovery`: step-by-step recovery procedure with safety warnings

# Procedure

1. **Identify operation and environment** -- Confirm which Keycloak operation is needed and which environment.
   **Checkpoint:** Operation type determined.

2. **Check prerequisites** -- Verify Keycloak pod is running (`kubectl get pods -n $NAMESPACE | grep keycloak`). For auth debugging, verify both internal DNS and ingress are reachable.
   **Checkpoint:** Keycloak pod status confirmed.

3. **Apply configuration** -- Use the appropriate pattern (Helm values, setup script, or Auth.js config).
   **Checkpoint:** Config applied or code change suggested.

4. **Verify** -- For auth integration, confirm the OIDC discovery endpoint is reachable and the issuer URL matches `AUTH_KEYCLOAK_ISSUER`. Run: `curl <issuer>/.well-known/openid-configuration` and verify the JSON `issuer` field.
   **Checkpoint:** Discovery endpoint returns valid JSON with matching issuer.

# Self-check

- [ ] No plaintext passwords -- used `$KC_ADMIN_PASSWORD` or equivalent placeholder
- [ ] Issuer URL matches the `AUTH_KEYCLOAK_ISSUER` env var format
- [ ] Used `kubernetes.io/metadata.name` for NetworkPolicy namespace selectors, not custom `name:` labels
- [ ] Distinguished between `KC_HOSTNAME` (routing) and `KC_HOSTNAME_URL` (token issuer) when both are needed
- [ ] Verified whether the database is fresh before suggesting bootstrap admin vars will work
- [ ] Did not suggest running `kc.sh bootstrap-admin user` inside a pod (causes OOM-kill)

# Common mistakes

- DO NOT hardcode admin passwords in values examples -- use `$KC_ADMIN_PASSWORD` placeholder with a `# LOCAL DEV ONLY` comment
- DO NOT use `name: ingress-nginx` as a namespace selector label -- minikube addon does not add it; use `kubernetes.io/metadata.name: ingress-nginx`
- DO NOT run `/opt/keycloak/bin/kc.sh bootstrap-admin user` inside a running pod -- starts a second JVM, causes OOM-kill at 1Gi memory limit
- DO NOT assume `KC_BOOTSTRAP_ADMIN_*` vars work on an existing database -- they only take effect on first startup with an empty auth database
- DO NOT rely on client-side auth checks alone -- always re-verify in the route handler or server action
- DO NOT use `NEXTAUTH_URL` -- this is Auth.js v4 naming. In Auth.js v5, the env var is `AUTH_URL`

# Escalation

- STOP when: the Keycloak pod is in CrashLoopBackOff and logs show a database connection error (may need PostgreSQL debugging)
- STOP when: the user wants to modify production Keycloak security settings
- STOP when: the issuer mismatch involves a certificate trust chain issue

# Examples

## Example: Two-stage Helm deployment

**Stage 1 -- Init (bootstrap without ingress):**
```yaml
# values.init.localdev.yaml
keycloak:
  enabled: true
  command: ["/opt/keycloak/bin/kc.sh"]
  args: ["start-dev"]
  database:
    vendor: postgres
    hostname: <infra-release>-postgresql.$NAMESPACE.svc.cluster.local
    port: 5432
    database: user_access
    existingSecret: <infra-release>-postgresql
    existingSecretKey: postgres-password
  extraEnv: |
    - name: KC_BOOTSTRAP_ADMIN_USERNAME
      value: admin
    - name: KC_BOOTSTRAP_ADMIN_PASSWORD
      value: "$KC_ADMIN_PASSWORD"   # LOCAL DEV ONLY -- never use in staging/prod
```

**Stage 2 -- Full (add ingress + hostname):**
```yaml
# values.localdev.yaml
keycloak:
  enabled: true
  extraEnv: |
    - name: KC_HOSTNAME
      value: "keycloak.<localdev-host>"
    - name: KC_HOSTNAME_STRICT
      value: "false"
  ingress:
    enabled: true
    ingressClassName: nginx
    rules:
      - host: "keycloak.<localdev-host>"
        paths:
          - path: /
            pathType: Prefix
```

## Example: Auth.js v5 integration in the web portal

```typescript
// <mainApp>/src/auth.ts
import NextAuth from "next-auth";
import Keycloak from "next-auth/providers/keycloak";

export const { handlers, auth, signIn, signOut } = NextAuth({
  providers: [
    Keycloak({
      clientId: process.env.KEYCLOAK_CLIENT_ID!,
      clientSecret: process.env.KEYCLOAK_CLIENT_SECRET!,
      issuer: process.env.KEYCLOAK_ISSUER!,
    }),
  ],
  callbacks: {
    async jwt({ token, account, profile }) {
      if (account) {
        token.accessToken = account.access_token;
        token.roles = (profile as { realm_access?: { roles?: string[] } })
          ?.realm_access?.roles ?? [];
      }
      return token;
    },
    async session({ session, token }) {
      session.accessToken = token.accessToken as string | undefined;
      session.roles = (token.roles as string[] | undefined) ?? [];
      return session;
    },
  },
});
```

Required env vars: `AUTH_SECRET`, `AUTH_URL`, `KEYCLOAK_CLIENT_ID`, `KEYCLOAK_CLIENT_SECRET`, `KEYCLOAK_ISSUER`.

Note: `AUTH_URL` replaces the Auth.js v4 `NEXTAUTH_URL`. Do not use `NEXTAUTH_URL` in Auth.js v5 projects.

## Example: Diagnosing issuer mismatch (error=Configuration)

**Symptom:** Auth.js callback returns `error=Configuration`. Browser shows redirect loop.

**Cause:** the web portal pod exchanges tokens via internal cluster DNS (`<infra-release>-keycloak-http.$NAMESPACE.svc.cluster.local`). Keycloak embeds that internal URL as the `iss` claim. Auth.js rejects it because `iss` does not match `KEYCLOAK_ISSUER` (the public HTTPS URL).

**Fix:** Add `KC_HOSTNAME_URL` to Keycloak env vars:
```yaml
keycloak:
  extraEnv: |
    - name: KC_HOSTNAME
      value: "keycloak.staging.example.com"
    - name: KC_HOSTNAME_URL
      value: "https://keycloak.staging.example.com"
    - name: KC_HOSTNAME_STRICT
      value: "true"
```

- `KC_HOSTNAME` controls request routing and admin console hostname
- `KC_HOSTNAME_URL` controls the full base URL in JWT `iss` claims and OIDC discovery
- Both are needed when internal and external URLs differ

## Example: Bootstrap admin recovery (existing database)

`KC_BOOTSTRAP_ADMIN_*` vars only take effect on first startup with an empty database.

**Recovery procedure:**
```bash
# 1. Scale Keycloak to 0
kubectl scale deployment <infra-release>-keycloak -n "$NAMESPACE" --replicas=0

# 2. Drop and recreate the auth database
kubectl exec -n "$NAMESPACE" <infra-release>-postgresql-0 -- \
  psql -U postgres -c "DROP DATABASE user_access;"
kubectl exec -n "$NAMESPACE" <infra-release>-postgresql-0 -- \
  psql -U postgres -c "CREATE DATABASE user_access;"

# 3. Scale Keycloak back up (bootstrap vars now take effect)
kubectl scale deployment <infra-release>-keycloak -n "$NAMESPACE" --replicas=1

# 4. Re-run setup script to recreate realms, clients, users
./files/keycloak/setup-keycloak.sh local
```

# Failure modes

| Mode | Symptom | Fix |
|------|---------|-----|
| error=Configuration | Redirect loop or error page after login | Decode JWT, check `iss` vs `KEYCLOAK_ISSUER`; add `KC_HOSTNAME_URL` |
| Bootstrap admin fails | Keycloak starts but admin login fails, no error in logs | Check if the auth DB has existing data; drop/recreate DB |
| NetworkPolicy blocks ingress | Keycloak unreachable via ingress but port-forward works | Use `kubernetes.io/metadata.name: ingress-nginx` selector |
| TLS 308 redirect timeout | `ERR_CONNECTION_TIMED_OUT` on HTTPS, HTTP works | Check GCP firewall for `allow-https` rule; see `kubernetes-deployment` |

# Related skills

- `helm-chart-expert` -- defer for Helm template authoring; compose when Keycloak values need template-level changes
- `kubernetes-deployment` -- defer for GCP firewall, minikube tunnel, and ingress debugging
- `infrastructure-as-code` -- defer for drift detection; compose when Keycloak Helm values override was applied manually
- `migration-check` -- no direct overlap; Keycloak manages its own database schema
