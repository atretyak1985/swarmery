#!/usr/bin/env bash
# rescrub-count.sh <db> — per-class counts of the secret-shaped values the
# daemon's background rescrub would mask in <db>. Values are never printed.
#
# Read-only, but point it at a COPY of a store, never the live one: the
# command refuses the default ~/.swarmery/swarmery.db.
#
# Uses $SWARMERY_BIN when set, else `go run` from this checkout.
set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "usage: $0 <path-to-a-copy-of-swarmery.db>" >&2
  exit 2
fi
db="$1"
if [ ! -f "$db" ]; then
  echo "rescrub-count: no such file: $db" >&2
  exit 2
fi

TOOLS="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if [ -n "${SWARMERY_BIN:-}" ]; then
  exec "$SWARMERY_BIN" rescrub --count --db "$db"
fi
exec go -C "$TOOLS" run ./cmd/swarmery rescrub --count --db "$db"
