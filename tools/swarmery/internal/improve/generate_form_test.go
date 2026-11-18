package improve

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
)

type formRow struct {
	status, outcome, lens, form, followups, warnings, rationale, diff string
	errCol, decidedAt                                                 sql.NullString
}

func readFormRow(t *testing.T, db *sql.DB, id int64) formRow {
	t.Helper()
	var r formRow
	if err := db.QueryRow(`
		SELECT status, outcome, lens, form, suggested_followups_json, warnings_json,
		       rationale, diff, error, decided_at
		  FROM agent_change_proposals WHERE id = ?`, id).
		Scan(&r.status, &r.outcome, &r.lens, &r.form, &r.followups, &r.warnings,
			&r.rationale, &r.diff, &r.errCol, &r.decidedAt); err != nil {
		t.Fatalf("read proposal %d: %v", id, err)
	}
	return r
}

// A well-formed answer stores form, the proposal lens, the follow-ups and the
// generate-time always-on growth warning (validOut adds one line to an agent
// definition and removes none).
func TestGenerateStoresFormLensFollowupsWarnings(t *testing.T) {
	db := openDB(t)
	seedAgent(t, db, 1, "tech-lead", "local", "/x/tech-lead.md", "agent body")
	out := validOut +
		"## Follow-ups\n- hook: refuse done while the last command failed\n"
	svc := newService(t, db, &mockRunner{out: out})

	id, err := svc.Generate(context.Background(), GenerateReq{Agent: "tech-lead"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	r := readFormRow(t, db, id)
	if r.status != "proposed" || r.outcome != "" || r.form != FormAlwaysOn || r.lens != LensProcedure {
		t.Errorf("row = %+v", r)
	}
	var fs []Followup
	if err := json.Unmarshal([]byte(r.followups), &fs); err != nil || len(fs) != 1 || fs[0].Form != FormHook {
		t.Errorf("followups = %s (%v)", r.followups, err)
	}
	var ws []string
	if err := json.Unmarshal([]byte(r.warnings), &ws); err != nil || len(ws) != 1 ||
		!strings.Contains(ws[0], "always-on growth: "+genAgentRel+" gains 1 lines net") {
		t.Errorf("warnings = %s (%v)", r.warnings, err)
	}
	if r.decidedAt.Valid {
		t.Errorf("a proposed row must stay undecided, decided_at = %q", r.decidedAt.String)
	}
}

// D8: a strict-parse failure (a hunk without a lens) lands as 'failed' with the
// reason, and the row is retriable into a proper proposal.
func TestGenerateStrictParseFailureIsRetriable(t *testing.T) {
	db := openDB(t)
	seedAgent(t, db, 1, "tech-lead", "local", "/x/tech-lead.md", "agent body")
	runner := &mockRunner{out: fixture(t, "missing_lens.txt")}
	svc := newService(t, db, runner)

	id, err := svc.Generate(context.Background(), GenerateReq{Agent: "tech-lead"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	r := readFormRow(t, db, id)
	if r.status != "failed" || !strings.Contains(r.errCol.String, "output contract: hunk 2 has no lens") {
		t.Fatalf("row = %+v, want failed with the lens error", r)
	}

	for name, bad := range map[string]string{
		"missing form":         fixture(t, "missing_form.txt"),
		"keep base, no reason": fixture(t, "keep_base_no_reason.txt"),
	} {
		runner.out = bad
		if err := svc.Retry(context.Background(), id); err != nil {
			t.Fatalf("%s: Retry: %v", name, err)
		}
		if r := readFormRow(t, db, id); r.status != "failed" || !strings.Contains(r.errCol.String, "output contract") {
			t.Errorf("%s: row = %+v, want still failed", name, r)
		}
	}

	runner.out = validOut
	if err := svc.Retry(context.Background(), id); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	r = readFormRow(t, db, id)
	if r.status != "proposed" || r.lens != LensProcedure || r.form != FormAlwaysOn || r.errCol.Valid {
		t.Errorf("retried row = %+v, want proposed with lens + form", r)
	}
}

// KEEP_BASE is terminal: status 'rejected' + outcome 'kept_base', the reason in
// rationale, no diff — and it does NOT hold the one-open slot, so the next
// Generate for the same agent succeeds.
func TestGenerateKeepBaseFreesOpenSlot(t *testing.T) {
	db := openDB(t)
	seedAgent(t, db, 1, "tech-lead", "local", "/x/tech-lead.md", "agent body")
	runner := &mockRunner{out: fixture(t, "keep_base_reason.txt")}
	svc := newService(t, db, runner)

	id, err := svc.Generate(context.Background(), GenerateReq{Agent: "tech-lead"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	r := readFormRow(t, db, id)
	if r.status != "rejected" || r.outcome != OutcomeKeptBase || r.form != FormKeepBase ||
		r.diff != "" || r.lens != "" || r.errCol.Valid || !r.decidedAt.Valid {
		t.Errorf("keep-base row = %+v", r)
	}
	if !strings.Contains(r.rationale, "line 12 already requires") {
		t.Errorf("reason not stored: %q", r.rationale)
	}
	if r.warnings != "[]" || !strings.Contains(r.followups, `"form":"script"`) {
		t.Errorf("warnings/followups = %s / %s", r.warnings, r.followups)
	}
	// Not an operator correction: only operator endpoints write that ledger.
	var nCorr int
	if err := db.QueryRow(`SELECT COUNT(*) FROM operator_corrections`).Scan(&nCorr); err != nil || nCorr != 0 {
		t.Errorf("operator_corrections = %d (%v), want 0 after a KEEP_BASE answer", nCorr, err)
	}
	if open, err := svc.OpenProposalID("tech-lead"); err != nil || open != 0 {
		t.Fatalf("OpenProposalID = %d, %v; a kept_base row must not hold the slot", open, err)
	}

	runner.out = validOut
	id2, err := svc.Generate(context.Background(), GenerateReq{Agent: "tech-lead"})
	if err != nil {
		t.Fatalf("Generate after KEEP_BASE: %v", err)
	}
	if r := readFormRow(t, db, id2); r.status != "proposed" {
		t.Errorf("second proposal status = %q, want proposed", r.status)
	}
	// A kept_base row is not retriable (only 'failed' is) — it is an answer.
	if err := svc.Retry(context.Background(), id); err == nil {
		t.Error("Retry accepted a kept_base row")
	}
}

// A failed row retried into a KEEP_BASE answer lands terminal too.
func TestRetryKeepBaseIsTerminal(t *testing.T) {
	db := openDB(t)
	seedAgent(t, db, 1, "tech-lead", "local", "/x/tech-lead.md", "agent body")
	runner := &mockRunner{out: "garbage"}
	svc := newService(t, db, runner)
	id, err := svc.Generate(context.Background(), GenerateReq{Agent: "tech-lead"})
	if err != nil {
		t.Fatal(err)
	}
	runner.out = fixture(t, "keep_base_reason.txt")
	if err := svc.Retry(context.Background(), id); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if r := readFormRow(t, db, id); r.status != "rejected" || r.outcome != OutcomeKeptBase || !r.decidedAt.Valid {
		t.Errorf("retried keep-base row = %+v", r)
	}
}
