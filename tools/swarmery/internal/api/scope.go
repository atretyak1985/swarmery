package api

import "net/http"

// projectScopePredicate is the single source of truth for matching the global
// ?project=<slug|id> scope against a joined projects table (aliased p). It is
// appended to a WHERE clause and binds two args: the raw project query value,
// twice (slug match, then id match). Query sites that build SQL by string
// concatenation use the const directly; the rest go through scopeFilter.
const projectScopePredicate = ` AND (p.slug = ? OR CAST(p.id AS TEXT) = ?)`

// scopeFilter resolves ?project=<slug|id> into a SQL predicate appended to a
// query that joins projects p — the same match rule everywhere a project
// scope applies (/api/sessions, /api/stats/*, /api/system/*, analytics).
// Empty when unscoped.
func scopeFilter(r *http.Request) (string, []any) {
	project := r.URL.Query().Get("project")
	if project == "" {
		return "", nil
	}
	return projectScopePredicate, []any{project, project}
}
