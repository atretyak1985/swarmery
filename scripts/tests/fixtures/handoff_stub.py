#!/usr/bin/env python3
"""Stub daemon for scripts/tests/session-context-bridge.test.sh.

Serves GET /api/handoffs/latest the way the real daemon does, so the hook can
be exercised against a 200 and a 204 without a running daemon or a database.

    handoff_stub.py <mode>

      hit             every request -> 200 with a short brief
      hit-then-empty  first request -> 200, every later one -> 204
      empty           every request -> 204
      big             every request -> 200 with an oversized brief (cap test)
      html            every request -> 200 with the SPA index.html, which is
                      what a daemon PREDATING this route actually answers

Binds an ephemeral port, prints "PORT <n>" on stdout, then serves until killed.
"""

import json
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer

MODE = sys.argv[1] if len(sys.argv) > 1 else "hit"
BRIEF = "# Handoff: bridge the context\n## Next step\n- finish phase 1\n"
BIG_BRIEF = ("handoff-filler " * 6 + "\n") * 60
SERVED = {"n": 0}


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):  # noqa: N802 — the base class spells it this way
        if not self.path.startswith("/api/handoffs/latest"):
            self.send_response(404)
            self.end_headers()
            return

        SERVED["n"] += 1
        mode = MODE
        if mode == "hit-then-empty":
            mode = "hit" if SERVED["n"] == 1 else "empty"

        if mode == "empty":
            self.send_response(204)
            self.end_headers()
            return

        if mode == "html":
            page = b"<!doctype html>\n<html lang=\"en\"><head><title>dashboard</title></head></html>\n"
            self.send_response(200)
            self.send_header("Content-Type", "text/html")
            self.send_header("Content-Length", str(len(page)))
            self.end_headers()
            self.wfile.write(page)
            return

        body = json.dumps(
            {
                "session_uuid": "u-stub-1",
                "created_at": "2026-09-17T18:30:00Z",
                "context_tokens": 164000,
                "brief": BIG_BRIEF if mode == "big" else BRIEF,
            }
        ).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass  # keep the suite's output clean


def main():
    srv = HTTPServer(("127.0.0.1", 0), Handler)
    print("PORT %d" % srv.server_address[1], flush=True)
    srv.serve_forever()


if __name__ == "__main__":
    main()
