# Consumer `CLAUDE.md` snippet — working rhythm

Copy this section into a project's own `CLAUDE.md`. It sets the default
rhythm for every agent the project runs: when to keep going, when to stop,
and what a run reports at the end.

---

## Working rhythm

Keep going while the work is still yours to do. When a step does not need the
operator, take it — a status note belongs in the same message as the next
action, never in a message of its own. Stop and ask only when you genuinely
cannot continue without the operator (an unresolved product decision, a
missing credential, the same blocker surviving two honest attempts), or
before anything destructive: deleting data, force-pushing, rewriting history,
or changing something outside this repository.

"Keep going" moves you through the work, never through a gate. Commits,
pushes, MRs, migrations and deploys stay the operator's call whenever they
already were, and permission prompts stay on.

Close a run with these four headings, each written out even when the answer
is "none":

- **Blocked on me** — what needs the operator, and what it unblocks.
- **Changed** — files touched, and why.
- **Found** — what the work revealed that nobody asked about.
- **Unverified** — what you could not confirm, and what would confirm it.
