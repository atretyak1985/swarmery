package sysscan

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// NOTE on upserts: UNIQUE(name, scope, project_id) does not fire ON CONFLICT
// for global rows (SQLite treats NULL project_id values as distinct), so all
// item upserts are explicit SELECT-then-INSERT/UPDATE with `project_id IS ?`.
// The scanner is the only writer of these tables and the store opens SQLite
// with a single connection, so the two-step upsert is race-free.

type warnFn func(string, ...any)

// ── agents ───────────────────────────────────────────────────────────────────

// scanAgentFile indexes one agents/**/*.md file: hash-skip when unchanged,
// tolerant frontmatter parse, upsert + content version, parse_error finding.
func (s *Scanner) scanAgentFile(f mdFile, st *Stats, counter *int, seen map[int64]bool, warn warnFn) {
	content, err := os.ReadFile(f.path)
	if err != nil {
		warn("agent %s: %v", f.path, err)
		return
	}
	if !isFrontmatterStart(content) {
		return // helper file (README.md etc., §1.1) — not an agent
	}
	hash := sha256Hex(content)

	// Cheap comparator: content_hash of the current version. mtime is not
	// trusted; an unchanged file skips the parse entirely.
	if id, ok := s.unchangedRow(
		`SELECT a.id FROM agents a JOIN agent_versions v ON v.id = a.current_version_id
		 WHERE a.file_path = ? AND a.deleted = 0 AND v.content_hash = ?`, f.path, hash); ok {
		seen[id] = true
		*counter++
		return
	}

	fm, perr := parseFrontmatter(content)
	name := strField(fm, "name")
	if name == "" {
		name = stem(f.path)
	}
	if f.src.origin == "plugin" {
		name = f.src.plugin + ":" + name // composite name — collision rule, §7
	}
	var toolsJSON any // never observed in the corpus (§1.2) — NULL-heavy by design
	if _, ok := fm["tools"]; ok {
		if b, jerr := json.Marshal(fm["tools"]); jerr == nil {
			toolsJSON = string(b)
		}
	}

	id, isNew, wasDeleted, err := s.upsertAgentRow(f, name,
		nullStr(strField(fm, "model")), toolsJSON, nullStr(strField(fm, "description")))
	if err != nil {
		warn("agent %s: upsert: %v", f.path, err)
		return
	}
	seen[id] = true
	*counter++

	versioned, err := s.saveVersion("agent_versions", "agent_id", "agents", id, hash, content)
	if err != nil {
		warn("agent %s: version: %v", f.path, err)
		return
	}
	if versioned {
		st.NewVersions++
	}

	target := fmt.Sprintf("agent:%d", id)
	if perr != nil {
		st.ParseErrors++
		warn("agent %s: %v — kept with parse_error finding", f.path, perr)
		if ferr := s.upsertFinding(target, "parse_error", "error", f.path+": "+perr.Error()); ferr != nil {
			warn("agent %s: finding: %v", f.path, ferr)
		}
	} else if ferr := s.resolveFinding(target, "parse_error"); ferr != nil {
		warn("agent %s: finding resolve: %v", f.path, ferr)
	}

	if isNew || wasDeleted || versioned {
		s.publish(KindAgent, id)
	}
}

func (s *Scanner) upsertAgentRow(f mdFile, name string, model, toolsJSON, description any) (id int64, isNew, wasDeleted bool, err error) {
	var deleted int
	err = s.db.QueryRow(
		`SELECT id, deleted FROM agents WHERE name = ? AND scope = ? AND project_id IS ?`,
		name, f.src.scope, f.src.projectID).Scan(&id, &deleted)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		res, ierr := s.db.Exec(
			`INSERT INTO agents (name, scope, project_id, file_path, model, tools_json, description, origin, plugin_name, deleted)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
			name, f.src.scope, f.src.projectID, f.path, model, toolsJSON, description,
			f.src.origin, nullStr(f.src.plugin))
		if ierr != nil {
			return 0, false, false, ierr
		}
		id, err = res.LastInsertId()
		return id, true, false, err
	case err != nil:
		return 0, false, false, err
	}
	_, err = s.db.Exec(
		`UPDATE agents SET file_path = ?, model = ?, tools_json = ?, description = ?,
		        origin = ?, plugin_name = ?, deleted = 0 WHERE id = ?`,
		f.path, model, toolsJSON, description, f.src.origin, nullStr(f.src.plugin), id)
	return id, false, deleted == 1, err
}

// ── skills ───────────────────────────────────────────────────────────────────

// scanSkillDir indexes one skill directory. Identity is the dir name (§2.2);
// only the SKILL.md content is versioned — sibling resources ride by ref.
func (s *Scanner) scanSkillDir(d skillDir, st *Stats, counter *int, seen map[int64]bool, warn warnFn) {
	skillMD := d.dir + string(os.PathSeparator) + "SKILL.md"
	content, err := os.ReadFile(skillMD)
	if err != nil {
		warn("skill %s: %v", skillMD, err)
		return
	}
	hash := sha256Hex(content)

	if id, ok := s.unchangedRow(
		`SELECT s.id FROM skills s JOIN skill_versions v ON v.id = s.current_version_id
		 WHERE s.dir_path = ? AND s.deleted = 0 AND v.content_hash = ?`, d.dir, hash); ok {
		seen[id] = true
		*counter++
		return
	}

	fm, perr := parseFrontmatter(content)
	name := stem(d.dir) // dir name is the identity; frontmatter name matches it in the corpus
	if d.src.origin == "plugin" {
		name = d.src.plugin + ":" + name
	}

	id, isNew, wasDeleted, err := s.upsertSkillRow(d, name, nullStr(strField(fm, "description")))
	if err != nil {
		warn("skill %s: upsert: %v", d.dir, err)
		return
	}
	seen[id] = true
	*counter++

	versioned, err := s.saveVersion("skill_versions", "skill_id", "skills", id, hash, content)
	if err != nil {
		warn("skill %s: version: %v", d.dir, err)
		return
	}
	if versioned {
		st.NewVersions++
	}

	target := fmt.Sprintf("skill:%d", id)
	if perr != nil {
		st.ParseErrors++
		warn("skill %s: %v — kept with parse_error finding", skillMD, perr)
		if ferr := s.upsertFinding(target, "parse_error", "error", skillMD+": "+perr.Error()); ferr != nil {
			warn("skill %s: finding: %v", d.dir, ferr)
		}
	} else if ferr := s.resolveFinding(target, "parse_error"); ferr != nil {
		warn("skill %s: finding resolve: %v", d.dir, ferr)
	}

	if isNew || wasDeleted || versioned {
		s.publish(KindSkill, id)
	}
}

func (s *Scanner) upsertSkillRow(d skillDir, name string, description any) (id int64, isNew, wasDeleted bool, err error) {
	var deleted int
	err = s.db.QueryRow(
		`SELECT id, deleted FROM skills WHERE name = ? AND scope = ? AND project_id IS ?`,
		name, d.src.scope, d.src.projectID).Scan(&id, &deleted)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		res, ierr := s.db.Exec(
			`INSERT INTO skills (name, scope, project_id, dir_path, description, origin, plugin_name, deleted)
			 VALUES (?, ?, ?, ?, ?, ?, ?, 0)`,
			name, d.src.scope, d.src.projectID, d.dir, description, d.src.origin, nullStr(d.src.plugin))
		if ierr != nil {
			return 0, false, false, ierr
		}
		id, err = res.LastInsertId()
		return id, true, false, err
	case err != nil:
		return 0, false, false, err
	}
	_, err = s.db.Exec(
		`UPDATE skills SET dir_path = ?, description = ?, origin = ?, plugin_name = ?, deleted = 0 WHERE id = ?`,
		d.dir, description, d.src.origin, nullStr(d.src.plugin), id)
	return id, false, deleted == 1, err
}

// ── commands ─────────────────────────────────────────────────────────────────

// scanCommandFile indexes one commands/*.md file. The name is the file stem
// (there is no `name:` key, §4); files without frontmatter (README.md) are
// skipped. No version history in Stage 1 — content_hash lives on the row.
func (s *Scanner) scanCommandFile(f mdFile, st *Stats, counter *int, seen map[int64]bool, warn warnFn) {
	content, err := os.ReadFile(f.path)
	if err != nil {
		warn("command %s: %v", f.path, err)
		return
	}
	if !isFrontmatterStart(content) {
		return // README.md inside commands/ would otherwise register as /README (§4)
	}
	hash := sha256Hex(content)

	if id, ok := s.unchangedRow(
		`SELECT id FROM commands WHERE file_path = ? AND deleted = 0 AND content_hash = ?`,
		f.path, hash); ok {
		seen[id] = true
		*counter++
		return
	}

	fm, perr := parseFrontmatter(content)
	name := stem(f.path)
	if f.src.origin == "plugin" {
		name = f.src.plugin + ":" + name
	}

	id, changed, err := s.upsertCommandRow(f, name, nullStr(strField(fm, "description")), hash)
	if err != nil {
		warn("command %s: upsert: %v", f.path, err)
		return
	}
	seen[id] = true
	*counter++

	target := fmt.Sprintf("command:%d", id)
	if perr != nil {
		st.ParseErrors++
		warn("command %s: %v — kept with parse_error finding", f.path, perr)
		if ferr := s.upsertFinding(target, "parse_error", "error", f.path+": "+perr.Error()); ferr != nil {
			warn("command %s: finding: %v", f.path, ferr)
		}
	} else if ferr := s.resolveFinding(target, "parse_error"); ferr != nil {
		warn("command %s: finding resolve: %v", f.path, ferr)
	}

	if changed {
		s.publish(KindCommand, id)
	}
}

func (s *Scanner) upsertCommandRow(f mdFile, name string, description any, hash string) (id int64, changed bool, err error) {
	var deleted int
	var curHash string
	err = s.db.QueryRow(
		`SELECT id, deleted, content_hash FROM commands WHERE name = ? AND scope = ? AND project_id IS ?`,
		name, f.src.scope, f.src.projectID).Scan(&id, &deleted, &curHash)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		res, ierr := s.db.Exec(
			`INSERT INTO commands (name, scope, project_id, file_path, description, origin, plugin_name, content_hash, deleted)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0)`,
			name, f.src.scope, f.src.projectID, f.path, description,
			f.src.origin, nullStr(f.src.plugin), hash)
		if ierr != nil {
			return 0, false, ierr
		}
		id, err = res.LastInsertId()
		return id, true, err
	case err != nil:
		return 0, false, err
	}
	_, err = s.db.Exec(
		`UPDATE commands SET file_path = ?, description = ?, origin = ?, plugin_name = ?,
		        content_hash = ?, deleted = 0 WHERE id = ?`,
		f.path, description, f.src.origin, nullStr(f.src.plugin), hash, id)
	return id, deleted == 1 || curHash != hash, err
}

// ── shared helpers ───────────────────────────────────────────────────────────

// unchangedRow runs the two-arg hash-comparator query; ok means the item is
// already indexed at this exact content and the parse can be skipped.
func (s *Scanner) unchangedRow(query string, path, hash string) (int64, bool) {
	var id int64
	err := s.db.QueryRow(query, path, hash).Scan(&id)
	if err != nil {
		return 0, false
	}
	return id, true
}

// saveVersion records content into <table> under UNIQUE(item, content_hash)
// and repoints current_version_id. Dedup contract: a resave with unchanged
// content creates NO version row (the caller pre-checks via unchangedRow;
// this function re-checks against current_version_id so renames and path
// moves cannot slip a duplicate through). The stored content is the full
// file — it is the rollback source (design §2).
func (s *Scanner) saveVersion(table, fkCol, itemTable string, itemID int64, hash string, content []byte) (bool, error) {
	var curHash sql.NullString
	err := s.db.QueryRow(fmt.Sprintf(
		`SELECT v.content_hash FROM %s i LEFT JOIN %s v ON v.id = i.current_version_id WHERE i.id = ?`,
		itemTable, table), itemID).Scan(&curHash)
	if err != nil {
		return false, err
	}
	if curHash.Valid && curHash.String == hash {
		return false, nil // no-op resave — no new version
	}
	if _, err := s.db.Exec(fmt.Sprintf(
		`INSERT INTO %s (%s, content_hash, content, created_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(%s, content_hash) DO NOTHING`, table, fkCol, fkCol),
		itemID, hash, string(content), nowRFC3339()); err != nil {
		return false, err
	}
	var versionID int64
	if err := s.db.QueryRow(fmt.Sprintf(
		`SELECT id FROM %s WHERE %s = ? AND content_hash = ?`, table, fkCol),
		itemID, hash).Scan(&versionID); err != nil {
		return false, err
	}
	_, err = s.db.Exec(fmt.Sprintf(
		`UPDATE %s SET current_version_id = ? WHERE id = ?`, itemTable), versionID, itemID)
	return true, err
}

// upsertFinding records one unresolved lint finding per (target, rule) —
// rescans refresh the message instead of piling up duplicate rows.
func (s *Scanner) upsertFinding(target, rule, severity, message string) error {
	var id int64
	err := s.db.QueryRow(
		`SELECT id FROM config_lint_findings WHERE target = ? AND rule = ? AND resolved_at IS NULL`,
		target, rule).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = s.db.Exec(
			`INSERT INTO config_lint_findings (target, rule, severity, message, detected_at)
			 VALUES (?, ?, ?, ?, ?)`, target, rule, severity, message, nowRFC3339())
		return err
	}
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE config_lint_findings SET severity = ?, message = ? WHERE id = ?`,
		severity, message, id)
	return err
}

// resolveFinding closes the open finding for (target, rule), if any. It only
// ever touches the scanner's own rule rows (step-04's linter owns the rest).
func (s *Scanner) resolveFinding(target, rule string) error {
	_, err := s.db.Exec(
		`UPDATE config_lint_findings SET resolved_at = ? WHERE target = ? AND rule = ? AND resolved_at IS NULL`,
		nowRFC3339(), target, rule)
	return err
}

// sweepDeleted soft-deletes rows whose backing file vanished: deleted=1, the
// row (and its versions) stay — physical deletion never happens. Guarded to
// paths under a scanned root so items of unscanned tiers are left alone.
func (s *Scanner) sweepDeleted(kind, table, pathCol string, seen map[int64]bool, roots []string, st *Stats, warn warnFn) {
	rows, err := s.db.Query(fmt.Sprintf(`SELECT id, %s FROM %s WHERE deleted = 0`, pathCol, table))
	if err != nil {
		warn("sweep %s: %v", table, err)
		return
	}
	type victim struct {
		id   int64
		path string
	}
	var victims []victim
	for rows.Next() {
		var v victim
		if err := rows.Scan(&v.id, &v.path); err != nil {
			warn("sweep %s: %v", table, err)
			rows.Close()
			return
		}
		if !seen[v.id] && underAny(v.path, roots) {
			victims = append(victims, v)
		}
	}
	rows.Close()
	for _, v := range victims {
		if _, err := s.db.Exec(fmt.Sprintf(`UPDATE %s SET deleted = 1 WHERE id = ?`, table), v.id); err != nil {
			warn("sweep %s #%d: %v", table, v.id, err)
			continue
		}
		st.Deleted++
		s.publish(kind, v.id)
	}
}

// underAny reports whether path lies under (or is) one of the roots.
func underAny(path string, roots []string) bool {
	for _, r := range roots {
		if path == r || strings.HasPrefix(path, r+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// nullStr maps "" to NULL.
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ── hooks (settings.json / settings.local.json, §3) ─────────────────────────

// hookEntry is one parsed hook command entry, ready for its hooks table row.
type hookEntry struct {
	event         string
	matcher       sql.NullString // NULL when absent in JSON; "" is a real observed value
	command       string
	timeout       sql.NullInt64
	statusMessage sql.NullString
	seq           int
	managed       sql.NullString // 'swarmery' on the "swarmery hook" marker (hookcfg)
	hash          string
}

// hookcfgMarker recognizes swarmery-installed entries — the exact marker
// string internal/hookcfg uses.
const hookcfgMarker = "swarmery hook"

// parseSettingsHooks extracts hook entries from one settings file. The walk
// is hookcfg-style defensive map traversal: odd shapes degrade to skipped
// entries, only invalid JSON is an error. Unknown event names are data, not
// errors (§3.5). seq is the entry's position within its event's flattened
// group arrays at scan time.
func parseSettingsHooks(raw []byte, warn warnFn, path string) ([]hookEntry, error) {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	hooks, _ := root["hooks"].(map[string]any)
	events := make([]string, 0, len(hooks))
	for ev := range hooks {
		events = append(events, ev)
	}
	sort.Strings(events)

	var out []hookEntry
	for _, ev := range events {
		seq := 0
		for _, g := range sliceOf(hooks[ev]) {
			group, ok := g.(map[string]any)
			if !ok {
				continue
			}
			var matcher sql.NullString
			if mv, ok := group["matcher"].(string); ok {
				matcher = sql.NullString{String: mv, Valid: true}
			}
			for _, h := range sliceOf(group["hooks"]) {
				entry, ok := h.(map[string]any)
				if !ok {
					continue
				}
				cmd, _ := entry["command"].(string)
				if cmd == "" {
					warn("hooks %s: %s entry without a command — skipped", path, ev)
					continue
				}
				e := hookEntry{event: ev, matcher: matcher, command: cmd, seq: seq}
				if tv, ok := entry["timeout"].(float64); ok {
					e.timeout = sql.NullInt64{Int64: int64(tv), Valid: true}
				}
				if sm, ok := entry["statusMessage"].(string); ok {
					e.statusMessage = sql.NullString{String: sm, Valid: true}
				}
				if strings.Contains(cmd, hookcfgMarker) {
					e.managed = sql.NullString{String: "swarmery", Valid: true}
				}
				e.hash = hookHash(e)
				out = append(out, e)
				seq++
			}
		}
	}
	return out, nil
}

// hookHash renders one entry's identity hash (row-level content_hash).
func hookHash(e hookEntry) string {
	matcher := "\x00absent"
	if e.matcher.Valid {
		matcher = e.matcher.String
	}
	timeout := "\x00absent"
	if e.timeout.Valid {
		timeout = fmt.Sprint(e.timeout.Int64)
	}
	status := "\x00absent"
	if e.statusMessage.Valid {
		status = e.statusMessage.String
	}
	return sha256Hex([]byte(strings.Join(
		[]string{e.event, fmt.Sprint(e.seq), matcher, e.command, timeout, status}, "\n")))
}

func sliceOf(v any) []any {
	s, _ := v.([]any)
	return s
}

// scanHooksFile syncs the hooks rows of one settings source file. Strategy
// (step-03 design): per source_file delete-and-insert in ONE tx, guarded by
// an ordered content-hash comparison so unchanged files touch nothing (and
// row ids stay stable across rescans). Unparseable JSON keeps existing rows.
func (s *Scanner) scanHooksFile(f settingsFile, st *Stats, warn warnFn) {
	raw, err := os.ReadFile(f.path)
	if os.IsNotExist(err) {
		s.deleteHooksRows(f.path, st, warn) // file gone — drop its rows
		return
	}
	if err != nil {
		warn("hooks %s: %v", f.path, err)
		return
	}

	target := "hooks:" + f.path
	entries, perr := parseSettingsHooks(raw, warn, f.path)
	if perr != nil {
		st.ParseErrors++
		warn("hooks %s: %v — keeping previously scanned rows", f.path, perr)
		if ferr := s.upsertFinding(target, "parse_error", "error", f.path+": "+perr.Error()); ferr != nil {
			warn("hooks %s: finding: %v", f.path, ferr)
		}
		return
	}
	if ferr := s.resolveFinding(target, "parse_error"); ferr != nil {
		warn("hooks %s: finding resolve: %v", f.path, ferr)
	}

	st.Hooks += len(entries)
	for _, e := range entries {
		if e.managed.Valid {
			st.HooksManaged++
		}
	}

	// Ordered hash comparison — matches the (event, seq) insert order below.
	var existing []string
	rows, err := s.db.Query(`SELECT content_hash FROM hooks WHERE source_file = ? ORDER BY event, seq`, f.path)
	if err != nil {
		warn("hooks %s: %v", f.path, err)
		return
	}
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			warn("hooks %s: %v", f.path, err)
			rows.Close()
			return
		}
		existing = append(existing, h)
	}
	rows.Close()
	if hashesEqual(existing, entries) {
		return // unchanged — no delete-insert, ids stay stable
	}

	tx, err := s.db.Begin()
	if err != nil {
		warn("hooks %s: %v", f.path, err)
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM hooks WHERE source_file = ?`, f.path); err != nil {
		warn("hooks %s: %v", f.path, err)
		return
	}
	var newIDs []int64
	for _, e := range entries {
		res, err := tx.Exec(
			`INSERT INTO hooks (scope, project_id, event, matcher, command, timeout, status_message,
			                    source_file, seq, enabled, managed, content_hash)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
			f.src.scope, f.src.projectID, e.event, e.matcher, e.command, e.timeout, e.statusMessage,
			f.path, e.seq, e.managed, e.hash)
		if err != nil {
			warn("hooks %s: insert: %v", f.path, err)
			return
		}
		id, err := res.LastInsertId()
		if err != nil {
			warn("hooks %s: %v", f.path, err)
			return
		}
		newIDs = append(newIDs, id)
	}
	if err := tx.Commit(); err != nil {
		warn("hooks %s: commit: %v", f.path, err)
		return
	}
	for _, id := range newIDs {
		s.publish(KindHook, id)
	}
}

func hashesEqual(existing []string, entries []hookEntry) bool {
	if len(existing) != len(entries) {
		return false
	}
	for i, e := range entries {
		if existing[i] != e.hash {
			return false
		}
	}
	return true
}

// deleteHooksRows removes every row of one vanished source file.
func (s *Scanner) deleteHooksRows(sourceFile string, st *Stats, warn warnFn) {
	rows, err := s.db.Query(`SELECT id FROM hooks WHERE source_file = ?`, sourceFile)
	if err != nil {
		warn("hooks %s: %v", sourceFile, err)
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			warn("hooks %s: %v", sourceFile, err)
			rows.Close()
			return
		}
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) == 0 {
		return
	}
	if _, err := s.db.Exec(`DELETE FROM hooks WHERE source_file = ?`, sourceFile); err != nil {
		warn("hooks %s: delete: %v", sourceFile, err)
		return
	}
	st.Deleted += len(ids)
	for _, id := range ids {
		s.publish(KindHook, id)
	}
}

// sweepStaleHookFiles drops rows of source files that are no longer scan
// candidates (an archived project, a changed ClaudeDir).
func (s *Scanner) sweepStaleHookFiles(candidates map[string]bool, st *Stats, warn warnFn) {
	rows, err := s.db.Query(`SELECT DISTINCT source_file FROM hooks`)
	if err != nil {
		warn("hooks sweep: %v", err)
		return
	}
	var stale []string
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			warn("hooks sweep: %v", err)
			rows.Close()
			return
		}
		if !candidates[f] {
			stale = append(stale, f)
		}
	}
	rows.Close()
	for _, f := range stale {
		s.deleteHooksRows(f, st, warn)
	}
}
