---
description: Quick security audit - find common vulnerabilities and security issues
color: red
---

# Security Audit

Static application security audit -- secrets, injection, XSS, auth/authz, dependency CVEs, deployment/infra config, and the project's device-specific surfaces (telemetry-protocol validation, WebSocket auth, media/stream access -- see `project.json → domainTerms`) -- mapped to OWASP Top 10 with severity-ranked findings.

Follow the playbook in `skills/security-audit/SKILL.md` (auto-loaded skill `security-audit`); apply it to $ARGUMENTS if provided.

For CI/CD pipeline hardening, image scanning, or SBOMs use `supply-chain-security` instead.
