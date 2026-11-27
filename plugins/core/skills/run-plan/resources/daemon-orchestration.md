# run-plan — route D: daemon orchestration

The controller is a pure **orchestrator**. It never edits code, never runs git
in a repository, and never dispatches an implementation subagent. Every phase is
executed by its own **daemon phase run**: a fresh headless session, in its own
worktree, on its own `swarm/phase-<id>` branch, with the agent, model and effort
the phase doc declares. That same isolation lets one plan span several
repositories. The controller decides **what** runs **when**, checks each result,
lands it as a change request, and cleans up after the human merges.

Route D is selected when the swarmery daemon starts the plan in mode
`orchestrate` (its prompt says ROUTE D and gives the plan task id and the
`swarmery` binary). Rules 1–3, 7, 10, 11 and 13 of SKILL.md hold; rule 4 is
replaced: **phase runs commit on their own branches; the controller opens the
change request and never merges.**

## The client

```bash
SW="<the binary from the prompt>"           # e.g. /usr/local/bin/swarmery
$SW api GET  /api/epics                      # live state of every plan
$SW api POST /api/epics/<taskId>/phases/<phaseId>/run
$SW api POST /api/epics/<taskId>/phases/<phaseId>/land --data '{"action":"pr"}'
$SW api POST /api/epics/<taskId>/phases/<phaseId>/land --data '{"action":"return","feedback":"…"}'
$SW api DELETE /api/epics/<taskId>/phases/<phaseId>/branch
```

Exit code: `0` = 2xx, `1` = any other status (the JSON body, with its `code`,
is on stdout), `2` = the daemon did not answer. Filter `GET /api/epics` with
`jq '.[] | select(.taskId == <taskId>) | .phases'` and keep only these fields:
`id, seq, name, dependsOn, checkboxesDone, checkboxesTotal, runState,
runOutcome, completionState, completionBlockers, verifyVerdict, decisionHold,
manualOpen, reviewBlocks, landing`. The full DTO is large, so never print it
whole.

## The loop

Re-read the live state at the top of every pass. Never decide from the manifest
in the prompt, which is a snapshot taken at start.

1. **Classify every phase**:

   | State | Meaning |
   |---|---|
   | **complete** | `checkboxesTotal > 0` and all ticked, and `completionState` is not blocked |
   | **running** | `runState == "running"` |
   | **held** | `decisionHold` is set, or the last run ended `blocked`: waiting on the operator |
   | **manual** | the doc's work is a human activity (field trip, manual test, live-hardware check; often `checkboxesTotal == 0` or only `manualOpen` items left). Never started by you |
   | **ready** | not any of the above, and every `dependsOn` seq is complete |
   | **waiting** | not ready because a dependency is not complete yet |

2. **Start every ready phase** (`POST …/run`) whose result you have not yet
   rejected twice. Handle refusals by their `code`:

   | Code | Action |
   |---|---|
   | `no-free-run-slot` (409) / low quota (429) | Daemon budget is full. Try again on the next pass |
   | `deps-unmet` | Your classification was stale. Re-read the state |
   | `already-running` | Treat the phase as running |
   | `branch-dirty` | A previous run's branch still holds commits. Do NOT delete it: land it (step 4) if its work passed review, otherwise mark the phase held |
   | anything else | Mark the phase held and name the code in the final report |

3. **Check each phase that finished since the last pass** (`runState` no longer
   `running`):
   - `runOutcome == "completed"`, `completionState` not blocked, `verifyVerdict`
     not `fail`, and the newest `reviewBlocks` entry (once one exists) is not
     `FAIL` → **accepted**. Go to step 4.
   - Review `FAIL`, verify `fail`, or unticked criteria with a run that claims
     done → **return** it once with the review's open findings as feedback
     (`land {"action":"return","feedback":…}`). That restarts the phase on the
     same branch. A second rejection marks the phase **held** (SKILL.md rule 10:
     there is no third automatic round).
   - `failed` / `partial` / `noop` → start it once more. A second miss marks it
     **held**.
   - `blocked` (a `PHASE BLOCKED` stop or a decision hold) → **held**, with the
     stop's reason as written (rule 11). Do not try to work around it.

4. **Land an accepted phase**: `land {"action":"pr"}`. That pushes
   `swarm/phase-<id>` and opens a change request (`landing.state` becomes
   `pr_open`, with `landing.prUrl`). Do not wait for the merge to continue:
   the phase-run dependency gate accepts a ticked dependency and stacks the
   dependent on that branch. **Never merge.** A human merges.

5. **Clean up after the human.** For every phase whose `landing.state` is
   `merged`, delete its run branch (`DELETE …/branch`). The daemon already
   removed its worktree when the run ended. Never delete a branch whose state
   is anything else: an unmerged branch holds work, and dependents stack on it.

6. **Wait, then loop.** While any phase is running, `sleep` for at most 540
   seconds in one Bash call, then go back to step 1. Your process dies with your
   turn, and nothing else starts dependents. So never end the turn while a phase
   run you started is still running.

## Ending the run

End when no phase is running and none is ready:

- every phase complete → `PLAN DONE`;
- otherwise → `PLAN BLOCKED at phase <n>: <what the operator must do>`, naming
  the first held, manual or waiting phase on the critical path.

Before the sentinel, write a short summary into the plan's `logs/run-ledger.md`
with one line per phase: state, run outcome, PR URL, and branch deleted or not.
The plan-end drift review (rule 13) runs only on `PLAN DONE`. Dispatch its
reviewer over the merged change requests, not over a worktree, because you have
none.

A later `orchestrate` run of the same plan resumes from the live state. Complete
phases stay complete, and phases already running are adopted, not restarted.
Re-running after the human merges a batch of PRs is also how the merged branches
get cleaned up.
