package api

// GET /api/projects/{id}/architecture/blast — the blast radius of the project's
// current branch: which modules and flows of architecture-map.json the commits
// since the default branch actually touch.
//
// Read-only, so it is not origin-fenced (the daemon is loopback-only), matching
// the architecture static jail next to it in routes.go. It DOES fork git, which
// the rest of the architecture feed deliberately avoids — hence archmap's 5 s
// deadline and per-(repo, from, to) memo, and hence a handler that refuses
// early on every cheap check rather than shelling out to find out.

import (
	"net/http"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/archmap"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/githead"
)

// blastResponse is the wire shape. `files` is the COUNT of changed paths, not
// the paths themselves — the per-module file lists inside `touched` are what
// the UI renders, and a whole-branch file list on a long-lived branch is a
// payload nobody reads.
//
// `base` is the NAME the branch is measured against (usually "main"), which is
// what the UI labels the strip with; the diff itself starts at the merge base
// of that name and `head`, not at its tip.
type blastResponse struct {
	Base    string          `json:"base"`
	Head    string          `json:"head"`
	Files   int             `json:"files"`
	Touched archmap.Touched `json:"touched"`
}

// architectureBlast handles GET /api/projects/{id}/architecture/blast.
//
// base defaults to the repo's default branch (origin/HEAD, else main, else
// master) and head to the project's current HEAD; both can be overridden with
// query params, which archmap validates before they reach argv.
func (h *Handler) architectureBlast(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	_, path, ok := h.projectPathByID(w, r)
	if !ok {
		return
	}

	base := r.URL.Query().Get("base")
	if base == "" {
		base = archmap.DefaultBranch(path)
	}
	if base == "" {
		// Neither origin/HEAD nor main/master resolved — most likely not a git
		// repo at all. 409, not 500: the project is fine, there is just nothing
		// to measure it against.
		writeJSONStatus(w, http.StatusConflict, map[string]string{
			"error": "no baseline branch — the project has no origin/HEAD, main or master to diff against",
		})
		return
	}

	head := r.URL.Query().Get("head")
	if head == "" {
		sha, hok := githead.Resolve(path)
		if !hok {
			writeJSONStatus(w, http.StatusConflict, map[string]string{
				"error": "could not resolve HEAD for this project",
			})
			return
		}
		head = sha
	}

	m, err := archmap.Load(path)
	if err != nil {
		// No map (or a corrupt one) means there is nothing to attribute files
		// to. 404 rather than an empty Touched, which would read as "this
		// branch changed nothing".
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}

	// `base...head`, spelled as merge-base + a two-dot diff: the blast radius is
	// what the branch changed SINCE IT WAS CUT, not everything that differs from
	// the base branch tip. Once `base` advances after the cut, a plain
	// `base..head` folds every file those newer base commits touched into this
	// branch's answer, inflating the count with modules the branch never opened.
	// Resolving to a sha here also hands Diff an immutable memo key.
	mergeBase, err := archmap.MergeBase(r.Context(), path, base, head)
	if err != nil {
		writeJSONStatus(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}

	files, err := archmap.Diff(r.Context(), path, mergeBase, head)
	if err != nil {
		writeJSONStatus(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, blastResponse{
		Base:    base,
		Head:    head,
		Files:   len(files),
		Touched: archmap.Match(m, files),
	}, nil)
}
