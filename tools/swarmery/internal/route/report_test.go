package route

import (
	"database/sql"
	"errors"
	"testing"
	"time"
)

func rrow(surface, tier, pick, used, outcome, verify string, cost *float64) reportRow {
	r := reportRow{surface: surface, tier: tier, mode: string(ModeShadow),
		pick: normModel(pick), used: normModel(used), outcome: outcome, verify: verify}
	if cost != nil {
		r.cost = sql.NullFloat64{Float64: *cost, Valid: true}
	}
	return r
}

func repeat(n int, r reportRow) []reportRow {
	out := make([]reportRow, n)
	for i := range out {
		out[i] = r
	}
	return out
}

func TestReport_NormModel(t *testing.T) {
	for in, want := range map[string]string{
		"haiku":               "haiku",
		"claude-haiku-4-5":    "haiku",
		"sonnet":              "sonnet",
		"claude-sonnet-5":     "sonnet",
		"claude-opus-5-5[1m]": "opus",
		"Custom-Model[1m]":    "custom-model",
		"":                    "",
	} {
		if got := normModel(in); got != want {
			t.Errorf("normModel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReport_FailureRateAgreeAndCost(t *testing.T) {
	var rows []reportRow
	// 20 tier-S dispatch rows: picked haiku, ran sonnet. 1 failed outcome, 1
	// blocked, 1 verify fail on a done run, 1 partial; 17 clean. Costs 0.1..2.0.
	for i := 0; i < 20; i++ {
		outcome, verify := OutcomeDone, "pass"
		switch i {
		case 0:
			outcome = OutcomeFailed
		case 1:
			outcome = OutcomeBlocked
		case 2:
			verify = "fail"
		case 3:
			outcome = "partial"
		case 4:
			verify = "inconclusive" // not a failure
		}
		c := float64(i+1) / 10
		rows = append(rows, rrow("dispatch", TierS, "haiku", "claude-sonnet-5", outcome, verify, &c))
	}
	// 20 tier-S rows where the pick ran (alias vs full id still agrees); two
	// have no cost — unknown, excluded from mean and p90 rather than read as 0.
	for i := 0; i < 20; i++ {
		var c *float64
		if i >= 2 {
			v := 0.05
			c = &v
		}
		rows = append(rows, rrow("dispatch", TierS, "haiku", "claude-haiku-4-5", OutcomeReview, "", c))
	}
	rep := buildReport(rows, 20)

	if len(rep.ByTier.Groups) != 1 {
		t.Fatalf("byTier = %+v", rep.ByTier)
	}
	tier := rep.ByTier.Groups[0]
	if tier.N != 40 || tier.Failures != 4 || tier.FailRate != 0.1 || tier.Agree != 0.5 {
		t.Errorf("tier S = %+v, want n=40 failures=4 failRate=0.1 agree=0.5", tier)
	}

	byModel := map[string]Group{}
	for _, g := range rep.ByModel.Groups {
		byModel[g.Model] = g
	}
	son, hai := byModel["sonnet"], byModel["haiku"]
	if son.N != 20 || son.FailRate != 0.2 || son.Agree != 0 || son.CostN != 20 {
		t.Errorf("sonnet = %+v", son)
	}
	if son.MeanCost == nil || *son.MeanCost != 1.05 || son.P90Cost == nil || *son.P90Cost != 1.8 {
		t.Errorf("sonnet cost mean=%v p90=%v, want 1.05 / 1.8", son.MeanCost, son.P90Cost)
	}
	if hai.N != 20 || hai.FailRate != 0 || hai.Agree != 1 || hai.CostN != 18 || *hai.MeanCost != 0.05 {
		t.Errorf("haiku = %+v", hai)
	}

	if len(rep.Divergent.Groups) != 1 {
		t.Fatalf("divergent = %+v", rep.Divergent)
	}
	d := rep.Divergent.Groups[0]
	if d.Tier != TierS || d.Pick != "haiku" || d.Model != "sonnet" || d.N != 20 {
		t.Errorf("divergent cell = %+v", d)
	}
	if d.PickRan == nil || d.PickRan.Model != "haiku" || d.PickRan.N != 20 {
		t.Errorf("divergent pickRan = %+v, want the tier-S haiku cell", d.PickRan)
	}
	if rep.HiddenGroups != 0 || rep.Rows != 40 {
		t.Errorf("hidden=%d rows=%d", rep.HiddenGroups, rep.Rows)
	}
}

func TestReport_HidesGroupsUnderMinSamples(t *testing.T) {
	c := 0.2
	rows := repeat(20, rrow("phaserun", TierM, "sonnet", "claude-sonnet-5", "completed", "pass", &c))
	rows = append(rows, repeat(19, rrow("phaserun", TierL, "opus", "claude-sonnet-5", "completed", "", &c))...)
	rep := buildReport(rows, MinSamples)

	if len(rep.ByTier.Groups) != 1 || rep.ByTier.Groups[0].Tier != TierM {
		t.Errorf("byTier groups = %+v, want only M", rep.ByTier.Groups)
	}
	if rep.ByTier.HiddenGroups != 1 || rep.ByTier.HiddenRuns != 19 {
		t.Errorf("byTier hidden = %d/%d", rep.ByTier.HiddenGroups, rep.ByTier.HiddenRuns)
	}
	// Both tiers ran sonnet: one model cell of 39.
	if len(rep.ByModel.Groups) != 1 || rep.ByModel.Groups[0].N != 39 {
		t.Errorf("byModel = %+v", rep.ByModel.Groups)
	}
	// The divergent (L, opus→sonnet) cell has 19 rows: hidden, not drawn.
	if len(rep.Divergent.Groups) != 0 || rep.Divergent.HiddenGroups != 1 || rep.Divergent.HiddenRuns != 19 {
		t.Errorf("divergent = %+v", rep.Divergent)
	}
	if rep.HiddenGroups != 2 {
		t.Errorf("hiddenGroups = %d, want 2", rep.HiddenGroups)
	}

	// Everything under the gate: nothing drawn at all, groups is [] not null.
	empty := buildReport(rows[:5], MinSamples)
	if empty.ByTier.Groups == nil || len(empty.ByTier.Groups)+len(empty.ByModel.Groups)+len(empty.Divergent.Groups) != 0 {
		t.Errorf("all-hidden report drew groups: %+v", empty)
	}
}

func TestReport_ExcludesRowsWithoutEvidence(t *testing.T) {
	rows := repeat(20, rrow("dispatch", TierS, "haiku", "haiku", OutcomeDone, "", nil))
	rows = append(rows, repeat(5, rrow("dispatch", TierS, "haiku", "haiku", OutcomeSuperseded, "", nil))...)
	// A superseded run whose verification failed IS evidence, and a failure.
	rows = append(rows, rrow("dispatch", TierS, "haiku", "haiku", OutcomeSuperseded, "fail", nil))
	rep := buildReport(rows, MinSamples)
	if rep.Rows != 21 || len(rep.ByTier.Groups) != 1 || rep.ByTier.Groups[0].Failures != 1 {
		t.Errorf("rows=%d byTier=%+v", rep.Rows, rep.ByTier.Groups)
	}
	if g := rep.ByTier.Groups[0]; g.MeanCost != nil || g.P90Cost != nil || g.CostN != 0 {
		t.Errorf("cost with no known costs = %+v, want nil", g)
	}
}

func TestReport_FromStoreWindowSurfaceAndUnsettled(t *testing.T) {
	db := openStore(t)
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	ins := func(surface, created string, outcome any) {
		mustExec(t, db, `INSERT INTO route_decisions (surface, subject, mode, signals_json, score, tier,
			pick_model, pick_effort, used_model, outcome, cost_usd, created_at)
			VALUES (?, 'task:1', 'shadow', '{}', 10, 'S', 'haiku', 'low', 'claude-sonnet-5', ?, 0.3, ?)`,
			surface, outcome, created)
	}
	for i := 0; i < 20; i++ {
		ins("dispatch", "2026-09-27T00:00:00.000Z", OutcomeDone)
	}
	ins("dispatch", "2026-09-27T00:00:00.000Z", nil)         // unsettled
	ins("dispatch", "2026-08-01T00:00:00.000Z", OutcomeDone) // outside 30 days
	for i := 0; i < 3; i++ {
		ins("phaserun", "2026-09-27T00:00:00.000Z", "completed")
	}

	rep, err := reportAt(db, "dispatch", 30, now)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Rows != 20 || rep.Unsettled != 1 || rep.Surface != "dispatch" || rep.Days != 30 || rep.MinSamples != 20 {
		t.Errorf("header = %+v", rep)
	}
	if len(rep.ByTier.Groups) != 1 || rep.ByTier.Groups[0].N != 20 || rep.ByTier.Groups[0].Agree != 0 {
		t.Errorf("byTier = %+v", rep.ByTier.Groups)
	}
	both, err := reportAt(db, "", 30, now)
	if err != nil {
		t.Fatal(err)
	}
	if both.Rows != 23 || both.ByTier.HiddenGroups != 1 || both.ByTier.HiddenRuns != 3 {
		t.Errorf("both surfaces = rows %d, hidden %d/%d", both.Rows, both.ByTier.HiddenGroups, both.ByTier.HiddenRuns)
	}
	// Report (wall clock) runs the same query.
	if _, err := Report(db, "", 3650); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Report(db, "", 30); err == nil {
		t.Error("Report on a closed DB returned nil")
	}
}

func TestReport_LatestDecision(t *testing.T) {
	db := openStore(t)
	if _, err := LatestDecision(db, "task:5"); !errors.Is(err, ErrNoDecision) {
		t.Fatalf("empty = %v, want ErrNoDecision", err)
	}
	sig := Signals{Surface: SurfaceDispatch, PromptBytes: 900, FileScope: 3, Areas: 1}
	d := Decide(sig, DefaultPolicy())
	for i, uuid := range []string{"old", "new"} {
		if err := Record(db, Row{Surface: SurfaceDispatch, Subject: "task:5", SessionUUID: uuid, Mode: ModeShadow,
			Signals: sig, Decision: d, UsedModel: "claude-sonnet-5", UsedEffort: "medium",
			UsedPlaybook: "standard", WonRung: RungDefault,
			CreatedAt: settleT0.Add(time.Duration(i) * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(t, db, `UPDATE route_decisions SET outcome='review', cost_usd=0.2 WHERE session_uuid='new'`)
	v, err := LatestDecision(db, "task:5")
	if err != nil {
		t.Fatal(err)
	}
	if v.Tier != d.Tier || v.PickModel != d.Model || v.UsedModel != "claude-sonnet-5" || v.WonRung != RungDefault ||
		v.Mode != "shadow" || v.Applied || len(v.Reasons) != len(d.Reasons) {
		t.Errorf("view = %+v", v)
	}
	if v.Outcome == nil || *v.Outcome != "review" || v.CostUSD == nil || *v.CostUSD != 0.2 || v.VerifyStatus != nil {
		t.Errorf("outcome cols = %v %v %v", v.Outcome, v.CostUSD, v.VerifyStatus)
	}
	if v.CreatedAt != "2026-09-27T11:00:00.000Z" {
		t.Errorf("createdAt = %q, want the newer row", v.CreatedAt)
	}
	mustExec(t, db, `UPDATE route_decisions SET reasons_json='null'`)
	if v, _ := LatestDecision(db, "task:5"); v.Reasons == nil {
		t.Error("null reasons decoded to nil, want []")
	}
	db.Close()
	if _, err := LatestDecision(db, "task:5"); err == nil || errors.Is(err, ErrNoDecision) {
		t.Errorf("closed DB = %v", err)
	}
}
