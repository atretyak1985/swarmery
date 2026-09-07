# run-plan — durable progress, the two hard gates, invariants, failure modes

## Durable progress

Append one line per event to `<task-dir>/logs/run-ledger.md`:

```
phase 1: dispatched wt=<path> base=<sha7>
phase 1: review clean (spec ✅ quality ✅) head=<sha7>
phase 2: [MANUAL] browser leg DEFERRED — env down
```

On invocation, **read the ledger first**: phases marked reviewed-clean are DONE —
never re-dispatch them. Re-dispatching completed work is the most expensive known
failure after context compaction. Trust the ledger and `git log` over recollection.

## Progress contract (hard gate)

A phase is NOT complete until every satisfied acceptance criterion in its phase
doc is flipped `- [ ]` → `- [x]` (Edit tool, plan doc in the workspace task dir).
Tick immediately after verifying each criterion — never in a batch at the end.
When you accept delegated work from a subagent, YOU tick the boxes as part of
acceptance. The platform derives all plan progress from these checkboxes;
untracked completion is invisible completion. Criteria that were NOT satisfied
stay unticked — never tick to "close out" a phase.

## Summary contract (hard gate)

Ticks say WHICH criteria are met; they never say what was built. The prose account
goes into the phase doc's own `## Completion Report` section — fill the planner's
stub, or append the section at the end of the doc when the plan has no stub.
Contents: what shipped, files and commits (SHAs), verification output, deviations
from the plan's design, anything DEFERRED, and a mandatory **Blocked calls**
field — which tool calls were refused or failed and how you got around them (or
that you did not), one line each, written as "none" when the list is empty;
omitting the field is not the same as having nothing to report. Keep it under
~50 lines.

That section is the ONLY per-phase summary the platform surfaces: it parses
`## Completion Report` out of the doc and renders it as the phase's Summary tab. A
report living in `<task-dir>/reports/phase-<N>-report.md`, in the run ledger, or in
a subagent's final message is invisible there — the operator sees "no summary of
the work written" over a phase that shipped. Write both: the `reports/` file is the
long form and the working record, the doc section is the summary. When you accept a
subagent's work, YOU write the doc section as part of acceptance, the same way you
own the ticks. A phase without a Completion Report is not done.

## Invariants (all routes)

- ASK gates are the controller's: commits, pushes, MRs, migrations, and deploys
  are surfaced to the user with the plan's own rollback notes. Executors never
  commit.
- The plan is authority for WHAT; this skill is authority for HOW-to-run. An
  executor deviating from the plan's design must say so in its report; a plan step
  that turns out wrong goes back to the user (or the planner), never silently
  "fixed".
- Every dispatch carries the 4-field brief — objective, output format plus length
  budget, tools/skills guidance, boundaries — per the delegation contract.
- After the last phase: a final whole-branch review (fresh reviewer, full branch
  diff), then `SUMMARY.md` at the task root (via the `session-closeout` skill or
  inline), then worktree cleanup. Only then report done.

## Failure modes

| Failure | Detection | Recovery |
|---|---|---|
| Implementer BLOCKED / NEEDS_CONTEXT | status in report | Missing context → supply it and re-dispatch; task too large → split; plan wrong → escalate to the user |
| Review loop stuck (>2 fix rounds) | ledger shows a 3rd re-review | Stop; present the disagreement to the user |
| Derived manifest mis-reads the DAG | README table absent or odd | Treat the plan as fully sequential (safe default); note it in the ledger |
| Merge conflict on route P integration | `git merge` fails | Dedicated fix dispatch in the integration worktree; re-run phase verification |
| Context compacted mid-run | ledger has entries you do not remember | Resume from ledger + `git log`; never restart at phase 1 |
| Plan cites files that no longer exist | executor reports drift | Halt the phase; send the plan back for revision (planner or user) |
| Phase shipped but its Summary tab is empty | dashboard shows "no summary of the work written" | The doc has no `## Completion Report`; write it now from `reports/phase-<N>-report.md` plus the phase's commits — do not leave it for the end of the run |
