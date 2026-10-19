#!/usr/bin/env bash
# repro-link-stall.sh — reproduce the link-ingest stall against a COPY of the
# daemon database, and say pass/fail.
#
# The stall: the live daemon answered /api/health in 18-60 s and logged
# "error: api: link ingest: begin: context canceled" — the mod's 5 s request
# budget (plugins/swarmery-link/hooks/lib/client.ts REQUEST_TIMEOUT_MS) ran out
# while POST /api/link/events was still queued for the store's single
# connection (internal/store.Open: SetMaxOpenConns(1)).
#
# What it does:
#   1. starts `swarmery serve` on --port against --db, with its own HOME (so its
#      link token, ~/.claude dir and every other home-relative path are scratch
#      ones — the real ~/.swarmery/link-token is never read or written), its own
#      workspace root, SWARMERY_WTJANITOR=0, -plugin-drift-interval 0 and every
#      model-spawning ticker switched off (dispatch, routines, autoverify,
#      handoff, trajjudge, channel probe, quota, breaker probes, landpoll);
#   2. once /api/health answers, drives for --seconds:
#        --concurrency workers POSTing /api/link/events batches (curl
#        --max-time 5, the mod's budget), --readers workers looping over heavy
#        dashboard reads, and a 1 s /api/health poller;
#   3. prints the numbers and stops everything it started.
#
# Ingest load: the transcript tail runs against --projects-root (repeatable).
# Without one it watches an empty scratch dir, i.e. no JSONL ingest at all.
# Pass the real roots (read-only to the daemon) to replay the startup backfill
# the live daemon runs: --projects-root ~/.claude/projects.
#
# Goroutine dumps: export SWARMERY_PPROF=127.0.0.1:<port> before running. The
# dev daemon then serves pprof there (internal/pprofopt), and the script saves
# a goroutine dump (debug=2) the first few times a health probe takes longer
# than 1 s, plus one at the end, into the scratch dir.
#
# Usage:
#   repro-link-stall.sh --db <scratch-copy.db> [--port 7788] [--seconds 120]
#                       [--concurrency 8] [--readers 2] [--batch 20]
#                       [--projects-root <dir>]... [--scratch <dir>] [--bin <swarmery>]
#
# Make the copy ONLY with: sqlite3 ~/.swarmery/swarmery.db ".backup <scratch>/stall.db"
# (never `swarmery backup`: it migrates the source database).
#
# Output (key=value lines): health_samples, health_p50_ms, health_p95_ms,
# health_max_ms, posts, post_timeouts, canceled (count of "link ingest: begin:
# context canceled" in the dev log), waitCount and waitDurationMs (final
# /api/health db pool counters), verdict.
#
# Exit codes: 0 health_p95_ms <= 1000 and canceled == 0; 1 otherwise;
# 2 usage error, a refused --db, or a dev daemon that could not be started.

set -uo pipefail

usage() {
	echo "usage: repro-link-stall.sh --db <scratch-copy.db> [--port 7788] [--seconds 120] [--concurrency 8] [--readers 2] [--batch 20] [--projects-root <dir>]... [--scratch <dir>] [--bin <swarmery>]" >&2
	exit 2
}

die2() {
	echo "repro-link-stall: $*" >&2
	exit 2
}

is_uint() { [[ "$1" =~ ^[0-9]+$ ]]; }

script_dir=$(cd "$(dirname "$0")" && pwd)
db="" port=7788 seconds=120 concurrency=8 readers=2 batch=20 scratch="" bin="$script_dir/../swarmery"
roots=()
while [ $# -gt 0 ]; do
	case "$1" in
	--db) [ $# -ge 2 ] || usage; db=$2; shift 2 ;;
	--port) [ $# -ge 2 ] || usage; port=$2; shift 2 ;;
	--seconds) [ $# -ge 2 ] || usage; seconds=$2; shift 2 ;;
	--concurrency) [ $# -ge 2 ] || usage; concurrency=$2; shift 2 ;;
	--readers) [ $# -ge 2 ] || usage; readers=$2; shift 2 ;;
	--batch) [ $# -ge 2 ] || usage; batch=$2; shift 2 ;;
	--projects-root) [ $# -ge 2 ] || usage; roots+=("$2"); shift 2 ;;
	--scratch) [ $# -ge 2 ] || usage; scratch=$2; shift 2 ;;
	--bin) [ $# -ge 2 ] || usage; bin=$2; shift 2 ;;
	-h | --help) usage ;;
	*) echo "repro-link-stall: unknown argument: $1" >&2; usage ;;
	esac
done

[ -n "$db" ] || { echo "repro-link-stall: --db is required" >&2; usage; }
for n in "$port" "$seconds" "$concurrency" "$readers" "$batch"; do
	is_uint "$n" || { echo "repro-link-stall: not a non-negative integer: $n" >&2; usage; }
done
[ "$port" -ge 1 ] && [ "$port" -le 65535 ] || die2 "--port must be 1..65535"
[ "$batch" -ge 1 ] && [ "$batch" -le 200 ] || die2 "--batch must be 1..200 (the daemon's cap)"

# Never the live database: not by its path, and not by another name for the
# same file (symlink, hard link, ./ spelling).
live_db="$HOME/.swarmery/swarmery.db"
if [ "$db" = "$live_db" ] || [ "$db" -ef "$live_db" ]; then
	die2 "refusing --db $db: that is the live daemon database. Make a copy with: sqlite3 $live_db \".backup <scratch>/stall.db\""
fi
case "$(cd "$(dirname "$db")" 2>/dev/null && pwd)/$(basename "$db")" in
"$HOME/.swarmery/"*) die2 "refusing --db $db: it lives in the live daemon's directory; copy it elsewhere" ;;
esac
[ -f "$db" ] || die2 "--db $db does not exist"
[ -x "$bin" ] || die2 "no swarmery binary at $bin (build it: make -C tools/swarmery build-go, or pass --bin)"
command -v curl >/dev/null || die2 "curl is required"

base="http://127.0.0.1:$port"
if curl -s -o /dev/null --max-time 2 "$base/api/health"; then
	die2 "something already answers on port $port; pick another --port"
fi

[ -n "$scratch" ] || scratch="$(cd "$(dirname "$db")" && pwd)/repro-link-stall-$port"
mkdir -p "$scratch/home" "$scratch/wsroot" "$scratch/projects" "$scratch/cwd" "$scratch/dumps" || die2 "cannot create $scratch"
log="$scratch/dev.log"
health_tsv="$scratch/health.tsv"
posts_tsv="$scratch/posts.tsv"
: >"$health_tsv"
: >"$posts_tsv"
[ ${#roots[@]} -gt 0 ] || roots=("$scratch/projects")

root_flags=()
for r in "${roots[@]}"; do root_flags+=(-projects-root "$r"); done

daemon_pid=""
worker_pids=()
cleanup() {
	local p
	for p in "${worker_pids[@]+"${worker_pids[@]}"}"; do kill "$p" 2>/dev/null; done
	if [ -n "$daemon_pid" ] && kill -0 "$daemon_pid" 2>/dev/null; then
		kill -TERM "$daemon_pid" 2>/dev/null
		for _ in $(seq 1 20); do
			kill -0 "$daemon_pid" 2>/dev/null || break
			sleep 0.5
		done
		kill -KILL "$daemon_pid" 2>/dev/null
	fi
	wait 2>/dev/null
}
trap cleanup EXIT
trap 'exit 130' INT TERM

# The dev daemon. HOME is the scratch home: link.EnsureToken mints the token
# under it, sysscan reads its empty .claude, and nothing touches the operator's.
env -u SWARMERY_PORT -u SWARMERY_PROJECTS_ROOT -u SWARMERY_PROJECTS_ROOTS -u SWARMERY_WORKSPACE_ROOT \
	HOME="$scratch/home" \
	SWARMERY_WTJANITOR=0 \
	SWARMERY_DISPATCH=0 \
	SWARMERY_ROUTINES=0 \
	SWARMERY_AUTOVERIFY=0 \
	SWARMERY_HANDOFF=off \
	SWARMERY_TRAJJUDGE_CAP=0 \
	SWARMERY_CHANNEL_PROBE=0 \
	SWARMERY_QUOTA_INTERVAL=0 \
	SWARMERY_PREFLIGHT_TTL=0 \
	SWARMERY_LANDPOLL_INTERVAL=0 \
	"$bin" serve -db "$db" -port "$port" \
	-workspace-root "$scratch/wsroot" \
	-claude-dir "$scratch/home/.claude" \
	-plugin-drift-interval 0 \
	"${root_flags[@]}" >"$log" 2>&1 &
daemon_pid=$!
echo "$daemon_pid" >"$scratch/dev.pid"
echo "repro-link-stall: dev daemon pid $daemon_pid on $base (db $db, log $log)" >&2

ready=0
for _ in $(seq 1 300); do
	kill -0 "$daemon_pid" 2>/dev/null || break
	if curl -s -o /dev/null --max-time 5 "$base/api/health"; then
		ready=1
		break
	fi
	sleep 1
done
if [ "$ready" -ne 1 ]; then
	tail -20 "$log" >&2
	die2 "dev daemon did not answer /api/health within 300 s"
fi
token_file="$scratch/home/.swarmery/link-token"
token=$(tr -d '[:space:]' <"$token_file" 2>/dev/null)
[ -n "$token" ] || die2 "dev daemon minted no link token at $token_file"

end=$(($(date +%s) + seconds))

# One link worker: a fixed session per worker, batches of turn.start /
# turn.complete with distinct timestamps, at the mod's flush cadence or faster.
link_worker() {
	local w=$1 sid seq=0 body i at code kind
	sid=$(uuidgen | tr '[:upper:]' '[:lower:]')
	while [ "$(date +%s)" -lt "$end" ]; do
		body='{"events":['
		for i in $(seq 1 "$batch"); do
			seq=$((seq + 1))
			at="$(date -u +%Y-%m-%dT%H:%M:%S).$(printf '%03d' $((seq % 1000)))Z"
			[ "$i" -gt 1 ] && body+=','
			if [ $((i % 2)) -eq 1 ]; then kind=turn.start; else kind=turn.complete; fi
			body+="{\"sessionId\":\"$sid\",\"cwd\":\"$scratch/cwd\",\"kind\":\"$kind\",\"at\":\"$at\"}"
		done
		body+=']}'
		code=$(curl -s -o /dev/null -w '%{http_code} %{time_total}' --max-time 5 \
			-H "authorization: Bearer $token" -H 'content-type: application/json' \
			--data-binary "$body" "$base/api/link/events")
		echo "$(date +%s) w$w $code" >>"$posts_tsv"
		sleep 1
	done
}

# One reader: the dashboard's heavy pages, back to back.
reader_worker() {
	local paths=(/api/sessions /api/stats/overview /api/stats/timeseries /api/needs-you /api/projects /api/usage /api/retro/report /api/board/tasks)
	local p
	while [ "$(date +%s)" -lt "$end" ]; do
		for p in "${paths[@]}"; do
			[ "$(date +%s)" -lt "$end" ] || break
			curl -s -o /dev/null --max-time 60 "$base$p"
		done
	done
}

dumps=0
dump_goroutines() {
	[ -n "${SWARMERY_PPROF:-}" ] || return 0
	curl -s --max-time 30 "http://$SWARMERY_PPROF/debug/pprof/goroutine?debug=2" >"$scratch/dumps/goroutine-$1.txt"
}

for w in $(seq 1 "$concurrency"); do
	link_worker "$w" &
	worker_pids+=($!)
done
for r in $(seq 1 "$readers"); do
	reader_worker "$r" &
	worker_pids+=($!)
done

last_health=""
while [ "$(date +%s)" -lt "$end" ]; do
	t0=$(date +%s)
	out=$(curl -s --max-time 60 -w '\n%{http_code} %{time_total}' "$base/api/health")
	meta=${out##*$'\n'}
	json=${out%$'\n'*}
	ms=$(awk -v t="${meta#* }" 'BEGIN{printf "%d", t*1000}')
	echo "$t0 ${meta%% *} $ms" >>"$health_tsv"
	[ "${meta%% *}" = 200 ] && last_health=$json
	if [ "$ms" -gt 1000 ] && [ "$dumps" -lt 3 ]; then
		dumps=$((dumps + 1))
		dump_goroutines "slow-$dumps-$t0" &
	fi
	sleep 1
done
dump_goroutines "end-$(date +%s)"
for p in "${worker_pids[@]+"${worker_pids[@]}"}"; do wait "$p" 2>/dev/null; done
worker_pids=()

final=$(curl -s --max-time 60 "$base/api/health")
[ -n "$final" ] && last_health=$final

pct() { # pct <percentile 0-100> — nearest-rank over column 3 of health.tsv
	awk '{print $3}' "$health_tsv" | sort -n | awk -v p="$1" '{v[NR]=$1} END{if(NR==0){print 0;exit} i=int((p*NR+99)/100); if(i<1)i=1; print v[i]}'
}
samples=$(wc -l <"$health_tsv" | tr -d ' ')
p50=$(pct 50)
p95=$(pct 95)
pmax=$(pct 100)
posts=$(wc -l <"$posts_tsv" | tr -d ' ')
post_timeouts=$(awk '$3=="000"' "$posts_tsv" | wc -l | tr -d ' ')
canceled=$(grep -c 'link ingest: begin: context canceled' "$log")
wait_count=$(printf '%s' "$last_health" | sed -n 's/.*"waitCount":\([0-9]*\).*/\1/p')
wait_ms=$(printf '%s' "$last_health" | sed -n 's/.*"waitDurationMs":\([0-9]*\).*/\1/p')

verdict=PASS
if [ "$p95" -gt 1000 ] || [ "$canceled" -gt 0 ]; then verdict=FAIL; fi

echo "scratch=$scratch"
echo "health_samples=$samples"
echo "health_p50_ms=$p50"
echo "health_p95_ms=$p95"
echo "health_max_ms=$pmax"
echo "posts=$posts"
echo "post_timeouts=$post_timeouts"
echo "canceled=$canceled"
echo "waitCount=${wait_count:-unknown}"
echo "waitDurationMs=${wait_ms:-unknown}"
echo "verdict=$verdict"
[ "$verdict" = PASS ]
