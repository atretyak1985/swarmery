package calibration

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/surprise"
)

func f(v float64) *float64 { return &v }
func i(v int) *int         { return &v }

func sample(model, effort string, conf float64, major bool, outcomeMiss float64) Sample {
	return Sample{
		Agent: "main", Model: model, Effort: effort, Project: "p", Index: 0.3,
		Components: map[string]*float64{surprise.CompOutcomeMiss: f(outcomeMiss)},
		Detail: surprise.Detail{
			MatchedAreas: []string{"a", "b"}, MissedAreas: []string{"c"}, UnexpectedAreas: []string{"d"},
			SizeDistance: i(0), DurationDistance: i(1), Confidence: f(conf), MajorMiss: major,
		},
	}
}

func many(n int, s Sample) []Sample {
	out := make([]Sample, n)
	for k := range out {
		out[k] = s
	}
	return out
}

func TestBuildHidesGroupsUnderTheGate(t *testing.T) {
	ss := append(many(20, sample("m1", "high", 0.9, false, 0)), many(19, sample("m2", "high", 0.9, false, 0))...)
	rep := Build(ss, []string{DimModel, DimEffort}, MinSamples)
	if len(rep.Groups) != 1 || rep.Groups[0].Key[DimModel] != "m1" || rep.Groups[0].Key[DimEffort] != "high" {
		t.Fatalf("groups = %+v", rep.Groups)
	}
	if rep.HiddenGroups != 1 || rep.HiddenRuns != 19 {
		t.Fatalf("hidden = %d/%d", rep.HiddenGroups, rep.HiddenRuns)
	}
	var buf bytes.Buffer
	if err := Render(&buf, rep, false); err != nil || strings.Contains(buf.String(), "m2") ||
		!strings.Contains(buf.String(), "m1 / high") {
		t.Fatalf("text render leaked a hidden group or lost a shown one: %q", buf.String())
	}
	empty := Build(many(3, sample("m", "", 0.5, false, 0)), AllDims, MinSamples)
	buf.Reset()
	if err := Render(&buf, empty, false); err != nil || !strings.Contains(buf.String(), "no group has enough samples") {
		t.Fatalf("empty render = %q", buf.String())
	}
	buf.Reset()
	if err := Render(&buf, empty, true); err != nil || !strings.Contains(buf.String(), `"hiddenGroups": 1`) {
		t.Fatalf("json render = %q", buf.String())
	}
}

func TestSummarizeMetrics(t *testing.T) {
	ss := append(many(10, sample("m", "low", 0.9, false, 0)), many(10, sample("m", "low", 0.1, true, 1))...)
	rep := Build(ss, []string{DimModel}, MinSamples)
	g := rep.Groups[0]
	// areas: 2 matched of 4 per run
	if *g.AreaHitRate != 0.5 {
		t.Fatalf("area hit = %v", *g.AreaHitRate)
	}
	// bands: size exact, duration off by one → half
	if *g.BandAccuracy != 0.5 || *g.OutcomeAccuracy != 0.5 || g.MeanSurprise != 0.3 {
		t.Fatalf("band %v outcome %v mean %v", *g.BandAccuracy, *g.OutcomeAccuracy, g.MeanSurprise)
	}
	if len(g.Buckets) != 2 || g.Buckets[0].Lo != 0 || g.Buckets[0].HeldRate != 0 ||
		g.Buckets[1].Lo != 0.8 || g.Buckets[1].HeldRate != 1 || g.Buckets[1].N != 10 {
		t.Fatalf("buckets = %+v", g.Buckets)
	}
	if gap := calibrationGap(g.Buckets); gap == nil || *gap != 0.1 {
		t.Fatalf("gap = %v", gap)
	}
	if calibrationGap(nil) != nil {
		t.Fatal("no buckets, no gap")
	}
	// confidence 1.0 lands in the top bucket, not a sixth one
	top := Build(many(20, sample("m", "x", 1.0, false, 0)), []string{DimModel}, MinSamples)
	if b := top.Groups[0].Buckets; len(b) != 1 || b[0].Hi != 1 {
		t.Fatalf("top bucket = %+v", b)
	}
}

func TestParseDims(t *testing.T) {
	if d, err := ParseDims(""); err != nil || len(d) != 4 {
		t.Fatalf("default = %v %v", d, err)
	}
	if d, err := ParseDims(" Model, effort,model "); err != nil || strings.Join(d, ",") != "model,effort" {
		t.Fatalf("parsed = %v %v", d, err)
	}
	if _, err := ParseDims("model,colour"); err == nil {
		t.Fatal("unknown dimension must fail")
	}
}

func TestLoadReadsLabelsAndSkipsPostHoc(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "cal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO projects(id, path, slug, first_seen) VALUES (1, '/repo', 'proj', 'now')`)
	exec(`INSERT INTO tasks (id, project_id, title, prompt, status, created_at, source) VALUES (7, 1, 'P', 'g', 'running', 'now', 'workspace')`)
	exec(`INSERT INTO epic_phases (id, workspace_task_id, seq, name, doc_path, depends_on, run_state, run_session_uuid, run_effort, doc_model)
		VALUES (3, 7, 1, 'Ph', '/d.md', '[]', 'done', 'u1', 'high', 'doc-model')`)
	exec(`INSERT INTO sessions (id, project_id, session_uuid, model, status, started_at) VALUES (11, 1, 'u1', 'claude-x[1m]', 'completed', 'now')`)
	exec(`INSERT INTO turns (session_id, seq, role, agent_name, started_at) VALUES (11, 1, 'assistant', 'implementer', 'now')`)
	exec(`INSERT INTO phase_surprise (phase_id, session_uuid, surprise_index, computed_at) VALUES (3, 'u1', 0.4, 'now')`)
	exec(`INSERT INTO phase_surprise (phase_id, session_uuid, surprise_index, computed_at) VALUES (3, 'u0', 0.9, 'now')`)
	exec(`INSERT INTO phase_surprise (phase_id, session_uuid, forecast_post_hoc, surprise_index, computed_at) VALUES (3, 'u2', 1, 0.0, 'now')`)
	ss, err := Load(db)
	if err != nil || len(ss) != 2 {
		t.Fatalf("samples = %+v, %v", ss, err)
	}
	var cur, old Sample
	for _, s := range ss {
		if s.Index == 0.4 {
			cur = s
		} else {
			old = s
		}
	}
	if cur.Model != "claude-x" || cur.Effort != "high" || cur.Agent != "implementer" || cur.Project != "proj" {
		t.Fatalf("current run labels = %+v", cur)
	}
	// an earlier run of the same phase: no session, effort not recorded
	if old.Model != "doc-model" || old.Effort != "" || old.Agent != "main" {
		t.Fatalf("earlier run labels = %+v", old)
	}
	if k := old.key(AllDims); k[DimEffort] != Unknown {
		t.Fatalf("key = %v", k)
	}
	rep, err := Compute(db, AllDims, 1)
	if err != nil || len(rep.Groups) != 2 {
		t.Fatalf("compute = %+v, %v", rep, err)
	}
}
