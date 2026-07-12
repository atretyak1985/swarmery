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
	// (live updates via WebSocket — registered by branch A; do not add here)

	// wave C: stats
	mux.HandleFunc("GET /api/stats/today", h.statsToday)
}
