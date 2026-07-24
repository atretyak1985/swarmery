package approvals

import (
	"database/sql"
	"sort"
	"testing"
)

// seedProject inserts a bare project and returns its id (presets tests need a
// FK target without a session).
func seedProject(t *testing.T, db *sql.DB, path string) int64 {
	t.Helper()
	res, err := db.Exec(
		`INSERT INTO projects (path, slug, name, first_seen) VALUES (?, ?, ?, '2026-07-24T00:00:00.000Z')`,
		path, path, path)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

// presetRules returns the tool_patterns of a project's managed (source='preset')
// rules, in id order (= compile/insert order).
func presetRules(t *testing.T, db *sql.DB, projectID int64) []string {
	t.Helper()
	rows, err := db.Query(
		`SELECT tool_pattern FROM approval_rules WHERE project_id = ? AND source = 'preset' ORDER BY id`,
		projectID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

// TestCompileApprovalRequiredFailsClosed: approval-required compiles ZERO
// managed rules (the fail-closed baseline) and stores the preset.
func TestCompileApprovalRequiredFailsClosed(t *testing.T) {
	db := testDB(t)
	pid := seedProject(t, db, "p1")

	n, err := Compile(db, pid, PresetApprovalRequired, nil)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if n != 0 {
		t.Fatalf("approval-required compiled %d rules, want 0", n)
	}
	if got := presetRules(t, db, pid); len(got) != 0 {
		t.Fatalf("approval-required left rules %v, want none", got)
	}
	preset, _, err := GetPreset(db, pid)
	if err != nil || preset != PresetApprovalRequired {
		t.Fatalf("GetPreset = %q, %v", preset, err)
	}
}

// TestCompileLockedDownFailsClosed: locked-down also compiles ZERO rules AND
// IsLockedDown reports true.
func TestCompileLockedDownFailsClosed(t *testing.T) {
	db := testDB(t)
	pid := seedProject(t, db, "p1")

	n, err := Compile(db, pid, PresetLockedDown, nil)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if n != 0 {
		t.Fatalf("locked-down compiled %d rules, want 0", n)
	}
	locked, err := IsLockedDown(db, pid)
	if err != nil || !locked {
		t.Fatalf("IsLockedDown = %v, %v; want true", locked, err)
	}
}

// TestCompileUnrestrictedDefault: unrestricted compiles every category EXCEPT
// git_push, with command_exec's broad Bash(*) LAST.
func TestCompileUnrestrictedDefault(t *testing.T) {
	db := testDB(t)
	pid := seedProject(t, db, "p1")

	n, err := Compile(db, pid, PresetUnrestricted, nil)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	got := presetRules(t, db, pid)
	if n != len(got) {
		t.Fatalf("compile returned %d but %d rows present", n, len(got))
	}

	// git_push patterns must be ABSENT under the default.
	for _, p := range got {
		if p == "Bash(git push*)" || p == "Bash(gh *)" {
			t.Fatalf("default unrestricted auto-approved git_push pattern %q", p)
		}
	}
	// Present categories: read_only, file_write, git_write, network, command_exec.
	want := []string{
		"Read", "Grep", "Glob", "Bash(git status*)", "Bash(git log*)", "Bash(git diff*)",
		"Edit", "Write", "NotebookEdit",
		"Bash(git add*)", "Bash(git commit*)", "Bash(git checkout*)", "Bash(git worktree*)",
		"WebFetch", "WebSearch",
		"Bash(*)",
	}
	if len(got) != len(want) {
		t.Fatalf("compiled %v\nwant     %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("compiled[%d] = %q, want %q (order matters: command_exec LAST)", i, got[i], want[i])
		}
	}
	// The broadest rule is dead last.
	if got[len(got)-1] != "Bash(*)" {
		t.Fatalf("last compiled rule = %q, want Bash(*)", got[len(got)-1])
	}
}

// TestCompileOverrideMatrix: overrides flip categories on/off under unrestricted.
func TestCompileOverrideMatrix(t *testing.T) {
	db := testDB(t)
	pid := seedProject(t, db, "p1")

	// Turn git_push ON (explicit) and command_exec OFF.
	n, err := Compile(db, pid, PresetUnrestricted, map[string]string{
		"git_push":     PolicyAllow,
		"command_exec": PolicyAsk,
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	got := presetRules(t, db, pid)
	if n != len(got) {
		t.Fatalf("count mismatch %d vs %d", n, len(got))
	}
	has := func(p string) bool {
		for _, x := range got {
			if x == p {
				return true
			}
		}
		return false
	}
	if !has("Bash(git push*)") || !has("Bash(gh *)") {
		t.Errorf("git_push override to allow did not compile its patterns: %v", got)
	}
	if has("Bash(*)") {
		t.Errorf("command_exec override to ask still compiled Bash(*): %v", got)
	}

	// Override under approval-required is inert (still zero rules).
	n2, err := Compile(db, pid, PresetApprovalRequired, map[string]string{"command_exec": PolicyAllow})
	if err != nil {
		t.Fatalf("compile2: %v", err)
	}
	if n2 != 0 {
		t.Errorf("approval-required with allow-override compiled %d rules, want 0 (override inert)", n2)
	}
}

// TestCompilePreservesManualRules: Compile only ever replaces source='preset'
// rows; a hand-written manual rule survives every recompile.
func TestCompilePreservesManualRules(t *testing.T) {
	db := testDB(t)
	pid := seedProject(t, db, "p1")

	// A manual rule (default source='manual') for THIS project + a global one.
	manualID := seedRule(t, db, pid, "Bash(terraform plan*)")
	globalID := seedRule(t, db, nil, "Read")

	// Compile unrestricted, then locked-down, then unrestricted again.
	for _, p := range []string{PresetUnrestricted, PresetLockedDown, PresetUnrestricted} {
		if _, err := Compile(db, pid, p, nil); err != nil {
			t.Fatalf("compile %s: %v", p, err)
		}
	}

	// Both manual rules still exist with source='manual'.
	for _, id := range []int64{manualID, globalID} {
		var pattern, source string
		if err := db.QueryRow(
			`SELECT tool_pattern, source FROM approval_rules WHERE id = ?`, id).Scan(&pattern, &source); err != nil {
			t.Fatalf("manual rule %d gone: %v", id, err)
		}
		if source != "manual" {
			t.Errorf("rule %d source = %q, want manual", id, source)
		}
	}
	// The manual project rule is NOT among the compiled preset set.
	for _, p := range presetRules(t, db, pid) {
		if p == "Bash(terraform plan*)" {
			t.Fatal("manual rule leaked into the preset rule set")
		}
	}
}

// TestCompileRecompileReplaces: a second Compile fully replaces the prior
// preset rule set (no accumulation).
func TestCompileRecompileReplaces(t *testing.T) {
	db := testDB(t)
	pid := seedProject(t, db, "p1")

	if _, err := Compile(db, pid, PresetUnrestricted, nil); err != nil {
		t.Fatal(err)
	}
	first := len(presetRules(t, db, pid))
	if first == 0 {
		t.Fatal("expected unrestricted rules")
	}
	// Recompile to approval-required → the preset rules vanish entirely.
	if _, err := Compile(db, pid, PresetApprovalRequired, nil); err != nil {
		t.Fatal(err)
	}
	if got := presetRules(t, db, pid); len(got) != 0 {
		t.Fatalf("recompile to approval-required left %v, want none", got)
	}
}

// TestEscalations: unrestricted and command_exec/git_push→allow are privileged;
// safe transitions escalate nothing.
func TestEscalations(t *testing.T) {
	cases := []struct {
		name      string
		preset    string
		overrides map[string]string
		wantCount int
	}{
		{"approval-required is safe", PresetApprovalRequired, nil, 0},
		{"locked-down is safe", PresetLockedDown, nil, 0},
		{"unrestricted escalates (preset + default command_exec allow)", PresetUnrestricted, nil, 2},
		{"unrestricted with command_exec ask → only preset escalation", PresetUnrestricted,
			map[string]string{"command_exec": PolicyAsk}, 1},
		{"unrestricted + git_push allow → preset + command_exec + git_push", PresetUnrestricted,
			map[string]string{"git_push": PolicyAllow}, 3},
		// An override to allow command_exec is inert under approval-required (no
		// managed rules compile), so it must NOT escalate.
		{"approval-required + command_exec allow is inert", PresetApprovalRequired,
			map[string]string{"command_exec": PolicyAllow}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Escalations(c.preset, c.overrides)
			if len(got) != c.wantCount {
				t.Fatalf("Escalations = %v (%d), want %d", got, len(got), c.wantCount)
			}
		})
	}
}

// TestEffectivePolicy: the display model resolves each category correctly and
// defaults fail closed for an unset project.
func TestEffectivePolicy(t *testing.T) {
	db := testDB(t)
	pid := seedProject(t, db, "p1")

	// Unset project → default (approval-required), everything ask, not locked.
	view, err := EffectivePolicy(db, pid)
	if err != nil {
		t.Fatal(err)
	}
	if view.Preset != PresetApprovalRequired || view.LockedDown {
		t.Fatalf("unset view = %+v, want approval-required / not locked", view)
	}
	for _, c := range view.Categories {
		if c.Policy != PolicyAsk {
			t.Errorf("unset category %s policy = %q, want ask (fail closed)", c.Category, c.Policy)
		}
	}
	if len(view.Categories) != len(categoryOrder) {
		t.Fatalf("categories = %d, want %d", len(view.Categories), len(categoryOrder))
	}

	// Unrestricted: everything allow except git_push.
	if _, err := Compile(db, pid, PresetUnrestricted, nil); err != nil {
		t.Fatal(err)
	}
	view, err = EffectivePolicy(db, pid)
	if err != nil {
		t.Fatal(err)
	}
	policyOf := map[string]string{}
	for _, c := range view.Categories {
		policyOf[c.Category] = c.Policy
	}
	if policyOf["git_push"] != PolicyAsk {
		t.Errorf("git_push policy = %q, want ask under default unrestricted", policyOf["git_push"])
	}
	if policyOf["command_exec"] != PolicyAllow {
		t.Errorf("command_exec policy = %q, want allow", policyOf["command_exec"])
	}
}

// TestGetPresetCorruptOverridesFailsClosed: a corrupt overrides blob degrades
// to no overrides (never fail open).
func TestGetPresetCorruptOverridesFailsClosed(t *testing.T) {
	db := testDB(t)
	pid := seedProject(t, db, "p1")
	if _, err := db.Exec(
		`INSERT INTO project_permission_presets (project_id, preset, overrides, updated_at)
		 VALUES (?, 'unrestricted', 'not-json{', '2026-07-24T00:00:00.000Z')`, pid); err != nil {
		t.Fatal(err)
	}
	_, overrides, err := GetPreset(db, pid)
	if err != nil {
		t.Fatalf("GetPreset should not error on corrupt overrides: %v", err)
	}
	if len(overrides) != 0 {
		t.Fatalf("corrupt overrides = %v, want empty (fail closed)", overrides)
	}
}

// TestValidateOverrides rejects unknown categories and bad policies.
func TestValidateOverrides(t *testing.T) {
	if msg := ValidateOverrides(map[string]string{"command_exec": PolicyAllow, "network": PolicyAsk}); msg != "" {
		t.Errorf("valid overrides rejected: %s", msg)
	}
	if ValidateOverrides(map[string]string{"bogus_cat": PolicyAllow}) == "" {
		t.Error("unknown category accepted")
	}
	if ValidateOverrides(map[string]string{"command_exec": "block"}) == "" {
		t.Error("policy 'block' accepted (must be allow|ask only)")
	}
}

// TestCompileRejectsUnknownPreset: a bad preset is a hard error (fail closed).
func TestCompileRejectsUnknownPreset(t *testing.T) {
	db := testDB(t)
	pid := seedProject(t, db, "p1")
	if _, err := Compile(db, pid, "wide-open", nil); err == nil {
		t.Fatal("Compile accepted unknown preset 'wide-open'")
	}
}

// TestCategoryPatternsAllParse: every pattern in the single-source-of-truth map
// must parse under the rules matcher (a typo would compile dead rules).
func TestCategoryPatternsAllParse(t *testing.T) {
	seen := map[string]bool{}
	for _, cat := range categoryOrder {
		pats, ok := categoryPatterns[cat]
		if !ok {
			t.Fatalf("categoryOrder has %q with no patterns", cat)
		}
		for _, p := range pats {
			if _, err := ParseRulePattern(p); err != nil {
				t.Errorf("category %s pattern %q does not parse: %v", cat, p, err)
			}
			seen[cat] = true
		}
	}
	// categoryOrder and categoryPatterns must cover the same categories.
	var mapCats []string
	for c := range categoryPatterns {
		mapCats = append(mapCats, c)
	}
	sort.Strings(mapCats)
	orderCats := append([]string(nil), categoryOrder...)
	sort.Strings(orderCats)
	if len(mapCats) != len(orderCats) {
		t.Fatalf("categoryPatterns keys %v != categoryOrder %v", mapCats, orderCats)
	}
	for i := range mapCats {
		if mapCats[i] != orderCats[i] {
			t.Fatalf("category set mismatch: %v vs %v", mapCats, orderCats)
		}
	}
}
