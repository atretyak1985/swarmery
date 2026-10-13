package sysscan

// Mods pass (mods-swarmery-link phase 9): plugins whose hooks/hooks.json lists
// `modules` are discovered by internal/modscan and validated with
// `claude plugin validate --strict --json` — a CLI run, so it happens only
// when a mod's module_sha changed or on a forced rescan. The parsed result is
// cached in the `mods` table (0112), one row per plugin dir.
//
// Same scanner contract as the other kinds: read-only on disk, tolerant (a
// broken mod is a row with errors plus a parse_error finding, never a failed
// pass), hash-skip, and a system_item_updated note with Kind "mod" whenever a
// row is created, changes, or is removed.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/modscan"
)

// ModStats reports one mods pass.
type ModStats struct {
	Mods        int `json:"mods"`        // mods present on disk this pass
	Validated   int `json:"validated"`   // validate runs this pass
	ParseErrors int `json:"parseErrors"` // validate runs that produced no report
	Removed     int `json:"removed"`     // rows dropped because their dir vanished
}

func (m ModStats) String() string {
	return fmt.Sprintf("mods=%d validated=%d parse_errors=%d removed=%d",
		m.Mods, m.Validated, m.ParseErrors, m.Removed)
}

// ModTarget is the config_lint_findings target of a mod row.
func ModTarget(id int64) string { return fmt.Sprintf("%s:%d", KindMod, id) }

// ScanMods runs the mods pass on its own — the full Scan calls it with
// force=false; POST /api/system/mods/rescan calls it with force=true, which
// re-validates every mod regardless of module_sha. Passes are serialized, so a
// manual rescan never races the periodic one into a double validate.
func (s *Scanner) ScanMods(ctx context.Context, force bool) (ModStats, error) {
	s.modsMu.Lock()
	defer s.modsMu.Unlock()

	var st ModStats
	mods := modscan.Discover(s.cfg.ClaudeDir, modscan.PluginDirsFromSettings(s.cfg.ClaudeDir))
	seen := make(map[string]bool, len(mods))
	for _, m := range mods {
		seen[m.Dir] = true
		st.Mods++
		if err := s.scanMod(ctx, m, force, &st); err != nil {
			return st, err
		}
	}
	removed, err := s.sweepMods(seen)
	st.Removed = removed
	return st, err
}

// modRow is the stored state a pass compares against.
type modRow struct {
	id     int64
	sha    sql.NullString
	report sql.NullString
}

func (s *Scanner) scanMod(ctx context.Context, m modscan.ModDir, force bool, st *ModStats) error {
	var row modRow
	err := s.db.QueryRow(`SELECT id, module_sha, report FROM mods WHERE dir = ?`, m.Dir).
		Scan(&row.id, &row.sha, &row.report)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("mods %s: %w", m.Dir, err)
	}

	sha, herr := modscan.Hash(m.Dir)
	if herr == nil && exists && !force && row.sha.Valid && row.sha.String == sha {
		return nil // unchanged — the cached validate stands
	}

	rep, verr := modscan.Report{}, error(nil)
	if herr != nil {
		// Discover just read hooks.json, so this is a race with an edit; the
		// next pass retries (module_sha stays NULL).
		rep, verr = modscan.EmptyReport(), &modscan.ParseError{Dir: m.Dir, Reason: "hash", Err: herr}
	} else {
		st.Validated++
		rep, verr = modscan.Validate(ctx, s.cfg.ClaudeBin, m.Dir)
	}
	if ctx.Err() != nil {
		return ctx.Err() // a cancelled rescan stores nothing half-done
	}

	var perr *modscan.ParseError
	storedSHA := any(sha)
	switch {
	case errors.As(verr, &perr):
		st.ParseErrors++
		rep = modscan.EmptyReport()
		rep.Errors = []string{perr.Error()}
	case verr != nil:
		return fmt.Errorf("mods %s: %w", m.Dir, verr)
	}
	if herr != nil || (len(rep.Errors) == 1 && rep.Errors[0] == modscan.MsgNoBinary) {
		// Retry next pass: hashing failed, or there was no claude to ask — a
		// cheap check, and installing claude must not need a manual rescan.
		storedSHA = nil
	}

	reportJSON := mustJSON(rep)
	changed := !exists || !row.report.Valid || row.report.String != reportJSON
	id, err := s.upsertMod(m, storedSHA, rep, reportJSON)
	if err != nil {
		return fmt.Errorf("mods %s: upsert: %w", m.Dir, err)
	}

	target := ModTarget(id)
	if perr != nil {
		if ferr := s.upsertFinding(target, "parse_error", "error", perr.Error()); ferr != nil {
			return fmt.Errorf("mods %s: finding: %w", m.Dir, ferr)
		}
	} else if ferr := s.resolveFinding(target, "parse_error"); ferr != nil {
		return fmt.Errorf("mods %s: finding resolve: %w", m.Dir, ferr)
	}
	if changed {
		s.publish(KindMod, id)
	}
	return nil
}

// upsertMod writes one row keyed by dir and returns its id.
func (s *Scanner) upsertMod(m modscan.ModDir, sha any, rep modscan.Report, reportJSON string) (int64, error) {
	risk := modscan.Risk(rep)
	_, err := s.db.Exec(`
		INSERT INTO mods (name, plugin_id, dir, source, version, module_sha, events, uses, env, state, risk, validated_at, errors, report)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(dir) DO UPDATE SET
		  name = excluded.name, plugin_id = excluded.plugin_id, source = excluded.source,
		  version = excluded.version, module_sha = excluded.module_sha, events = excluded.events,
		  uses = excluded.uses, env = excluded.env, state = excluded.state, risk = excluded.risk,
		  validated_at = excluded.validated_at, errors = excluded.errors, report = excluded.report`,
		m.Name, nullStr(m.PluginID), m.Dir, m.Source, nullStr(m.Version), sha,
		mustJSON(rep.Events), mustJSON(rep.Uses), mustJSON(rep.Env), mustJSON(rep.State),
		mustJSON(risk), nowRFC3339(), mustJSON(rep.Errors), reportJSON)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.db.QueryRow(`SELECT id FROM mods WHERE dir = ?`, m.Dir).Scan(&id)
	return id, err
}

// sweepMods deletes rows whose dir was not discovered this pass (a cache
// version bump moves a mod's dir; an uninstalled plugin is gone). The table is
// a cache of the last validate, so a hard delete loses nothing; the row's open
// parse_error finding is resolved with it.
func (s *Scanner) sweepMods(seen map[string]bool) (int, error) {
	rows, err := s.db.Query(`SELECT id, dir FROM mods`)
	if err != nil {
		return 0, fmt.Errorf("mods sweep: %w", err)
	}
	var stale []int64
	for rows.Next() {
		var id int64
		var dir string
		if err := rows.Scan(&id, &dir); err != nil {
			rows.Close()
			return 0, fmt.Errorf("mods sweep: %w", err)
		}
		if !seen[dir] {
			stale = append(stale, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("mods sweep: %w", err)
	}
	for _, id := range stale {
		if _, err := s.db.Exec(`DELETE FROM mods WHERE id = ?`, id); err != nil {
			return 0, fmt.Errorf("mods sweep #%d: %w", id, err)
		}
		if err := s.resolveFinding(ModTarget(id), "parse_error"); err != nil {
			return 0, fmt.Errorf("mods sweep #%d: finding: %w", id, err)
		}
		s.publish(KindMod, id)
	}
	return len(stale), nil
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}
