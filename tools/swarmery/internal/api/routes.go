package api

import "net/http"

// Routes registers every API route on the mux.
//
// Parallel-wave contract: each wave adds routes ONLY inside its own section
// below, so branches never conflict in one spot.
func Routes(mux *http.ServeMux, h *Handler) {
	// ── core: vertical slice (this file's owner) ──
	mux.HandleFunc("GET /api/projects", h.listProjects)
	mux.HandleFunc("GET /api/sessions", h.listSessions)
	mux.HandleFunc("GET /api/sessions/{id}", h.getSession)

	// wave A: WS
	mux.HandleFunc("GET /api/ws", h.ws)

	// wave C: stats
	// (overview/stats endpoints — registered by branch C; do not add here)
}
