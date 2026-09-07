package runcore

import (
	"database/sql"
	"log"
)

// RegisteredRoots returns the paths of every live (non-archived) project the daemon
// knows — the trusted roots repopath.ResolveTrusted accepts a declared Repo cell
// inside of.
//
// This is the one allow-list for cross-project phases: a plan in project A may
// declare `**Repo:** /abs/project-B` and run there only because the operator already
// registered B. A Repo cell is untrusted markdown; the registry is what keeps it from
// placing a worktree anywhere on the disk. Read fresh on every admission (projects
// are registered at runtime), and a query failure degrades to "no extra roots" — the
// project's own path still resolves exactly as before.
func RegisteredRoots(db *sql.DB) []string {
	rows, err := db.Query(`SELECT path FROM projects WHERE archived = 0 AND TRIM(path) <> ''`)
	if err != nil {
		log.Printf("warn: runcore: registered roots: %v", err)
		return nil
	}
	defer rows.Close()
	var roots []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			log.Printf("warn: runcore: registered roots: %v", err)
			return roots
		}
		roots = append(roots, p)
	}
	return roots
}
