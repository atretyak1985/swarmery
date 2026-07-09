---
description: Check environment variables across repos - find missing, unused, or undocumented vars
color: red
---

# Environment Variables Check

Static cross-repo env var audit -- missing, unused, undocumented, or inconsistently named variables plus hardcoded-secret flags, with file:line citations.

Follow the playbook in `skills/env-check/SKILL.md` (auto-loaded skill `env-check`); apply it to $ARGUMENTS if provided.

For live runtime env introspection use the platform's exec/describe tooling (e.g. `kubectl exec`, `gcloud run services describe`), not this command.
