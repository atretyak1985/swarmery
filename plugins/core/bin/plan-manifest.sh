#!/usr/bin/env bash
# plan-manifest.sh <plan-dir>
#
# Emit a machine-readable execution manifest (JSON, stdout) for a plan produced by
# @implementation-planner (phase-N-*.md) or @task-planner (step-NN-*.md).
#
# Resolution order:
#   1. <plan-dir>/manifest.json exists  -> validate and print it (planner-emitted, authoritative).
#   2. Otherwise                        -> derive best-effort from the plan files:
#      phase/step docs (title, header fields) + the README sequencing table
#      (depends-on / parallelizable columns) when present.
#
# Consumed by the run-plan skill (skills/run-plan/SKILL.md). Exit 1 on unusable input.

set -euo pipefail

PLAN_DIR="${1:-}"
if [[ -z "$PLAN_DIR" || ! -d "$PLAN_DIR" ]]; then
  echo "usage: plan-manifest.sh <plan-dir>  (dir not found: '${PLAN_DIR}')" >&2
  exit 1
fi

if [[ -f "$PLAN_DIR/manifest.json" ]]; then
  python3 -m json.tool "$PLAN_DIR/manifest.json" || {
    echo "ERROR: $PLAN_DIR/manifest.json is not valid JSON" >&2
    exit 1
  }
  exit 0
fi

python3 - "$PLAN_DIR" <<'PYEOF'
import json, os, re, sys

plan_dir = os.path.abspath(sys.argv[1])

def read(path):
    try:
        with open(path, encoding="utf-8") as f:
            return f.read()
    except OSError:
        return ""

files = sorted(
    f for f in os.listdir(plan_dir)
    if re.match(r"(phase|step)-\d+.*\.md$", f)
)
if not files:
    print(f"ERROR: no phase-N-*.md / step-NN-*.md files in {plan_dir}", file=sys.stderr)
    sys.exit(1)

# README sequencing table: rows like | 1 | Title | repo | depends | parallel | ...
readme = read(os.path.join(plan_dir, "README.md"))
table = {}
header_cols = []
for line in readme.splitlines():
    if not line.strip().startswith("|"):
        continue
    cols = [c.strip() for c in line.strip().strip("|").split("|")]
    lowered = [c.lower() for c in cols]
    if any("depends" in c for c in lowered) and any("phase" in c or "step" in c for c in lowered):
        header_cols = lowered
        continue
    if header_cols and cols and re.match(r"^\d+$", cols[0]):
        row = dict(zip(header_cols, cols))
        table[int(cols[0])] = row

def col(row, *keys):
    for k in row:
        if any(key in k for key in keys):
            return row[k]
    return ""

phases = []
for fname in files:
    body = read(os.path.join(plan_dir, fname))
    num_m = re.search(r"-(\d+)", fname)
    num = int(num_m.group(1)) if num_m else len(phases) + 1
    title_m = re.search(r"^#\s+(.+)$", body, re.M)
    row = table.get(num, {})

    dep_raw = col(row, "depends")
    depends = [int(d) for d in re.findall(r"\d+", dep_raw)] if dep_raw and dep_raw not in ("—", "-", "") else []
    par_raw = col(row, "parallel").lower()
    parallel_group = par_raw if par_raw not in ("", "—", "-", "no", "n/a") else None
    repo = col(row, "repo")

    kind = "quality-gate" if re.search(r"quality.?gate|verification|hardening|\bqa\b", (title_m.group(1) if title_m else "") + fname, re.I) else "implementation"

    phases.append({
        "id": num,
        "file": fname,
        "title": title_m.group(1).strip() if title_m else fname,
        "repos": [repo] if repo else [],
        "depends_on": depends,
        "parallel_group": parallel_group,
        "kind": kind,
        "has_prompt": bool(re.search(r"copy.?paste agent prompt", body, re.I)),
        "manual_legs": "[MANUAL]" in body,
    })

# Fallback: no table-derived deps -> assume strictly sequential (planner default).
if all(not p["depends_on"] for p in phases):
    for i, p in enumerate(phases[1:], start=1):
        p["depends_on"] = [phases[i - 1]["id"]]

manifest = {
    "task": os.path.basename(os.path.dirname(plan_dir)),
    "plan_dir": plan_dir,
    "source": "derived",  # planner-emitted manifests say "planner"
    "planner": "implementation-planner" if files[0].startswith("phase") else "task-planner",
    "phases": phases,
}
missing = [p["file"] for p in phases if not p["has_prompt"]]
if missing:
    manifest["warnings"] = [f"no copy-paste prompt section found in: {', '.join(missing)}"]

print(json.dumps(manifest, indent=2))
PYEOF
