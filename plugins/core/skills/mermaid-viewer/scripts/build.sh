#!/usr/bin/env bash
# build.sh — materialise a Mermaid .mmd file into a themed HTML viewer.
#
# Reads templates/viewer.html.tpl, substitutes sentinels ({%%%FOO%%%}), writes
# the output.  No sed — awk's `index`/`substr` are used for literal substitution
# so the Mermaid body can contain any regex metacharacter safely.
#
# Usage:
#   build.sh --mmd <path> --out <path> --title <str>
#            [--subtitle <str>] [--stats-json <json>] [--footer <html>]
#
# Flags:
#   --mmd         path to .mmd source              (required)
#   --out         path to write .html output       (required)
#   --title       page heading + browser title     (required)
#   --subtitle    subtitle line under the heading  (default: "")
#   --stats-json  stats from stats.sh              (default: "{}")
#   --footer      footer HTML                      (default: generated)

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "$0")" && pwd)"
TEMPLATE="$SCRIPT_DIR/../templates/viewer.html.tpl"

if [ ! -r "$TEMPLATE" ]; then
  echo "error: template not found at $TEMPLATE" >&2
  exit 1
fi

mmd="" out="" title="" subtitle="" stats_json="{}" footer=""

while [ $# -gt 0 ]; do
  case "$1" in
    --mmd)        mmd="$2";        shift 2 ;;
    --out)        out="$2";        shift 2 ;;
    --title)      title="$2";      shift 2 ;;
    --subtitle)   subtitle="$2";   shift 2 ;;
    --stats-json) stats_json="$2"; shift 2 ;;
    --footer)     footer="$2";     shift 2 ;;
    -h|--help)
      sed -n '2,22p' "$0"; exit 0 ;;
    *) echo "error: unknown flag: $1" >&2; exit 2 ;;
  esac
done

[ -z "$mmd"   ] && { echo "error: --mmd is required"   >&2; exit 2; }
[ -z "$out"   ] && { echo "error: --out is required"   >&2; exit 2; }
[ -z "$title" ] && { echo "error: --title is required" >&2; exit 2; }
[ -r "$mmd"   ] || { echo "error: cannot read $mmd"    >&2; exit 1; }

# ---------------------------------------------------------------------
# Build meta-badges HTML from the stats JSON.
# ---------------------------------------------------------------------
extract_num() {
  # $1 = key, $2 = JSON string.  Returns the integer value or empty.
  # Looks for "key":<digits>  — order-independent, no nesting supported.
  echo "$2" | awk -v key="\"$1\":" '
    {
      p = index($0, key)
      if (p == 0) next
      rest = substr($0, p + length(key))
      n = 0
      while (n < length(rest)) {
        c = substr(rest, n+1, 1)
        if (c < "0" || c > "9") break
        n++
      }
      if (n > 0) { print substr(rest, 1, n); exit }
    }
  '
}
extract_str() {
  echo "$2" | awk -v key="\"$1\":\"" '
    {
      p = index($0, key)
      if (p == 0) next
      rest = substr($0, p + length(key))
      q = index(rest, "\"")
      if (q > 0) { print substr(rest, 1, q-1); exit }
    }
  '
}

type_s="$(extract_str type         "$stats_json")"
tables="$(extract_num tables       "$stats_json")"
hard_fks="$(extract_num hardFks    "$stats_json")"
logical="$(extract_num logicalLinks "$stats_json")"
nodes="$(extract_num nodes         "$stats_json")"

badges=""
append_badge() {
  local variant="$1" value="$2" label="$3"
  if [ "$variant" = "primary" ]; then
    badges+="<span class=\"badge badge-primary\"><span class=\"badge-dot\"></span><strong>${value}</strong>&nbsp;${label}</span>"
  else
    badges+="<span class=\"badge\"><strong>${value}</strong>&nbsp;${label}</span>"
  fi
}

if [ -n "$tables" ];   then append_badge primary   "$tables"   "tables";        fi
if [ -n "$nodes" ];    then append_badge primary   "$nodes"    "nodes";         fi
if [ -n "$hard_fks" ]; then append_badge ""        "$hard_fks" "hard FKs";      fi
if [ -n "$logical" ];  then append_badge ""        "$logical"  "logical links"; fi
if [ -n "$type_s" ];   then append_badge ""        "$type_s"   "";              fi

# ---------------------------------------------------------------------
# Default footer if not provided.
# ---------------------------------------------------------------------
if [ -z "$footer" ]; then
  footer="Generated from <code>$(basename "$mmd")</code> · Mermaid Viewer"
fi

# ---------------------------------------------------------------------
# Read and sanitise Mermaid body.
# Guard against </script> injection (which would escape our text/plain
# preservation script).  Any real "</script" inside a Mermaid comment
# becomes "<\/script" — the browser still accepts it, Mermaid parser
# sees the unescaped form because textContent decodes the backslash.
# ---------------------------------------------------------------------
mermaid_body="$(awk '{ gsub(/<\/script/, "<\\/script"); print }' "$mmd")"

# Basename for download button filenames (strip any extension)
download_basename="$(basename "$mmd")"
download_basename="${download_basename%.*}"

# ---------------------------------------------------------------------
# Literal sentinel substitution via awk (no regex on the replacement).
# ---------------------------------------------------------------------
subst() {
  local sentinel="$1" value="$2"
  MV_SENT="$sentinel" MV_VAL="$value" awk '
    BEGIN {
      sent = ENVIRON["MV_SENT"]
      val  = ENVIRON["MV_VAL"]
      sl   = length(sent)
    }
    {
      line = $0
      out = ""
      while ((p = index(line, sent)) > 0) {
        out  = out substr(line, 1, p - 1) val
        line = substr(line, p + sl)
      }
      print out line
    }
  '
}

cat "$TEMPLATE" \
  | subst "{%%%PAGE_TITLE%%%}"         "$title"            \
  | subst "{%%%HEADING%%%}"            "$title"            \
  | subst "{%%%SUBTITLE%%%}"           "$subtitle"         \
  | subst "{%%%META_BADGES%%%}"        "$badges"           \
  | subst "{%%%FOOTER_HTML%%%}"        "$footer"           \
  | subst "{%%%DOWNLOAD_BASENAME%%%}"  "$download_basename" \
  | subst "{%%%MERMAID_BODY%%%}"       "$mermaid_body"     \
  > "$out"

# Safety net: bail if any sentinel was left unsubstituted.
if grep -q '{%%%' "$out"; then
  echo "error: unsubstituted sentinels remain in $out:" >&2
  grep -n '{%%%' "$out" | head -5 >&2
  exit 1
fi

echo "wrote $out"
