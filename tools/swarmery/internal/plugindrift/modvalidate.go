package plugindrift

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/findings"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/modscan"
)

// maxModErrorLen caps the quoted validate error in the advisory message; the
// full report lives on System → Mods.
const maxModErrorLen = 200

// modValidateFindings is RuleModValidateFailed: one warn-level advisory per
// (enabled plugin, project) where the plugin is a mod whose last validate —
// the `mods` row internal/sysscan caches — carries errors.
//
// "claude binary not found" is not a failed validate (nothing ran) and is
// skipped; blindness is RuleDetectorUnavailable's job. Plugin-dir mods have no
// plugin id and are never enabled through settings, so they never match.
//
// A query failure returns nil — "not evaluated" — so the Ticker keeps the
// rule's existing rows instead of resolving them.
func modValidateFindings(db *sql.DB, projects []Project) []findings.Item {
	failed, err := failedMods(db)
	if err != nil {
		log.Printf("plugindrift: read mods: %v", err)
		return nil
	}
	out := []findings.Item{}
	for _, p := range projects {
		for _, id := range p.Enabled {
			if msg, ok := failed[id]; ok {
				out = append(out, findings.Item{Target: Target(id, p.Path), Severity: "warn", Message: msg})
			}
		}
	}
	return out
}

// failedMods maps plugin id → advisory message for every cache mod whose
// stored validate errors are non-empty.
func failedMods(db *sql.DB) (map[string]string, error) {
	rows, err := db.Query(`SELECT plugin_id, name, errors FROM mods WHERE plugin_id IS NOT NULL ORDER BY dir`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, name string
		var raw sql.NullString
		if err := rows.Scan(&id, &name, &raw); err != nil {
			return nil, err
		}
		var errs []string
		if raw.Valid {
			_ = json.Unmarshal([]byte(raw.String), &errs)
		}
		kept := errs[:0]
		for _, e := range errs {
			if e != "" && e != modscan.MsgNoBinary {
				kept = append(kept, e)
			}
		}
		if len(kept) == 0 {
			continue
		}
		first := kept[0]
		if len(first) > maxModErrorLen {
			first = strings.ToValidUTF8(first[:maxModErrorLen], "") + "…"
		}
		out[id] = fmt.Sprintf("mod %s: last `claude plugin validate` reported %d error(s): %s — see System → Mods",
			name, len(kept), first)
	}
	return out, rows.Err()
}
