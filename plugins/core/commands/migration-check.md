---
description: Check database migrations and schema consistency
color: red
---

# Migration Check

Database migration safety review (reversibility, data safety, index concurrency, sequential ordering) plus schema alignment between the migrations directory, the ORM schema in the main app (`apps/<mainApp>`), and Zod/DTO types — exact paths per the project's `CLAUDE.md`.

Follow the playbook in `skills/migration-check/SKILL.md` (auto-loaded skill `migration-check`); apply it to $ARGUMENTS if provided.
