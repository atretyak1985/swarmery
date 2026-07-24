package api

// Auto-provision hook (auto-provision phase 3, internal/provision): after a
// successful plugin ENABLE, enqueue a single-flight provision job and run its
// install→freshness→generate pipeline asynchronously. Best-effort — the toggle
// response never waits on or fails for provisioning; failures land on the
// provision_jobs row and surface on the /api/tools architecture feed. Mirrors
// the improve seam (spawnImprove / improveGo).

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
)

// autoProvisionEnabled gates the whole behavior; SWARMERY_AUTOPROVISION=0/false/off
// disables it (the toggle reverts to settings-only). Default: enabled.
func autoProvisionEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("SWARMERY_AUTOPROVISION")))
	return v != "0" && v != "false" && v != "off"
}

// spawnProvision runs a provision pipeline asynchronously; the provisionGo seam
// (nil in production) lets tests run it inline for determinism. A panic in the
// long-running install→generate pipeline (40-min `claude -p`, external process,
// Runner) must never take the daemon down — recover, log with the label, and
// leave the row wherever it reached (HealStale sweeps a wedged in-flight row on
// the next restart). Mirrors spawnImprove.
func (h *Handler) spawnProvision(label string, fn func()) {
	wrapped := func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("error: provision: async pipeline panic recovered (%s): %v", label, r)
			}
		}()
		fn()
	}
	if h.provisionGo != nil {
		h.provisionGo(wrapped)
		return
	}
	go wrapped()
}

// enqueueProvision is the post-enable hook: single-flight enqueue + async run.
// Best-effort — provisioning failures never fail the toggle response; they land
// on the provision_jobs row and surface in the dashboard.
func (h *Handler) enqueueProvision(projectID int64, projectPath, pack string) {
	if h.Provision == nil || !autoProvisionEnabled() {
		return
	}
	id, started, err := h.Provision.Enqueue(projectID, pack)
	if err != nil {
		log.Printf("warning: provision enqueue (project %d, %s): %v", projectID, pack, err)
		return
	}
	if !started {
		return // a job is already in flight
	}
	h.spawnProvision(fmt.Sprintf("project %d, %s", projectID, pack), func() {
		if err := h.Provision.Run(context.Background(), id, projectPath, pack); err != nil {
			log.Printf("error: provision run (project %d, %s): %v", projectID, pack, err)
		}
	})
}
