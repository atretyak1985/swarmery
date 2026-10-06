#!/bin/bash
# Fixture PreToolUse hook for scripts/tests/prod-deploy-ask-probe.sh: answers
# permissionDecision "deny" for every call it sees and keeps the evidence (see
# hook-lib.sh).
# shellcheck disable=SC1091  # sibling fixture, resolved at runtime
. "$(dirname "${BASH_SOURCE[0]}")/hook-lib.sh"
probe_hook deny
