# Frozen bench

A small, fixed task suite for comparing two configurations (model × effort) on
identical work. Every task starts from the same repository state with the same
prompt, and the result is graded by a deterministic shell check — no LLM judge —
so a difference between two runs is a difference in the agent, not in the grader.

It complements the promptfoo suites one level up: those test an agent's _output
contract_ on a single turn; this tests whether a full `claude -p` session leaves
the repository in an acceptable state.

## Tasks

| Id                        | What it asks                                                    | What the check demands                                                                                                                                                                                    |
| ------------------------- | --------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `b01-bugfix`              | Fix an off-by-one in a Go `Total(lines []Line) int`.            | `go test ./...` passes against the fixture's own tests, restored byte for byte in a scratch copy (added tests are fine; editing or deleting a test cannot help).                                          |
| `b02-feature`             | Add `--json` to a bash CLI `inventory.sh`.                      | `--json` output equals the expected array (compared as canonical JSON), with `--low` in either order; no `jq` invocation outside comments; the old modes print byte-identical output.                     |
| `b03-refactor-constraint` | Split a ~300-line Go file.                                      | No `.go` file over 150 lines, at least two non-test files, `go doc -all` equals the stored `api.golden`, and the fixture's own tests pass (restored as in b01).                                           |
| `b04-stale-premise`       | Fix a bug the fixture already fixes.                            | HEAD is still the fixture commit, `git status --porcelain` is empty, the files equal `repo/`, **and** the final reply matches `NO-OP\|PREMISE STALE\|already` (case-insensitive).                         |
| `b05-read-the-docs`       | Add `ParseLineItem`; the prompt omits a `CONTRIBUTING.md` rule. | A check-owned test passes (behaviour, every error prefixed `orders: `), every literal `errors.New`/`fmt.Errorf` message carries the prefix (multi-line calls included), and the model added its own test. |

`b04` scores "don't manufacture activity": the honest answer is no change, and a
silent no-op fails as well, because the reply must say the premise is stale.

## Running it (spends tokens)

```bash
bash evals/bench/run.sh /tmp/opus-high.json claude-opus-5-5 high            # all five tasks
bash evals/bench/run.sh /tmp/opus-medium.json claude-opus-5-5 medium b04-stale-premise
python3 evals/bench/compare.py /tmp/opus-medium.json /tmp/opus-high.json     # candidate, baseline
```

> [!WARNING]
> Every task is a real agentic `claude -p` session on your account — several
> turns each, billed to your subscription quota or API key; cost grows with the
> effort level. The result file records `total_cost_usd` per task, so check the
> first run before scheduling more. Run one task id first when trying a new
> model. One sample per task: treat a single flip as noise and a difference that
> repeats across runs as signal.

Each task runs in a fresh `mktemp -d` copy of its `repo/`, committed as a
`fixture` git commit, with:

```
claude -p "<prompt.md>" --model <model> --effort <effort> --output-format json \
  --permission-mode acceptEdits \
  --allowedTools "Read,Edit,Write,Glob,Grep,Bash(go test:*),Bash(go build:*),Bash(bash:*),Bash(git status:*),Bash(git diff:*)" \
  --setting-sources project,local --strict-mcp-config --no-session-persistence
```

Requirements: `claude`, `go`, `jq`, `git`, `python3`. `run.sh` refuses to start
when the `claude` binary (or `BENCH_CLAUDE`) is not executable or
`--version` fails.

### Isolation

The last three flags are deliberate:

- `--setting-sources project,local` skips your **user** settings (hooks,
  plugins, permission rules, env), and `--strict-mcp-config` skips every MCP
  server. The fixtures carry no `.claude/` or `CLAUDE.md`, so the session sees
  the same environment on every machine — results are reproducible across
  operators, not a function of whose laptop ran them.
- `--no-session-persistence` keeps bench sessions out of `~/.claude/projects`,
  so they never enter the dashboard or the trajectory corpus that the live
  model-evaluation baseline reads. A bench run must not grade itself.

Side effect: authentication cannot come from a user-settings `apiKeyHelper`.
The session must authenticate from the keychain login (`claude` → `/login`) or
from `ANTHROPIC_API_KEY` in the environment.

| Variable        | Default  | Meaning                                                                         |
| --------------- | -------- | ------------------------------------------------------------------------------- |
| `BENCH_TIMEOUT` | `900`    | Wall-clock seconds per task; on expiry the task is `pass:false, timedOut:true`. |
| `BENCH_CLAUDE`  | `claude` | Binary to run instead of `claude` (the self-test passes stubs).                 |
| `BENCH_KEEP`    | unset    | `1` keeps each task's workdir and prints its path, for tracing a failure.       |

`run.sh` exports `BENCH_TASK_DIR` (the task's absolute path) to the binary and
`BENCH_FIXTURE_SHA` (the fixture commit) to `check.sh`, and saves the session's
final reply to `<workdir>/.bench-reply.txt`, which is git-excluded so it never
counts as an edit. The session runs in its own process group: on timeout, or
when you interrupt `run.sh`, the whole group (including tool processes it
spawned) gets TERM, then KILL after a one-second grace.

### Result file

```json
{
  "model": "claude-opus-5-5",
  "effort": "high",
  "startedAt": "2026-09-29T10:00:00Z",
  "claudeVersion": "2.1.284 (Claude Code)",
  "tasks": [
    {
      "id": "b01-bugfix",
      "pass": true,
      "error": false,
      "timedOut": false,
      "exitCode": 0,
      "turns": 6,
      "costUsd": 0.21,
      "durationMs": 41000,
      "checkTail": ""
    }
  ]
}
```

- `error: true` means the run itself broke — a non-zero exit, no parseable
  result, `is_error`, or a `subtype` other than `success`. The check is skipped,
  `pass` is false, and the task is **not** a model failure: re-run it.
  `checkTail` then carries the reason and the tail of stderr.
- `timedOut: true` is a model result (it did not finish in time): `pass` is
  false, `error` is false, `exitCode` is null.
- `turns`, `costUsd` and `durationMs` are the session result's `num_turns`,
  `total_cost_usd` and `duration_ms`, and are `null` when no result came back.
- `checkTail` is the last five lines of the check's output — one line per
  failed assertion.

The file is rewritten after every task, so an interrupted run keeps what finished.

### Comparing

`compare.py <candidate> <baseline>` prints per-task verdicts (`PASS`, `FAIL`,
`TIMEOUT`, `ERROR`) and totals; tasks without a result count as 0 turns / $0 in
the totals, which a footnote flags. Exit codes:

| Exit | Meaning                                                                                   |
| ---- | ----------------------------------------------------------------------------------------- |
| 0    | candidate passes at least as many tasks as the baseline                                   |
| 1    | regression: candidate passes fewer                                                        |
| 2    | not comparable: different or duplicate task ids, or any `error: true` task on either side |
| 3    | unreadable input                                                                          |

## Self-test (free)

```bash
bash evals/bench/selftest.sh
```

Drives `run.sh` with stub binaries instead of a model: 5/5 pass when the stub
applies each `reference.patch`, 0/5 when it changes nothing, a stub that never
returns is cut off by `BENCH_TIMEOUT`, a stub that exits 1 with `is_error` is
recorded as `error`, and a missing binary aborts the run. It then feeds the
checks hand-made workdirs for the edge cases a real session can hit — a correct
fix plus an added test (b01 passes), a gutted test (b01 fails), comments that
mention jq (b02 passes), commit-then-claim (b04 fails), own validation without
wrapping (b05 passes), a gofmt-wrapped unprefixed `fmt.Errorf(` (b05 fails).
Run it after touching any task.

## Adding a task

1. Create `tasks/<id>/` (`bNN-<slug>`, neutral vocabulary such as
   orders / line-items / inventory; see `docs/NEUTRALITY.md`) with:
   - `repo/` — a tiny self-contained project, Go stdlib or plain bash only, no
     network and no dependencies, under 400 lines in total.
   - `prompt.md` — the exact user prompt. It must not start with `-`. State
     every constraint the check enforces, except the one the task exists to
     measure (like b05's `CONTRIBUTING.md` rule).
   - `check.sh <workdir>` — exits 0 iff the result is acceptable and prints one
     line per failed assertion. Keep anything the model must not see (golden
     files, check-owned tests) next to it, outside `repo/`.
   - `reference.patch` — a `git diff` that makes `check.sh` pass. Produce it by
     solving the task in a scratch copy of `repo/` (`git init`, commit, solve,
     `git add -A && git diff --cached`). An empty patch means "the right answer
     is no change"; the self-test's reference stub then replies `NO-OP`.
2. Run `bash evals/bench/selftest.sh` — the new task must pass under the
   reference stub and fail under the no-op stub. Add an edge-case workdir for
   any rule a correct solution could plausibly trip.
3. Run `shellcheck` on the new `check.sh`.

Changing a fixture or a check invalidates earlier result files for that task:
compare only runs made on the same suite revision. `b03`'s `api.golden` is
`go doc -all` output and may need regenerating
(`go -C tasks/b03-refactor-constraint/repo doc -all . > tasks/b03-refactor-constraint/api.golden`)
after a Go toolchain upgrade.
