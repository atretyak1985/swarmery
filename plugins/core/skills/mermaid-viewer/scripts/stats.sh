#!/usr/bin/env bash
# stats.sh — extract diagram stats from a Mermaid source file.
#
# Emits a single-line JSON object to stdout.  POSIX awk only; no jq, no
# GNU-specific extensions — portable across macOS (BSD) and Linux (GNU).
#
# Fields emitted (only when non-zero):
#   type           "erDiagram", "flowchart", "sequence", "class", "state", "unknown"
#   tables         count of ER entity blocks (lines matching "NAME {")
#   hardFks        count of solid FK relations (||--o{)
#   logicalLinks   count of dotted relations (||..o{)
#   nodes          count of flowchart nodes (for flowchart/graph only)
#
# Example:
#   $ stats.sh schema.mmd
#   {"type":"erDiagram","tables":12,"hardFks":7,"logicalLinks":5}

set -euo pipefail

if [ $# -ne 1 ]; then
  echo "usage: $(basename "$0") <mmd-path>" >&2
  exit 2
fi

mmd="$1"
if [ ! -r "$mmd" ]; then
  echo "error: cannot read $mmd" >&2
  exit 1
fi

awk '
  function strip(s) { sub(/^[[:space:]]+/, "", s); return s }

  # Skip comments and blank lines
  /^[[:space:]]*%%/ { next }
  /^[[:space:]]*$/  { next }

  # First non-comment, non-blank line carries the diagram type token.
  !type_detected {
    line = strip($0)
    split(line, parts, /[[:space:]]/)
    word = parts[1]
    if      (word == "erDiagram")                        type = "erDiagram"
    else if (word == "flowchart" || word == "graph")     type = "flowchart"
    else if (word == "sequenceDiagram")                  type = "sequence"
    else if (word == "classDiagram")                     type = "class"
    else if (word == "stateDiagram" || word == "stateDiagram-v2") type = "state"
    else if (word == "journey")                          type = "journey"
    else if (word == "gantt")                            type = "gantt"
    else if (word == "pie")                              type = "pie"
    else if (word == "gitGraph")                         type = "gitGraph"
    else                                                 type = "unknown"
    type_detected = 1
  }

  # ER-specific counts
  /^[[:space:]]*[A-Z_][A-Z0-9_]*[[:space:]]*\{[[:space:]]*$/ { tables++ }
  /\|\|--o\{/ { hardFks++ }
  /\|\|\.\.o\{/ { logical++ }

  # Flowchart nodes — rough heuristic: NODE[label] or NODE(label) or NODE{label}
  # Count only when the diagram is declared flowchart/graph.
  {
    if (type == "flowchart") {
      n = gsub(/[A-Za-z0-9_]+[[(\{]/, "&", $0)
      nodes += n
    }
  }

  END {
    if (!type_detected) type = "unknown"
    printf "{\"type\":\"%s\"", type
    if (tables)  printf ",\"tables\":%d",       tables
    if (hardFks) printf ",\"hardFks\":%d",      hardFks
    if (logical) printf ",\"logicalLinks\":%d", logical
    if (nodes)   printf ",\"nodes\":%d",        nodes
    printf "}\n"
  }
' "$mmd"
