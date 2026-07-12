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
	mux.HandleFunc("GET /api/stats/today", h.statsToday)

	// parity: docs/stats/health (design-parity wave — dashboard endpoints)
	mux.HandleFunc("GET /api/health", h.health)
	mux.HandleFunc("GET /api/docs", h.listDocs)
	mux.HandleFunc("GET /api/docs/{slug}", h.getDoc)
	mux.HandleFunc("GET /api/stats/overview", h.statsOverview)

	// phase 2: approvals (frozen contract — docs/hooks-protocol.md).
	// All write endpoints reject foreign browser Origins (D4); requests
	// without an Origin (the swarmery hook shim, curl) pass.
	mux.HandleFunc("POST /api/hooks/permission-request", requireLocalOrigin(h.hookPermissionRequest))
	mux.HandleFunc("POST /api/hooks/stop", requireLocalOrigin(h.hookStop))
	mux.HandleFunc("POST /api/approvals/{id}", requireLocalOrigin(h.resolveApproval))
	mux.HandleFunc("GET /api/approvals", h.listApprovals)
}
