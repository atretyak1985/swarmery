package api

// Parity wave: daemon health endpoint for the dashboard header.
//
// Response shape is FROZEN by the parity contract (snake_case):
//   {"status":"ok","version":"<semver>","db_size_bytes":<int>,"watching":<bool>}

import (
	"net/http"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/version"
)

type healthDTO struct {
	Status      string `json:"status"`
	Version     string `json:"version"`
	DBSizeBytes int64  `json:"db_size_bytes"`
	Watching    bool   `json:"watching"`
}

// GET /api/health
//
// db_size_bytes is computed from the live connection (page_count ×
// page_size), so it needs no filesystem access to the DB path. watching is
// true when the ingest pipeline is attached (serve without --no-ingest).
func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	var size int64
	err := h.DB.QueryRow(
		`SELECT page_count * page_size FROM pragma_page_count(), pragma_page_size()`).Scan(&size)
	writeJSON(w, healthDTO{
		Status:      "ok",
		Version:     version.Version,
		DBSizeBytes: size,
		Watching:    h.Watching,
	}, err)
}
