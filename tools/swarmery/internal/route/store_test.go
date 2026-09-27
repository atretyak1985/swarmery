package route

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

func openStore(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "route.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestRecord_RoundTrip(t *testing.T) {
	db := openStore(t)
	sig := Signals{
		Surface: SurfaceDispatch, PromptBytes: 1800, FileScope: 7, Areas: 2,
		RiskPaths: []string{"db/migrations/0001.sql"}, Deps: 1,
		HistFailRate: 0.4, HistSamples: 6,
	}
	d := Decide(sig, DefaultPolicy())
	at := time.Date(2026, 9, 27, 21, 0, 0, 123e6, time.UTC)

	if err := Record(db, Row{
		Surface: SurfaceDispatch, Subject: SubjectTask(42), SessionUUID: "uuid-1",
		Mode: ModeShadow, Signals: sig, Decision: d,
		UsedModel: "claude-sonnet-5", UsedEffort: "medium", UsedPlaybook: "plan-first",
		WonRung: RungDefault, CreatedAt: at,
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	var (
		surface, subject, uuid, mode, sigJSON, tier  string
		pickModel, pickEffort, pickPlaybook, rsnJSON string
		usedModel, usedEffort, usedPlaybook, wonRung string
		createdAt                                    string
		score, applied                               int
		outcome, verify, outcomeAt                   sql.NullString
		cost                                         sql.NullFloat64
	)
	if err := db.QueryRow(`
		SELECT surface, subject, session_uuid, mode, signals_json, score, tier,
		       pick_model, pick_effort, pick_playbook, reasons_json, applied,
		       used_model, used_effort, used_playbook, won_rung,
		       outcome, verify_status, cost_usd, outcome_at, created_at
		  FROM route_decisions`).Scan(
		&surface, &subject, &uuid, &mode, &sigJSON, &score, &tier,
		&pickModel, &pickEffort, &pickPlaybook, &rsnJSON, &applied,
		&usedModel, &usedEffort, &usedPlaybook, &wonRung,
		&outcome, &verify, &cost, &outcomeAt, &createdAt); err != nil {
		t.Fatalf("read back: %v", err)
	}

	got := map[string]any{
		"surface": surface, "subject": subject, "uuid": uuid, "mode": mode,
		"score": score, "tier": tier, "pick_model": pickModel, "pick_effort": pickEffort,
		"pick_playbook": pickPlaybook, "applied": applied, "used_model": usedModel,
		"used_effort": usedEffort, "used_playbook": usedPlaybook, "won_rung": wonRung,
		"created_at": createdAt,
	}
	want := map[string]any{
		"surface": "dispatch", "subject": "task:42", "uuid": "uuid-1", "mode": "shadow",
		"score": d.Score, "tier": d.Tier, "pick_model": d.Model, "pick_effort": d.Effort,
		"pick_playbook": d.Playbook, "applied": 0, "used_model": "claude-sonnet-5",
		"used_effort": "medium", "used_playbook": "plan-first", "won_rung": "default",
		"created_at": "2026-09-27T21:00:00.123Z",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("row mismatch\n got  %v\n want %v", got, want)
	}
	if outcome.Valid || verify.Valid || cost.Valid || outcomeAt.Valid {
		t.Errorf("outcome columns must stay NULL until phase 3 fills them: %v %v %v %v", outcome, verify, cost, outcomeAt)
	}

	var doc signalsDoc
	if err := json.Unmarshal([]byte(sigJSON), &doc); err != nil {
		t.Fatalf("signals_json: %v", err)
	}
	wantDoc := signalsDoc{Surface: SurfaceDispatch, PromptBytes: 1800, FileScope: 7, Areas: 2,
		RiskPaths: []string{"db/migrations/0001.sql"}, Deps: 1, HistFailRate: 0.4, HistSamples: 6}
	if !reflect.DeepEqual(doc, wantDoc) {
		t.Errorf("signals_json = %+v, want %+v", doc, wantDoc)
	}
	var reasons []string
	if err := json.Unmarshal([]byte(rsnJSON), &reasons); err != nil {
		t.Fatalf("reasons_json: %v", err)
	}
	if !reflect.DeepEqual(reasons, d.Reasons) {
		t.Errorf("reasons = %v, want %v", reasons, d.Reasons)
	}
}

func TestRecord_EmptyListsStoreAsArrays(t *testing.T) {
	db := openStore(t)
	if err := Record(db, Row{
		Surface: SurfacePhaseRun, Subject: SubjectPhase(7), Mode: ModeActive, Applied: true,
		Signals: Signals{Surface: SurfacePhaseRun, FileScope: -1, Areas: -1},
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	var sigJSON, rsnJSON, subject, createdAt string
	var applied int
	if err := db.QueryRow(`SELECT signals_json, reasons_json, subject, applied, created_at FROM route_decisions`).
		Scan(&sigJSON, &rsnJSON, &subject, &applied, &createdAt); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(sigJSON), &doc); err != nil {
		t.Fatal(err)
	}
	if rp, ok := doc["risk_paths"].([]any); !ok || len(rp) != 0 {
		t.Errorf("risk_paths = %v, want []", doc["risk_paths"])
	}
	if rsnJSON != "[]" {
		t.Errorf("reasons_json = %q, want []", rsnJSON)
	}
	if subject != "phase:7" || applied != 1 {
		t.Errorf("subject=%q applied=%d", subject, applied)
	}
	if createdAt == "" {
		t.Error("created_at empty: a zero CreatedAt must default to now")
	}
}

func TestRecord_RejectsIncompleteRows(t *testing.T) {
	db := openStore(t)
	for name, r := range map[string]Row{
		"no surface": {Subject: "task:1", Mode: ModeShadow},
		"no subject": {Surface: SurfaceDispatch, Mode: ModeShadow},
		"mode off":   {Surface: SurfaceDispatch, Subject: "task:1", Mode: ModeOff},
		"no mode":    {Surface: SurfaceDispatch, Subject: "task:1"},
	} {
		if err := Record(db, r); !errors.Is(err, errRowIncomplete) {
			t.Errorf("%s: err = %v, want errRowIncomplete", name, err)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM route_decisions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d rows written for rejected input", n)
	}
}

func TestRecord_ReportsDBError(t *testing.T) {
	db := openStore(t)
	db.Close()
	err := Record(db, Row{Surface: SurfaceDispatch, Subject: "task:1", Mode: ModeShadow})
	if err == nil {
		t.Fatal("Record on a closed DB returned nil")
	}
}
