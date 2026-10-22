#!/usr/bin/env bash
# cachecold-report.sh — what cold prompt caches cost, from a swarmery DB.
#
# Usage: cachecold-report.sh <db-path> [days] [ttl-minutes]
#        (defaults: the last 30 days; a 60 min TTL, the subscription cache)
#
# Reads turns.cold_restart (migration 0127; set live by ingest and for history
# by `swarmery cachecold-backfill`) and prints:
#
#   1. per day: how many main-thread turns were cold restarts and their summed
#      cost_usd;
#   2. the validation the marks are judged by: every main-thread assistant turn
#      that came after a pause longer than the TTL AND whose cost_usd is in the
#      top decile of the window's main-thread assistant turns ("expensive after
#      a pause"), set against the marks — both / marked only / expensive only —
#      followed by the expensive-after-pause turns themselves.
#
# Read-only: opens the DB with `sqlite3 -readonly`. A WAL-mode copy made with
# `.backup` or `cp` has no -shm file and cannot be opened read-only that way; the
# script then falls back to `immutable=1`, which is ONLY safe on a copy nobody
# writes — point it at a copy, never at the daemon's live store.
# Bash 3.2-compatible (macOS). Exit codes: 0 ok, 2 usage / unreadable DB.

set -euo pipefail

usage() {
  echo "usage: cachecold-report.sh <db-path> [days] [ttl-minutes]" >&2
}

if [ $# -lt 1 ] || [ $# -gt 3 ]; then
  usage
  exit 2
fi
db="$1"
days="${2:-30}"
ttl="${3:-60}"
case "$days$ttl" in
  *[!0-9]*)
    usage
    exit 2
    ;;
esac
if [ ! -r "$db" ]; then
  echo "cachecold-report: cannot read $db" >&2
  exit 2
fi
if ! command -v sqlite3 >/dev/null 2>&1; then
  echo "cachecold-report: sqlite3 not found" >&2
  exit 2
fi

# Pick an open mode that works on this file.
if sqlite3 -readonly "$db" "SELECT 1;" >/dev/null 2>&1; then
  q() { sqlite3 -readonly -header -column "$db" "$1"; }
elif sqlite3 "file:${db}?immutable=1" "SELECT 1;" >/dev/null 2>&1; then
  echo "note: $db opened with immutable=1 (a WAL copy without -shm) — use a copy, not a live store" >&2
  q() { sqlite3 -header -column "file:${db}?immutable=1" "$1"; }
else
  echo "cachecold-report: sqlite3 cannot open $db" >&2
  exit 2
fi

# An instant, not a date: the same bound `swarmery cachecold-backfill --since Nd`
# measures from, so no turn is in the report's window but outside the marks'.
# Spelled like the stored timestamps (T separator) so the string compare holds.
since="strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-${days} days')"

# The window's main-thread assistant turns with usage, each with the pause since
# the previous one in its session and the cost decile it falls in.
window_cte="
WITH m AS (
  SELECT t.id, t.session_id, t.started_at, t.cost_usd, t.cold_restart,
         LAG(COALESCE(t.ended_at, t.started_at)) OVER (PARTITION BY t.session_id ORDER BY t.seq) AS prev_end
  FROM turns t
  WHERE t.role = 'assistant' AND t.agent_name IS NULL
    AND (COALESCE(t.tokens_in, 0) + COALESCE(t.tokens_cache_read, 0) + COALESCE(t.tokens_cache_write, 0)) > 0
),
w AS (
  SELECT m.*,
         (julianday(m.started_at) - julianday(m.prev_end)) * 1440.0 AS pause_min,
         CASE WHEN m.cost_usd IS NULL THEN NULL
              ELSE NTILE(10) OVER (PARTITION BY m.cost_usd IS NULL ORDER BY m.cost_usd DESC) END AS decile
  FROM m
  WHERE m.started_at >= ${since}
),
x AS (
  SELECT w.*, (w.prev_end IS NOT NULL AND w.pause_min > ${ttl} AND w.decile = 1) AS expensive
  FROM w
)"

echo "== cold restarts per day (last ${days} days, main thread) =="
q "
SELECT substr(started_at, 1, 10) AS day,
       COUNT(*) AS cold_restarts,
       printf('%.2f', COALESCE(SUM(cost_usd), 0)) AS cost_usd
FROM turns
WHERE cold_restart = 1 AND started_at >= ${since}
GROUP BY day
UNION ALL
SELECT 'total', COUNT(*), printf('%.2f', COALESCE(SUM(cost_usd), 0))
FROM turns
WHERE cold_restart = 1 AND started_at >= ${since};"

echo
echo "== marks vs expensive-after-pause turns (pause > ${ttl} min, cost in the top decile) =="
q "${window_cte}
SELECT COUNT(*) AS turns,
       SUM(cold_restart = 1) AS marked,
       SUM(expensive = 1) AS expensive_after_pause,
       SUM(cold_restart = 1 AND expensive = 1) AS both,
       SUM(cold_restart = 1 AND expensive = 0) AS marked_only,
       SUM(cold_restart = 0 AND expensive = 1) AS expensive_only,
       printf('%.4f', (SELECT MIN(cost_usd) FROM w WHERE decile = 1)) AS top_decile_from_usd
FROM x;"

echo
echo "== expensive-after-pause turns =="
q "${window_cte}
SELECT x.id AS turn_id, x.session_id, substr(x.started_at, 1, 16) AS started,
       CAST(ROUND(x.pause_min) AS INTEGER) AS pause_min,
       printf('%.3f', x.cost_usd) AS cost_usd,
       x.cold_restart AS marked
FROM x
WHERE x.expensive = 1
ORDER BY x.cost_usd DESC;"
