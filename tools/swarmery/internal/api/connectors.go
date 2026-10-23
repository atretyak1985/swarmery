package api

// connectors (MCP servers): a read + add/remove surface over Claude Code's own
// MCP configuration, read through internal/mcpcfg (which shells to `claude mcp
// …` with an injectable, test-isolated runner). GET is the unfenced read feed;
// POST/DELETE mutate and carry the same requireLocalOrigin D4 hardening as every
// other write endpoint. All argument passing is argv-slice through mcpcfg —
// user input never reaches a shell.

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/mcpcfg"
)

// connReader is attached once at startup (cmdServe). nil (mock/serve-less
// builds) makes every connectors endpoint answer 503.
var connReader *mcpcfg.Reader

// AttachConnectorReader wires the MCP-config reader; tests attach a reader built
// on a fake runner.
func AttachConnectorReader(r *mcpcfg.Reader) { connReader = r }

// connectorsCmdTimeout bounds each `claude mcp …` invocation so a hung CLI
// cannot wedge a request.
const connectorsCmdTimeout = 15 * time.Second

type connectorDTO struct {
	Name      string `json:"name"`
	Transport string `json:"transport"`
	Scope     string `json:"scope"`
	Status    string `json:"status"`
	Detail    string `json:"detail"`
	Source    string `json:"source"`
}

type connectorsResponse struct {
	Connectors []connectorDTO `json:"connectors"`
}

type addConnectorRequest struct {
	Name      string   `json:"name"`
	Transport string   `json:"transport"`
	Command   string   `json:"command"`
	Args      []string `json:"args"`
	URL       string   `json:"url"`
	Scope     string   `json:"scope"`
}

// writeConnectorErr is the single error mapper for all three connectors
// handlers. Validation failures are the caller's fault (400); a claude CLI the
// daemon cannot locate is the same "this host cannot serve connectors" class as
// an unattached reader (503, with an actionable hint) rather than a daemon
// fault; everything else — a genuine CLI failure — keeps the 500 it had.
//
// The hint is a static string: no paths, no env values, no account names, so
// nothing about the operator's filesystem reaches the response body.
func writeConnectorErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, mcpcfg.ErrInvalidInput):
		writeClientErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, mcpcfg.ErrClaudeNotFound):
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{
			"error": "claude CLI not found on the daemon's PATH",
			"hint":  "set SWARMERY_CLAUDE_BIN to the claude executable (or install it in a standard location) and restart the daemon",
		})
	default:
		writeErr(w, err)
	}
}

func toConnectorDTOs(servers []mcpcfg.Server) []connectorDTO {
	out := make([]connectorDTO, 0, len(servers))
	for _, s := range servers {
		out = append(out, connectorDTO{
			Name:      s.Name,
			Transport: string(s.Transport),
			Scope:     string(s.Scope),
			Status:    string(s.Status),
			// The command line or URL `claude mcp list` prints routinely
			// embeds credentials (an --api-key flag, a token query, userinfo).
			Detail: redact(s.Detail),
			Source: s.Source,
		})
	}
	return out
}

// listConnectors is the shared read used by GET and by the refreshed response
// returned from a successful mutation.
func (h *Handler) writeConnectorList(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), connectorsCmdTimeout)
	defer cancel()
	servers, err := connReader.List(ctx)
	if err != nil {
		writeConnectorErr(w, err)
		return
	}
	writeJSON(w, connectorsResponse{Connectors: toConnectorDTOs(servers)}, nil)
}

// connectors handles GET /api/connectors.
func (h *Handler) connectors(w http.ResponseWriter, r *http.Request) {
	if connReader == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "connector reader not attached"})
		return
	}
	h.writeConnectorList(w, r)
}

// addConnector handles POST /api/connectors.
func (h *Handler) addConnector(w http.ResponseWriter, r *http.Request) {
	if connReader == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "connector reader not attached"})
		return
	}
	var req addConnectorRequest
	if err := decodeJSONLimited(w, r, &req, maxJSONBody); err != nil {
		if !isBodyTooLarge(err) {
			writeClientErr(w, http.StatusBadRequest, "malformed request body")
		}
		return
	}
	spec := mcpcfg.AddSpec{
		Name:      req.Name,
		Transport: mcpcfg.Transport(req.Transport),
		Command:   req.Command,
		Args:      req.Args,
		URL:       req.URL,
		Scope:     mcpcfg.Scope(req.Scope),
	}
	ctx, cancel := context.WithTimeout(r.Context(), connectorsCmdTimeout)
	defer cancel()
	if err := connReader.Add(ctx, spec); err != nil {
		writeConnectorErr(w, err)
		return
	}
	h.writeConnectorList(w, r)
}

// removeConnector handles DELETE /api/connectors/{name}?scope=…
func (h *Handler) removeConnector(w http.ResponseWriter, r *http.Request) {
	if connReader == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable, map[string]string{"error": "connector reader not attached"})
		return
	}
	name := r.PathValue("name")
	scope := mcpcfg.Scope(r.URL.Query().Get("scope"))
	ctx, cancel := context.WithTimeout(r.Context(), connectorsCmdTimeout)
	defer cancel()
	if err := connReader.Remove(ctx, name, scope); err != nil {
		writeConnectorErr(w, err)
		return
	}
	h.writeConnectorList(w, r)
}
