package api

// mods-swarmery-link phase 9: System → Mods. Read surface over the `mods`
// table (0112) that internal/sysscan fills from `claude plugin validate
// --strict --json`, plus a local-origin rescan that forces a re-validate.
// The folder path is shown; module source never is — the detail carries the
// validator's report (its notes name what a module hooks and calls), not the
// module files.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/modscan"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/sysscan"
)

// ModRescanner is what POST /api/system/mods/rescan drives — the daemon's
// *sysscan.Scanner.
type ModRescanner interface {
	ScanMods(ctx context.Context, force bool) (sysscan.ModStats, error)
}

// modRescanner is attached once at daemon startup (nil → rescan serves 503),
// the package-variable pattern of AttachHookEditor.
var modRescanner ModRescanner

// AttachModRescanner wires the system scanner into the mods rescan endpoint.
func AttachModRescanner(r ModRescanner) { modRescanner = r }

// modDTO is one row of GET /api/system/mods.
type modDTO struct {
	ID          int64             `json:"id"`
	Name        string            `json:"name"`
	PluginID    *string           `json:"pluginId"` // "<name>@<marketplace>"; null for plugin-dir mods
	Dir         string            `json:"dir"`
	Source      string            `json:"source"` // cache | plugin-dir
	Version     *string           `json:"version"`
	ModuleSHA   *string           `json:"moduleSha"` // null = validate is retried next pass
	Events      []string          `json:"events"`
	Uses        []string          `json:"uses"`
	Env         modscan.EnvAccess `json:"env"`
	State       []string          `json:"state"`
	Risk        []modscan.Badge   `json:"risk"`
	Errors      []string          `json:"errors"`
	ValidatedAt *string           `json:"validatedAt"`
}

// modDetailDTO is GET /api/system/mods/{id}: the row plus the full report.
type modDetailDTO struct {
	modDTO
	Report modscan.Report `json:"report"`
}

const modColumns = `id, name, plugin_id, dir, source, version, module_sha,
	events, uses, env, state, risk, errors, validated_at, report`

// scanModRow reads one modColumns row; JSON columns that are NULL or
// unreadable decode to empty values, never null.
func scanModRow(sc interface{ Scan(...any) error }) (modDetailDTO, error) {
	var d modDetailDTO
	var events, uses, env, state, risk, errs, report sql.NullString
	if err := sc.Scan(&d.ID, &d.Name, &d.PluginID, &d.Dir, &d.Source, &d.Version, &d.ModuleSHA,
		&events, &uses, &env, &state, &risk, &errs, &d.ValidatedAt, &report); err != nil {
		return d, err
	}
	d.Events = decodeStrings(events)
	d.Uses = decodeStrings(uses)
	d.State = decodeStrings(state)
	d.Errors = decodeStrings(errs)
	_ = json.Unmarshal([]byte(env.String), &d.Env)
	if d.Env.Reads == nil {
		d.Env.Reads = []string{}
	}
	if d.Env.Writes == nil {
		d.Env.Writes = []string{}
	}
	_ = json.Unmarshal([]byte(risk.String), &d.Risk)
	if d.Risk == nil {
		d.Risk = []modscan.Badge{}
	}
	for i := range d.Risk {
		if d.Risk[i].Matches == nil {
			d.Risk[i].Matches = []string{}
		}
	}
	d.Report = modscan.EmptyReport()
	if report.Valid {
		_ = json.Unmarshal([]byte(report.String), &d.Report)
	}
	return d, nil
}

func decodeStrings(s sql.NullString) []string {
	var out []string
	if s.Valid {
		_ = json.Unmarshal([]byte(s.String), &out)
	}
	if out == nil {
		out = []string{}
	}
	return out
}

// GET /api/system/mods — every cached mod, by name then dir.
func (h *Handler) listSystemMods(w http.ResponseWriter, r *http.Request) {
	rows, err := h.DB.Query(`SELECT ` + modColumns + ` FROM mods ORDER BY name, dir`)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	out := []modDTO{}
	for rows.Next() {
		d, err := scanModRow(rows)
		if err != nil {
			writeErr(w, err)
			return
		}
		out = append(out, d.modDTO)
	}
	writeJSON(w, out, rows.Err())
}

// GET /api/system/mods/{id} — the row plus the parsed validate report.
func (h *Handler) getSystemMod(w http.ResponseWriter, r *http.Request) {
	id, ok := systemItemID(w, r)
	if !ok {
		return
	}
	d, err := scanModRow(h.DB.QueryRow(`SELECT `+modColumns+` FROM mods WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		writeClientErr(w, http.StatusNotFound, "mod not found")
		return
	}
	writeJSON(w, d, err)
}

// POST /api/system/mods/rescan — re-validates every mod now (force), then
// answers with the pass's counts. Synchronous: the client refetches the list.
func (h *Handler) rescanSystemMods(w http.ResponseWriter, r *http.Request) {
	if modRescanner == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "mods scanner not attached (serve without --no-ingest)")
		return
	}
	st, err := modRescanner.ScanMods(r.Context(), true)
	writeJSON(w, st, err)
}
