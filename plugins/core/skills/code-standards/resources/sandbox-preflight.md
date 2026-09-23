# Sandbox preflight — where am I, and what is around me

Agents that edit files often run inside a git worktree isolate rather than the
operator's main checkout. Two guards watch the shell: a permission classifier
and a worktree-isolation guard. This resource explains how to work with both
instead of losing turns to them.

## One operation per Bash call

Run each exploratory or git command as its own Bash call. Do not join them
with `;`, `&&`, or `||` — the composed form is what the two guards reject. A
pipeline inside a single logical operation (`grep … | head`), a substitution
inside one command, and a heredoc script run by one interpreter are all fine.

Independent calls can go out in parallel in a single message; that is the
replacement for `&&`, and it is faster.

If a call comes back with `too complex to verify that it stays inside the
worktree`, that is not a refusal of the action — it is a request to split the
call. Split it and repeat the same work; do not reformulate the task.

## Before the first read or write

Run these as separate Bash calls:

1. `pwd` — this is your only root. Remember the value; it is called ROOT below.
2. `git rev-parse --show-toplevel` — if it differs from ROOT you are in a
   worktree. That is expected, and rule 4 applies.
3. `test -e <path>` for every target the task names, one call each. If any is
   missing, stop and report: which path is absent, which step you were on, and
   what you need to continue. Do not run a script that will fail on it, and do
   not invent a substitute.
4. Paths in a task may be written from the project root
   (`<project-root>/src/…`). Inside ROOT the same file is `src/…`. An absolute
   path that does not start with ROOT will be rejected by the sandbox and the
   turn is lost, so build absolute paths as `<ROOT>/<repo-relative path>`.
5. In a worktree, `node_modules` and `.venv` may be absent. Do not run
   `npm install` / `npm ci` / `pip install` — that mutates the shared tree.
   Link a copy from the main checkout in one call
   (`ln -s <main-checkout>/node_modules node_modules`) and check it with
   `test -d node_modules/.bin`. If neither works, stop and report rather than
   continue on hope.
6. The document you are asked to edit (a phase doc, a report) may live outside
   ROOT. If `test -e` on its path fails, say so instead of editing blind — a
   quiet write to a scratch file does not count, because the dashboard reads
   only the original.
