package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// lowQuotaBody is the 429 wire shape of a quota refusal.
type lowQuotaBody struct {
	Error       string  `json:"error"`
	Code        string  `json:"code"`
	Message     string  `json:"message"`
	Account     string  `json:"account"`
	Window      string  `json:"window"`
	PercentLeft float64 `json:"percentLeft"`
	Floor       float64 `json:"floor"`
	ResetsAt    string  `json:"resetsAt"`
}

func decodeLowQuota(t *testing.T, resp *http.Response) lowQuotaBody {
	t.Helper()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
	var b lowQuotaBody
	if err := json.NewDecoder(resp.Body).Decode(&b); err != nil {
		t.Fatal(err)
	}
	if b.Error != codeLowQuota || b.Code != codeLowQuota {
		t.Errorf("error/code = %q/%q, want %q", b.Error, b.Code, codeLowQuota)
	}
	return b
}

// End to end through the REAL gate: the unbound fixture project runs on the
// default account, whose fresh weekly reading is under the 10% floor. The run is
// refused with 429 naming the account, window, headroom, floor and reset — and
// nothing is stamped on the phase.
func TestPhaseRun_LowQuota_429(t *testing.T) {
	t.Setenv(runcore.QuotaFloorEnv, "")
	t.Setenv(runcore.QuotaIntervalEnv, "")
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	svc := attachPhaseRun(t, db, &phaseStubRunner{}, true)
	if err := store.PutAccountQuota(db, "default", []store.QuotaRow{
		{WindowKey: "five_hour", Label: "Session (5h)", PercentLeft: 70, ResetsAt: "2027-01-01T00:00:00Z"},
		{WindowKey: "seven_day", Label: "Weekly", PercentLeft: 6, ResetsAt: "2027-01-05T00:00:00Z"},
	}, time.Now()); err != nil {
		t.Fatal(err)
	}

	b := decodeLowQuota(t, postPhase(t, phaseRunURL(srv, taskID, p1)))
	if b.Account != "default" || b.Window != "seven_day" || b.PercentLeft != 6 || b.Floor != 10 {
		t.Errorf("body = %+v, want default/seven_day/6/10", b)
	}
	if b.ResetsAt != "2027-01-05T00:00:00Z" {
		t.Errorf("resetsAt = %q, want the tight window's reset", b.ResetsAt)
	}
	if b.Message == "" {
		t.Error("no human-readable message")
	}
	if svc.Slots.Count() != 0 {
		t.Errorf("slots held = %d, want 0", svc.Slots.Count())
	}
	var state string
	if err := db.QueryRow(`SELECT run_state FROM epic_phases WHERE id=?`, p1).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "idle" {
		t.Errorf("run_state = %q, want idle — low quota is not a failed phase", state)
	}
}

// The plan surface answers the same refusal with the same shape.
func TestRunPlan_LowQuota_429(t *testing.T) {
	srv, db, taskID := planFixtureWithReadme(t)
	svc := attachPlanRun(t, db, &planrunStubRunner{}, true)
	svc.QuotaCheck = func(*sql.DB, claudeacct.Resolution, time.Time) error {
		return &runcore.LowQuotaError{Account: "work", Window: "five_hour", Label: "Session (5h)",
			ResetsAt: "2027-01-01T00:00:00Z", PercentLeft: 3, Floor: 10}
	}

	b := decodeLowQuota(t, postPlanRun(t, planRunURL(srv, taskID), ""))
	if b.Account != "work" || b.Window != "five_hour" || b.PercentLeft != 3 || b.Floor != 10 ||
		b.ResetsAt != "2027-01-01T00:00:00Z" {
		t.Errorf("body = %+v", b)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM plan_runs WHERE workspace_task_id=?`, taskID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("plan_runs rows = %d, want 0", n)
	}
}
