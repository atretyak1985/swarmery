#!/usr/bin/env bash
# inventory.sh — report on an inventory file.
#
# Usage:
#   inventory.sh FILE            print a table of every item
#   inventory.sh --total FILE    print the total quantity on hand
#   inventory.sh --low N FILE    print only items with qty below N
#
# FILE holds one item per line as `sku,qty,location`. Blank lines and lines
# starting with `#` are ignored.
set -euo pipefail

usage() {
  echo "usage: inventory.sh [--total | --low N] FILE" >&2
  exit 2
}

mode=table
low=
while [ $# -gt 0 ]; do
  case "$1" in
    --total) mode=total; shift ;;
    --low)
      [ $# -ge 2 ] || usage
      mode=low; low=$2; shift 2 ;;
    -*) usage ;;
    *) break ;;
  esac
done
[ $# -eq 1 ] || usage
file=$1
[ -r "$file" ] || { echo "inventory.sh: cannot read $file" >&2; exit 1; }

# items prints the data lines of FILE, one `sku,qty,location` per line.
items() {
  grep -v -e '^[[:space:]]*$' -e '^[[:space:]]*#' "$file" || true
}

case "$mode" in
  total)
    items | awk -F, '{ s += $2 } END { print s + 0 }'
    ;;
  table|low)
    printf '%-10s %5s  %s\n' SKU QTY LOCATION
    items | while IFS=, read -r sku qty location; do
      if [ "$mode" = low ] && [ "$qty" -ge "$low" ]; then
        continue
      fi
      printf '%-10s %5d  %s\n' "$sku" "$qty" "$location"
    done
    ;;
esac
