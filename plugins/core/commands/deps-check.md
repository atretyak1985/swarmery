---
description: Check dependency versions across all project repositories
color: red
---

# Dependencies Check

Read-only dependency audit across the project's canonical repos (`project.json → repos`) -- outdated packages, security vulnerabilities (npm audit / pip-audit), cross-repo version mismatches, and upgrade recommendations with breaking-change notes.

Follow the playbook in `skills/deps-check/SKILL.md` (auto-loaded skill `deps-check`); apply it to $ARGUMENTS if provided.

This audits only -- it never runs `npm audit fix`, `npm update`, or `pip install --upgrade`.
