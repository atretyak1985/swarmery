-- 0114: route_decisions.applied counts the router's MODEL only (phase runs).
--
-- Until this release a phase run's row said applied = 1 when the router's model
-- OR its effort won the ladder. Since the Plans page started every run with an
-- explicit model, every tier-M row read applied = 1 while the router's model ran
-- zero times (won_rung = request): "router · applied" said 56/56 for 0/56.
-- internal/phaserun now writes applied = 1 only when won_rung = 'route'; this
-- rewrites the old phase-run rows to the same rule so the report does not mix
-- two meanings across the deploy.
--
-- Lossless for the model question: won_rung is the model rung and is kept. The
-- effort-only signal is dropped on purpose — used_effort still records what ran.
--
-- Dispatch rows are untouched: there applied also covers a routed playbook,
-- which is a real application of the router (internal/dispatch/route_signals.go).
--
-- WHY 0114: 0108–0111 stay reserved for the mods plan (see 0113's note).
UPDATE route_decisions
   SET applied = 0
 WHERE surface = 'phaserun'
   AND applied = 1
   AND won_rung <> 'route';
