# OWASP Top 10 Checklist

> Map each item onto the project's actual stack — see `.claude/project.json` (`stack`, `device`,
> `cloud.*`) and the project's `CLAUDE.md`. Mark items N/A when the surface does not exist
> (e.g., device checks in a project with no device/edge repo).

## A01: Broken Access Control
- [ ] All API endpoints require authentication (e.g., Auth.js v5 JWT or the project's auth layer)
- [ ] Role-based access enforced in route handlers and server actions (session + role checks)
- [ ] No direct object reference without authorization check
- [ ] CORS properly configured (only allowed origins)
- [ ] WebSocket connections authenticated

## A02: Cryptographic Failures
- [ ] Passwords never stored in plain text
- [ ] JWT secrets stored in the runtime's secret manager, never in committed values/config files
- [ ] TLS/HTTPS for all external communication
- [ ] No sensitive data in URL parameters or logs
- [ ] Cloud provider service account keys not committed to git

## A03: Injection
- [ ] SQL: Parameterized queries only (a typed ORM such as Prisma handles this; flag any raw-SQL escape hatch with user input)
- [ ] NoSQL: check operator injection if the project uses a NoSQL store; N/A for SQL-only stacks
- [ ] Command: No user input in subprocess calls (Python/device code)
- [ ] XSS: No dangerouslySetInnerHTML without sanitization (React/Next.js apps)
- [ ] Database migrations use parameterized scripts

## A04: Insecure Design
- [ ] Rate limiting on API endpoints
- [ ] Input validation at API boundaries (e.g., Zod schemas in route handlers and server actions)
- [ ] Privileged operations require an authenticated, authorized user
- [ ] Device commands validated before forwarding to the device/edge layer (if the project has one)

## A05: Security Misconfiguration
- [ ] Debug mode disabled in production
- [ ] Default credentials changed (auth provider admin, database)
- [ ] Unnecessary services disabled
- [ ] Security headers set (CSP, X-Frame-Options, etc.)
- [ ] Container/runtime SecurityContext: runAsNonRoot, readOnlyRootFilesystem (where supported)

## A06: Vulnerable and Outdated Components
- [ ] `npm audit` clean (Node/TypeScript apps)
- [ ] `pip-audit` clean (Python repos, e.g. the device/edge repo)
- [ ] Base Docker images updated regularly
- [ ] Infrastructure/service-config dependencies up to date

## A07: Identification and Authentication Failures
- [ ] Brute-force detection enabled on the auth provider
- [ ] Short-lived access tokens (e.g., 5-minute JWT lifespan)
- [ ] Refresh token rotation enabled
- [ ] MFA for admin accounts
- [ ] Session timeout configured

## A08: Software and Data Integrity Failures
- [ ] Docker images from a trusted registry (the project's cloud artifact registry)
- [ ] Infrastructure/service config version-pinned
- [ ] CI/CD pipeline secured (secrets not exposed)
- [ ] No eval() or dynamic code execution

## A09: Security Logging and Monitoring Failures
- [ ] Authentication events logged (login, logout, failed attempts)
- [ ] Authorization failures logged
- [ ] API errors logged with correlation IDs
- [ ] Alerts for error rate spikes (e.g., Prometheus or the provider's monitoring)
- [ ] Log aggregation configured (structured JSON logging)

## A10: Server-Side Request Forgery (SSRF)
- [ ] No user-controlled URLs in server-side fetches
- [ ] Internal service URLs not exposed to clients
- [ ] Device/edge WebSocket URLs validated before connection
- [ ] Ingress rules restrict internal-only paths
