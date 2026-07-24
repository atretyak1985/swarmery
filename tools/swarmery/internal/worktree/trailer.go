package worktree

import "strings"

// trailerKey is the git commit trailer that attributes a commit to a board
// task. Adapted from Fusion's `Fusion-Task-Id:` (DESIGN.md §4.3) — deterministic
// task↔commit attribution replacing cwd/time heuristics, and the basis of
// foreign-commit (contamination) detection.
const trailerKey = "Swarm-Task-Id"

// Trailer renders the commit trailer line for a task id, e.g.
// Trailer("T-abc123") == "Swarm-Task-Id: T-abc123". Dispatched sessions are
// instructed to append this to every commit.
func Trailer(taskID string) string {
	return trailerKey + ": " + taskID
}

// CommitsForTask returns the SHAs of commits carrying this task's trailer,
// across all refs. It greps the exact trailer line (git log --grep is a regex,
// so the taskID is escaped). Newest first (git log default order).
func (m *Manager) CommitsForTask(repoRoot, taskID string) ([]string, error) {
	grep := "^" + regexpEscape(Trailer(taskID)) + "$"
	out, err := m.Git.Run(repoRoot, "log", "--all", "--format=%H", "--grep", grep, "-E")
	if err != nil {
		return nil, err
	}
	var shas []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			shas = append(shas, line)
		}
	}
	return shas, nil
}

// regexpEscape escapes the ERE metacharacters that can appear in a task id or
// the fixed trailer text, so `git log --grep` matches the literal line. Task
// ids are "T-"+base36 in practice, but the fixed "Swarm-Task-Id:" contains a
// literal '-' (harmless) and the escape keeps the grep correct if the id
// convention ever widens.
func regexpEscape(s string) string {
	const meta = `\.+*?()|[]{}^$`
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(meta, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
