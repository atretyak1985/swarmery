// Package routines is the scheduled-automation subsystem (fusion phase 7): it
// runs cron/webhook/manual-triggered routines whose typed steps
// (command | ai-prompt | create-task) execute sequentially with a per-step and
// a whole-run timeout, a catch-up policy, and pruned run history.
//
// It mirrors the internal/provision + internal/dispatch idioms exactly:
//   - Every process/side-effect boundary is an interface — Runner for the
//     headless `claude` an ai-prompt step spawns, TaskCreator for the board task
//     a create-task step inserts — so unit tests run against stubs with no real
//     process spawned and no api-package import cycle.
//   - The Service holds *sql.DB and writes through its own small method set
//     (single-writer discipline); a `now func() time.Time` clock seam and a
//     `Go func(func())` async seam make the scheduler deterministic in tests
//     (no sleeping on real cron).
//   - A 60s ticker drives due routines; a global semaphore caps concurrent runs
//     and a per-routine single-flight guard prevents overlap; SWARMERY_ROUTINES=0
//     is the kill-switch; startup HealStale marks interrupted runs 'failed'.
//
// The WS bus is frozen: ai-prompt runs are ordinary headless sessions and show
// up in Sessions/analytics via the normal ingest pipeline; create-task emits the
// existing task_updated event through the injected TaskCreator. No new WS message
// type is introduced — the Routines page polls GET /api/routines/{id}/runs.
package routines
