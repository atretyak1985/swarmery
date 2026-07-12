# Step 11 — T3.5: `swarmery install` (launchd auto-start)

## Header

| Field | Value |
|---|---|
| Phase | 4 — Integration, install, ship |
| Duration | 1 short agent session, ~1–2 h (MEDIUM confidence) |
| Type | Agent session (code) |
| Risk | Low — isolated to cmd/ + new package; reversible via uninstall |
| Dependencies | Step 10 |

## Goal

Make the daemon always-on: `swarmery install` registers a launchd agent so the
dashboard survives reboots; `uninstall` removes it cleanly; `status` reports health.

## Automation

Fresh Claude Code session in `/Volumes/Work/swarmery/tools/swarmery`.

## Agent Prompt

```
Reference: docs/plan/step-11-install-daemon.md

Context:
Репозиторій Swarmery, main після інтеграції (step 10), make build працює.
Прочитай cmd/swarmery. Задача: автозапуск демона на macOS через launchd,
щоб після логіна дашборд завжди був живий на localhost:7777.

Tasks:
1. swarmery install:
   - копіює поточний бінарник у ~/.swarmery/bin/swarmery
   - пише ~/Library/LaunchAgents/com.swarmery.daemon.plist:
     ProgramArguments = [~/.swarmery/bin/swarmery, serve],
     RunAtLoad=true, KeepAlive=true,
     StandardOutPath/StandardErrorPath → ~/.swarmery/logs/
   - launchctl bootstrap gui/$(id -u) <plist> (сучасний API, не load)
   - ІДЕМПОТЕНТНІСТЬ: повторний install = оновити бінарник і
     перезапустити сервіс (bootout → bootstrap), нічого не дублювати
2. swarmery uninstall: bootout + видалити plist (логи і БД лишити)
3. swarmery status: чи запущено, версія, PID, аптайм, розмір БД
4. НЕ чіпай hooks у ~/.claude/settings.json — це Фаза 2.

Boundaries:
- Тільки cmd/ і новий internal/installer. Ніякого UI.
- Тести: генерація plist (golden file) та ідемпотентність
  (повторний install не додає другий сервіс) — logic-рівень, без
  реального launchctl у тестах.

Output / Validation:
go test зелені. Живий тест: make build && ./swarmery install →
launchctl print gui/$(id -u)/com.swarmery.daemon показує running →
curl -s localhost:7777/api/stats/today відповідає → swarmery uninstall
чисто прибирає. Покажи вивід кожного кроку. Conventional commit.
Заповни Completion Report у docs/plan/step-11-install-daemon.md.
```

## Detailed Instructions

- Port: plist must respect `SWARMERY_PORT` if the user configured one — write it into
  `EnvironmentVariables` in the plist when the flag/env was set at install time.
- Watch out for a port clash during the live test: stop any manually-running
  `./swarmery serve` before `install`.
- Reversibility: `uninstall` is the rollback; DB and logs are intentionally preserved.

## Success Criteria

- [ ] `go test` green incl. plist golden-file + idempotency tests
- [ ] `install` → `launchctl print` shows the service running; API answers on :7777
- [ ] Second `install` run leaves exactly one service registered (idempotent)
- [ ] `uninstall` removes plist + service; `~/.swarmery/{swarmery.db,logs}` remain
- [ ] `status` prints running-state, PID, version, DB size
- [ ] `~/.claude/settings.json` untouched

## Navigation

Previous: [step-10-integration.md](step-10-integration.md) · Next: [step-12-quality-gate-ship-dogfood.md](step-12-quality-gate-ship-dogfood.md) · Index: [00-plan.md](00-plan.md)

### Completion Report

```
Date/agent: 2026-07-12 · Claude Code executor (feat/swarmery-install worktree)
Commit SHA: (this commit)
Live-test output summary:
  install #1     → binary at ~/.swarmery/bin/swarmery, plist written,
                   launchctl print: state = running, pid = 32082;
                   curl localhost:7777/api/projects → [] (200)
  status         → "service: running / pid / uptime / db 4.0 KiB / version 0.1.0"
  install #2     → "restarting existing service" (bootout → bootstrap),
                   new pid 33133; exactly 1 service in gui/501, 1 plist — idempotent
  SWARMERY_PORT=7788 install → plist got EnvironmentVariables{SWARMERY_PORT=7788};
                   daemon log shows "serving on :7788"; curl :7788 → []
  uninstall      → service gone (launchctl print: Could not find service),
                   plist deleted; ~/.swarmery/{swarmery.db,logs} preserved
  System left CLEAN (uninstalled) — real installation happens at Gate 12.
Notes:
  • Ran EARLY in parallel with the wave (before step 10 integration), branched
    from the contract-freeze tag — main.go diff kept to +11/-1 (three subcommand
    registrations + usage lines) since this branch merges LAST.
  • New package internal/installer: launchctl behind a Runner interface;
    tests (go vet + go test green) cover plist golden files (default + port),
    install idempotency with a fake launchd, uninstall keeping logs/DB, status.
  • /api/stats/today does not exist on this branch (metrics branch) —
    /api/projects used for the live check instead.
  • ~/.claude/settings.json untouched (hooks are Phase 2).
```
