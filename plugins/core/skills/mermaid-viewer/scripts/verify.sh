#!/usr/bin/env bash
# verify.sh — local HTTP server harness so Playwright can reach the viewer.
#
# The browser security model blocks file:// URLs when driven by Playwright.
# This script starts a python3 http.server in the background on an ephemeral
# port and prints "URL<tab>PID<tab>PORT" so the caller knows where to navigate
# and how to stop it.
#
# Usage:
#   verify.sh start <dir>         # start server serving <dir>
#   verify.sh stop  <port>        # kill server on <port> and clean up log
#
# Exit codes:
#   0 OK
#   1 server failed to start / stop
#   2 bad usage

set -euo pipefail

case "${1:-}" in
  start)
    dir="${2:-.}"
    if [ ! -d "$dir" ]; then
      echo "error: not a directory: $dir" >&2
      exit 1
    fi

    # Ask the kernel for a free ephemeral port.
    port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("localhost",0)); print(s.getsockname()[1]); s.close()')"
    if [ -z "$port" ]; then
      echo "error: could not find free port" >&2
      exit 1
    fi

    log="/tmp/mermaid-viewer-verify.$port.log"

    (cd "$dir" && python3 -m http.server "$port" >"$log" 2>&1) &
    pid=$!
    disown 2>/dev/null || true

    # Wait up to ~2s for the port to respond
    ok=0
    for _ in 1 2 3 4 5 6 7 8; do
      if curl -sSf -o /dev/null "http://localhost:$port/" 2>/dev/null; then
        ok=1; break
      fi
      sleep 0.25
    done

    if [ "$ok" -ne 1 ] || ! kill -0 "$pid" 2>/dev/null; then
      echo "error: server did not start; see $log" >&2
      exit 1
    fi

    # Tab-delimited output so the caller can parse with cut/awk.
    printf "http://localhost:%s/\t%s\t%s\n" "$port" "$pid" "$port"
    ;;

  stop)
    port="${2:-}"
    [ -z "$port" ] && { echo "error: stop requires <port>" >&2; exit 2; }
    # pkill by command pattern — avoids stale PIDs if the background shell died.
    pkill -f "http.server $port" 2>/dev/null || true
    rm -f "/tmp/mermaid-viewer-verify.$port.log"
    echo "stopped"
    ;;

  *)
    echo "usage: $(basename "$0") {start <dir> | stop <port>}" >&2
    exit 2
    ;;
esac
