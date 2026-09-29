Add a `--json` flag to `inventory.sh`.

- `inventory.sh --json FILE` prints every item as one JSON array of objects with the
  keys `sku` (string), `qty` (number) and `location` (string), in file order.
  For `sample.csv` that is:
  `[{"sku":"A-100","qty":12,"location":"aisle-1"},{"sku":"B-200","qty":3,"location":"aisle-2"},{"sku":"C-300","qty":0,"location":"backroom"}]`
- A file with no items prints `[]`.
- `--json` combines with `--low N`: `inventory.sh --json --low 5 FILE` (either order)
  prints only the items with qty below N, as JSON.
- Keep the script plain bash plus standard POSIX tools; do not depend on `jq`.
- The existing modes (default table, `--total`, `--low N`) must behave exactly as before.
  Update the usage text.
